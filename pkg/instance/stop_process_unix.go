//go:build !windows

package instance

import (
	"fmt"
	"os"
	"syscall"
)

// stopProcessByPID sends SIGTERM to the process with the given PID.
func stopProcessByPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("FindProcess(%d) failed: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("SIGTERM to PID %d failed: %w", pid, err)
	}
	return nil
}
