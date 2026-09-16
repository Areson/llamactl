//go:build windows

// Package hotswap: A-side orchestration.
package hotswap

import (
	"bytes"
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
	BinaryPath   string
	DataDir      string
	ConfigEnv    []string
	SocketSource SocketSource
	Drainer      Drainer
	DrainTimeout time.Duration
}

// ASide runs the A-side of a hot-swap:
//  1. Write handoff state (PhaseStarting)
//  2. Pick free port, rename old binary, copy new, spawn B
//  3. Dial B, exchange READY/DUP/BYE/ACK
//  4. Drain in-flight work
//  5. Caller closes listener and exits
func ASide(opts ASideOptions) error {
	if opts.DrainTimeout == 0 {
		opts.DrainTimeout = 5 * time.Second
	}
	dataDir := opts.DataDir

	state := &HandoffState{
		Phase:      PhaseStarting,
		APID:       os.Getpid(),
		BinaryPath: opts.BinaryPath,
		StartedAt:  time.Now(),
	}
	if err := WriteHandoffState(dataDir, state); err != nil {
		return fmt.Errorf("failed to write initial handoff state: %w", err)
	}

	port, err := pickFreePort()
	if err != nil {
		return failASide(dataDir, fmt.Errorf("failed to pick free port: %w", err))
	}
	state.HandoffPort = port

	aExe, err := os.Executable()
	if err != nil {
		return failASide(dataDir, fmt.Errorf("failed to get executable path: %w", err))
	}

	// Pre-flight: verify the target binary actually implements the B-side
	// of the protocol. A pre-hot-swap target would start as a plain
	// server, lose the port race against A, die, and leave the install
	// wedged (A's image locked as .old, the new binary sitting at the
	// production path with nothing to clean it up). Failing here is free.
	if ok, perr := isBCapable(opts.BinaryPath); perr != nil {
		return failASide(dataDir, fmt.Errorf("cannot probe target binary: %w", perr))
	} else if !ok {
		return failASide(dataDir, fmt.Errorf("target binary %s has no B-side (hot-swap) support; aborted before any filesystem change", opts.BinaryPath))
	}
	log.Printf("ASide: target %s is B-capable", opts.BinaryPath)

	// Step 1: rename the running binary out of the way.
	// Windows allows renaming a running .exe — the process keeps running.
	oldPath := aExe + ".old"
	os.Remove(oldPath) // idempotent: clear any leftover from a previous swap
	if err := os.Rename(aExe, oldPath); err != nil {
		return failASide(dataDir, fmt.Errorf("failed to rename old binary: %w", err))
	}
	log.Printf("ASide: renamed %s → %s", aExe, oldPath)

	// Step 2: copy the new binary into the production path.
	if err := copyFile(opts.BinaryPath, aExe); err != nil {
		rollbackBinary(aExe, oldPath, nil)
		return failASide(dataDir, fmt.Errorf("failed to copy new binary: %w", err))
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

	// Capture B's stdout/stderr so we can diagnose startup failures.
	if dataDir != "" {
		bLogPath := filepath.Join(dataDir, "hotswap-b.log")
		if bLog, err := os.OpenFile(bLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			cmd.Stdout = bLog
			cmd.Stderr = bLog
		}
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		rollbackBinary(aExe, oldPath, cmd)
		return failASide(dataDir, fmt.Errorf("failed to spawn B: %w", err))
	}
	bPID := cmd.Process.Pid
	log.Printf("ASide: spawned B with PID %d", bPID)

	state.BPID = bPID
	if err := WriteHandoffState(dataDir, state); err != nil {
		rollbackBinary(aExe, oldPath, cmd)
		return failASide(dataDir, fmt.Errorf("failed to update handoff state: %w", err))
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
		rollbackBinary(aExe, oldPath, cmd)
		return failASide(dataDir, fmt.Errorf("B did not open handoff port %d within 10s", port))
	}
	defer conn.Close()

	channel := NewHandoffChannelFromConn(conn)

	readyLine, err := channel.ReadLine()
	if err != nil {
		rollbackBinary(aExe, oldPath, cmd)
		return failASide(dataDir, fmt.Errorf("failed to read READY from B: %w", err))
	}
	bPIDFromReady, err := ParseReady(readyLine)
	if err != nil {
		rollbackBinary(aExe, oldPath, cmd)
		return failASide(dataDir, fmt.Errorf("failed to parse READY: %w", err))
	}
	if bPIDFromReady != bPID {
		rollbackBinary(aExe, oldPath, cmd)
		return failASide(dataDir, fmt.Errorf("PID mismatch: expected %d, got %d", bPID, bPIDFromReady))
	}
	log.Printf("ASide: B (PID %d) is ready", bPID)

	state.Phase = PhaseInProgress
	if err := WriteHandoffState(dataDir, state); err != nil {
		log.Printf("ASide: warning: failed to update state to in_progress: %v", err)
	}

	// Duplicate client sockets to B.
	if opts.SocketSource != nil {
		handles := opts.SocketSource.SocketHandles()
		state.TotalSockets = len(handles)
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
			if ok, errMsg, _ := ParseGOT(gotLine); !ok {
				log.Printf("ASide: B failed to adopt socket: %s", errMsg)
				continue
			}
			state.SocketsHandedOff++
		}
		log.Printf("ASide: socket handoff complete (%d/%d)", state.SocketsHandedOff, len(handles))
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

// failASide logs the error, records the failed state, and returns it.
func failASide(dataDir string, err error) error {
	log.Printf("ASide: FAILED: %s", err)
	state, _ := ReadHandoffState(dataDir)
	if state == nil {
		state = &HandoffState{}
	}
	state.Phase = PhaseFailed
	state.Error = err.Error()
	WriteHandoffState(dataDir, state)
	RemoveHandoffState(dataDir)
	return err
}

// BCapabilityMarker is embedded (as a string literal) in every build that
// contains the hot-swap package. ASide scans the target binary for it
// before mutating the filesystem, so a target without B-side support
// fails fast instead of wedging the install.
const BCapabilityMarker = "LLAMACTL_HOTSWAP_B_CAPABLE_v1"

// isBCapable reports whether binaryPath contains the B-side marker.
func isBCapable(binaryPath string) (bool, error) {
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		return false, err
	}
	return bytes.Contains(data, []byte(BCapabilityMarker)), nil
}

// rollbackBinary restores the original binary to aExe after a failure in
// the rename/copy/spawn phase. B is killed first if it was started, then
// the rename is retried until the killed process releases its image
// (Windows pins a running exe until it exits).
func rollbackBinary(aExe, oldPath string, b *exec.Cmd) {
	if b != nil && b.Process != nil {
		b.Process.Kill()
		go b.Process.Wait() // reap; do not block the rollback on it
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		os.Remove(aExe) // drop the copied target; it may not exist
		if err := os.Rename(oldPath, aExe); err == nil {
			log.Printf("ASide: rollback complete — original binary restored to %s", aExe)
			return
		} else if time.Now().After(deadline) {
			log.Printf("ASide: ROLLBACK FAILED (manual recovery: kill the new llamactl, then rename %s back to %s)", oldPath, aExe)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0755)
}
