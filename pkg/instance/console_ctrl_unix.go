//go:build !windows

package instance

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// RunConsoleCtrlHelper is Windows-only; on other platforms args are never a
// helper invocation.
func RunConsoleCtrlHelper(args []string) (handled bool, exitCode int) {
	return false, 0
}

// gracefulStopPID sends SIGINT, the same clean-stop request signalStop uses
// for owned processes.
func gracefulStopPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("FindProcess(%d) failed: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		return fmt.Errorf("SIGINT to PID %d failed: %w", pid, err)
	}
	return nil
}

// waitPIDExit polls until pid is gone or timeout elapses. It reports true
// when the process has exited.
func waitPIDExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for PIDAlive(pid) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return true
}
