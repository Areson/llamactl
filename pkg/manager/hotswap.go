//go:build windows

package manager

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"llamactl/pkg/hotswap"
)

type socketHandleSource interface {
	SocketHandles() []syscall.Handle
}

// HotSwap replaces the running llamactl binary with a new version without
// stopping model instances. Live client sockets are duplicated to B; the
// public port is rebound by B after A closes its listener.
func (im *instanceManager) HotSwap(binaryPath string) error {
	if err := im.AcquireSwap(); err != nil {
		return err
	}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			im.ReleaseSwap()
		}
	}()

	dataDir := im.globalConfig.DataDir
	aPID := os.Getpid()

	state := &hotswap.HandoffState{
		Phase:      hotswap.PhaseStarting,
		APID:       aPID,
		BinaryPath: binaryPath,
		StartedAt:  time.Now(),
	}
	if err := hotswap.WriteHandoffState(dataDir, state); err != nil {
		return fmt.Errorf("failed to write initial handoff state: %w", err)
	}

	port, err := pickFreePort()
	if err != nil {
		im.failHandoff(dataDir, "failed to pick free port: %v", err)
		return fmt.Errorf("failed to pick free port: %w", err)
	}
	state.HandoffPort = port

	aExe, err := os.Executable()
	if err != nil {
		im.failHandoff(dataDir, "failed to get executable path: %v", err)
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	aDir := filepath.Dir(aExe)
	candidatePath := filepath.Join(aDir, "llamactl-candidate.exe")

	if err := copyFile(binaryPath, candidatePath); err != nil {
		im.failHandoff(dataDir, "failed to copy binary: %v", err)
		return fmt.Errorf("failed to copy binary: %w", err)
	}
	defer os.Remove(candidatePath)

	cmd := exec.Command(candidatePath,
		fmt.Sprintf("--handoff-port=%d", port),
		"--hot-swap",
	)
	cmd.Dir = aDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		im.failHandoff(dataDir, "failed to spawn B: %v", err)
		return fmt.Errorf("failed to spawn B: %w", err)
	}
	bPID := cmd.Process.Pid
	log.Printf("HotSwap: spawned B with PID %d", bPID)

	state.BPID = bPID
	if err := hotswap.WriteHandoffState(dataDir, state); err != nil {
		cmd.Process.Kill()
		im.failHandoff(dataDir, "failed to update handoff state: %v", err)
		return fmt.Errorf("failed to update handoff state: %w", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		var dialErr error
		conn, dialErr = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if dialErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if conn == nil {
		cmd.Process.Kill()
		im.failHandoff(dataDir, "B did not open handoff port %d within 10s", port)
		return fmt.Errorf("B did not open handoff port %d within 10s", port)
	}
	defer conn.Close()

	channel := hotswap.NewHandoffChannelFromConn(conn)

	readyLine, err := channel.ReadLine()
	if err != nil {
		cmd.Process.Kill()
		im.failHandoff(dataDir, "failed to read READY from B: %v", err)
		return fmt.Errorf("failed to read READY from B: %w", err)
	}
	bPIDFromReady, err := hotswap.ParseReady(readyLine)
	if err != nil {
		cmd.Process.Kill()
		im.failHandoff(dataDir, "failed to parse READY: %v", err)
		return fmt.Errorf("failed to parse READY: %w", err)
	}
	if bPIDFromReady != bPID {
		cmd.Process.Kill()
		im.failHandoff(dataDir, "PID mismatch: expected %d, got %d", bPID, bPIDFromReady)
		return fmt.Errorf("PID mismatch: expected %d, got %d", bPID, bPIDFromReady)
	}
	log.Printf("HotSwap: B (PID %d) is ready", bPID)

	state.Phase = hotswap.PhaseInProgress
	if err := hotswap.WriteHandoffState(dataDir, state); err != nil {
		log.Printf("HotSwap: warning: failed to update state to in_progress: %v", err)
	}

	handles := im.clientSocketHandles()
	state.TotalSockets = len(handles)
	log.Printf("HotSwap: handing off %d live client sockets", len(handles))

	for _, fd := range handles {
		blob, err := ws2DuplicateSocket(fd, uint32(bPID))
		if err != nil {
			log.Printf("HotSwap: failed to duplicate socket %d: %v (skipping)", fd, err)
			continue
		}
		if err := channel.SendDUP(blob); err != nil {
			log.Printf("HotSwap: failed to send DUP: %v (skipping)", err)
			continue
		}
		gotLine, err := channel.ReadLine()
		if err != nil {
			log.Printf("HotSwap: failed to read GOT: %v", err)
			continue
		}
		ok, errMsg, _ := hotswap.ParseGOT(gotLine)
		if !ok {
			log.Printf("HotSwap: B failed to adopt socket: %s", errMsg)
			continue
		}
		state.SocketsHandedOff++
		log.Printf("HotSwap: socket handed off (%d/%d)", state.SocketsHandedOff, len(handles))
	}

	if err := channel.SendBYE(); err != nil {
		log.Printf("HotSwap: failed to send BYE: %v", err)
	}
	ackLine, err := channel.ReadLine()
	if err != nil {
		log.Printf("HotSwap: failed to read ACK: %v", err)
	} else if err := hotswap.ParseACK(ackLine); err != nil {
		log.Printf("HotSwap: unexpected response to BYE: %v", err)
	}

	oldPath := aExe + ".old"
	if err := os.Rename(aExe, oldPath); err != nil {
		im.failHandoff(dataDir, "failed to rename old binary: %v", err)
		return fmt.Errorf("failed to rename old binary: %w", err)
	}

	if err := os.Rename(candidatePath, aExe); err != nil {
		os.Rename(oldPath, aExe)
		im.failHandoff(dataDir, "failed to rename candidate: %v", err)
		return fmt.Errorf("failed to rename candidate: %w", err)
	}

	now := time.Now()
	state.Phase = hotswap.PhaseComplete
	state.CompletedAt = &now
	if err := hotswap.WriteHandoffState(dataDir, state); err != nil {
		log.Printf("HotSwap: warning: failed to update state to complete: %v", err)
	}

	log.Printf("HotSwap: complete. B (PID %d) will bind the public port after A closes the listener.", bPID)

	// Release :8079 so B can rebind. Keep the swap mutex until this process
	// exits so start/stop cannot race the successor.
	im.CloseListener()
	releaseOnReturn = false
	return nil
}

func (im *instanceManager) failHandoff(dataDir string, format string, args ...interface{}) {
	errMsg := fmt.Sprintf(format, args...)
	log.Printf("HotSwap: FAILED: %s", errMsg)

	state, _ := hotswap.ReadHandoffState(dataDir)
	if state == nil {
		state = &hotswap.HandoffState{}
	}
	state.Phase = hotswap.PhaseFailed
	state.Error = errMsg
	hotswap.WriteHandoffState(dataDir, state)
	hotswap.RemoveHandoffState(dataDir)
}

func (im *instanceManager) clientSocketHandles() []syscall.Handle {
	if im.connTracker == nil {
		return nil
	}
	if src, ok := im.connTracker.(socketHandleSource); ok {
		return src.SocketHandles()
	}
	return nil
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr := l.Addr().(*net.TCPAddr)
	return addr.Port, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}
