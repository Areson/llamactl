//go:build windows

// Package hotswap: A-side orchestration.
// Moved from pkg/manager/hotswap.go.

package hotswap

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"llamactl/pkg/hotswap/ws2"
)

// SocketSource provides raw socket handles for duplication.
// Implemented by server.TrackingListener.
type SocketSource interface {
	SocketHandles() []syscall.Handle
}

// ASideOptions configures the A-side of a hot-swap.
type ASideOptions struct {
	// BinaryPath is the path to the new llamactl binary.
	BinaryPath string
	// DataDir is the data directory (for handoff-state.json).
	DataDir string
	// ConfigEnv is the environment for B (with abs config path).
	ConfigEnv []string
	// SocketSource provides socket handles for duplication. Optional.
	SocketSource SocketSource
	// Drainer tracks in-flight work for the drain. Optional.
	Drainer Drainer
	// DrainTimeout is the maximum drain time. Default 5s.
	DrainTimeout time.Duration
}

// ASide runs the A-side of a hot-swap:
//  1. Write handoff state (PhaseStarting)
//  2. Pick free port, copy binary, spawn B
//  3. Dial B, exchange READY/DUP/BYE/ACK
//  4. Drain in-flight work
//  5. Rename binaries (atomic swap)
//  6. Caller closes listener and exits
func ASide(opts ASideOptions) error {
	if opts.DrainTimeout == 0 {
		opts.DrainTimeout = 5 * time.Second
	}

	aPID := os.Getpid()
	dataDir := opts.DataDir

	state := &HandoffState{
		Phase:      PhaseStarting,
		APID:       aPID,
		BinaryPath: opts.BinaryPath,
		StartedAt:  time.Now(),
	}
	if err := WriteHandoffState(dataDir, state); err != nil {
		return fmt.Errorf("failed to write initial handoff state: %w", err)
	}

	// Pick a free port for the handoff channel.
	port, err := pickFreePort()
	if err != nil {
		failASide(dataDir, "failed to pick free port: %v", err)
		return fmt.Errorf("failed to pick free port: %w", err)
	}
	state.HandoffPort = port

	// Resolve the production executable path.
	aExe, err := os.Executable()
	if err != nil {
		failASide(dataDir, "failed to get executable path: %v", err)
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Step 1: rename the running binary out of the way.
	// Windows allows renaming a running .exe — the process keeps running.
	oldPath := aExe + ".old"
	if err := os.Rename(aExe, oldPath); err != nil {
		// If the old file already exists from a previous swap, remove it first.
		if os.IsExist(err) {
			os.Remove(oldPath)
			if err := os.Rename(aExe, oldPath); err != nil {
				failASide(dataDir, "failed to rename old binary: %v", err)
				return fmt.Errorf("failed to rename old binary: %w", err)
			}
		} else {
			failASide(dataDir, "failed to rename old binary: %v", err)
			return fmt.Errorf("failed to rename old binary: %w", err)
		}
	}
	log.Printf("ASide: renamed %s → %s", aExe, oldPath)

	// Step 2: copy the new binary into the production path.
	if err := copyFile(opts.BinaryPath, aExe); err != nil {
		// Rollback: restore the old binary.
		os.Rename(oldPath, aExe)
		failASide(dataDir, "failed to copy new binary: %v", err)
		return fmt.Errorf("failed to copy new binary: %w", err)
	}
	log.Printf("ASide: copied %s → %s", opts.BinaryPath, aExe)

	// Spawn B from the new production binary.
	cmd := exec.Command(aExe,
		fmt.Sprintf("--handoff-port=%d", port),
		"--hot-swap",
	)
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	} else {
		cmd.Dir = filepath.Dir(aExe)
	}
	cmd.Env = opts.ConfigEnv

	// Capture B's stdout/stderr to a log file so we can diagnose
	// startup failures. B also tees to its own file via setupLogFile,
	// but this catches early output before B's Go runtime initializes.
	if dataDir != "" {
		bLogPath := filepath.Join(dataDir, "hotswap-b.log")
		if bLog, err := os.OpenFile(bLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			cmd.Stdout = bLog
			cmd.Stderr = bLog
		} else {
			log.Printf("ASide: warning: could not open B log file: %v", err)
		}
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		failASide(dataDir, "failed to spawn B: %v", err)
		return fmt.Errorf("failed to spawn B: %w", err)
	}
	bPID := cmd.Process.Pid
	log.Printf("ASide: spawned B with PID %d", bPID)

	state.BPID = bPID
	if err := WriteHandoffState(dataDir, state); err != nil {
		cmd.Process.Kill()
		failASide(dataDir, "failed to update handoff state: %v", err)
		return fmt.Errorf("failed to update handoff state: %w", err)
	}

	// Dial B's handoff port.
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
		failASide(dataDir, "B did not open handoff port %d within 10s", port)
		return fmt.Errorf("B did not open handoff port %d within 10s", port)
	}
	defer conn.Close()

	// Protocol exchange.
	channel := NewHandoffChannelFromConn(conn)

	readyLine, err := channel.ReadLine()
	if err != nil {
		cmd.Process.Kill()
		failASide(dataDir, "failed to read READY from B: %v", err)
		return fmt.Errorf("failed to read READY from B: %w", err)
	}
	bPIDFromReady, err := ParseReady(readyLine)
	if err != nil {
		cmd.Process.Kill()
		failASide(dataDir, "failed to parse READY: %v", err)
		return fmt.Errorf("failed to parse READY: %w", err)
	}
	if bPIDFromReady != bPID {
		cmd.Process.Kill()
		failASide(dataDir, "PID mismatch: expected %d, got %d", bPID, bPIDFromReady)
		return fmt.Errorf("PID mismatch: expected %d, got %d", bPID, bPIDFromReady)
	}
	log.Printf("ASide: B (PID %d) is ready", bPID)

	state.Phase = PhaseInProgress
	if err := WriteHandoffState(dataDir, state); err != nil {
		log.Printf("ASide: warning: failed to update state to in_progress: %v", err)
	}

	// V1: rebind-only. Socket duplication is V2.
	// If SocketSource is provided, duplicate each socket to B.
	if opts.SocketSource != nil {
		handles := opts.SocketSource.SocketHandles()
		state.TotalSockets = len(handles)
		log.Printf("ASide: duplicating %d client sockets to B", len(handles))

		for _, fd := range handles {
			blob, err := ws2.DuplicateSocket(fd, uint32(bPID))
			if err != nil {
				log.Printf("ASide: failed to duplicate socket %d: %v (skipping)", fd, err)
				continue
			}
			if err := channel.SendDUP(blob); err != nil {
				log.Printf("ASide: failed to send DUP: %v (skipping)", err)
				continue
			}
			gotLine, err := channel.ReadLine()
			if err != nil {
				log.Printf("ASide: failed to read GOT: %v", err)
				continue
			}
			ok, errMsg, _ := ParseGOT(gotLine)
			if !ok {
				log.Printf("ASide: B failed to adopt socket: %s", errMsg)
				continue
			}
			state.SocketsHandedOff++
			log.Printf("ASide: socket handed off (%d/%d)", state.SocketsHandedOff, len(handles))
		}
	} else {
		state.TotalSockets = 0
		log.Printf("ASide: V1 rebind-only mode; no socket handoff")
	}

	// BYE / ACK.
	if err := channel.SendBYE(); err != nil {
		log.Printf("ASide: failed to send BYE: %v", err)
	}
	ackLine, err := channel.ReadLine()
	if err != nil {
		log.Printf("ASide: failed to read ACK: %v", err)
	} else if err := ParseACK(ackLine); err != nil {
		log.Printf("ASide: unexpected response to BYE: %v", err)
	}

	// Drain in-flight work.
	if opts.Drainer != nil {
		DrainA(opts.Drainer, DrainOptions{Timeout: opts.DrainTimeout})
	}

	// Cleanup: remove the old binary (best-effort — A is about to exit).
	if err := os.Remove(oldPath); err != nil {
		log.Printf("ASide: cleanup: could not remove %s: %v (will be swept on next startup)", oldPath, err)
	} else {
		log.Printf("ASide: cleanup: removed %s", oldPath)
	}

	now := time.Now()
	state.Phase = PhaseComplete
	state.CompletedAt = &now
	if err := WriteHandoffState(dataDir, state); err != nil {
		log.Printf("ASide: warning: failed to update state to complete: %v", err)
	}

	log.Printf("ASide: complete. B (PID %d) will bind the public port after A closes the listener.", bPID)
	return nil
}

func failASide(dataDir string, format string, args ...interface{}) {
	errMsg := fmt.Sprintf(format, args...)
	log.Printf("ASide: FAILED: %s", errMsg)

	state, _ := ReadHandoffState(dataDir)
	if state == nil {
		state = &HandoffState{}
	}
	state.Phase = PhaseFailed
	state.Error = errMsg
	WriteHandoffState(dataDir, state)
	RemoveHandoffState(dataDir)
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
