//go:build !windows

package instance

import (
	"os"
	"syscall"
)

// PIDAlive reports whether a process with the given PID is running.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 is the portable "is this PID alive?" check.
	return proc.Signal(syscall.Signal(0)) == nil
}
