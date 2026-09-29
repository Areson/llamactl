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

// stopAdoptedProcessTree stops an adopted process on non-Windows by signaling
// the root PID (process-group kill remains Setpgid + signalStop for owned
// spawns). Returns method "signal".
func stopAdoptedProcessTree(instanceName string, rootPID int) (method string, err error) {
	_ = instanceName
	if err := stopProcessByPID(rootPID); err != nil {
		return "signal", err
	}
	return "signal", nil
}
