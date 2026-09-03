//go:build windows

package instance

import (
	"fmt"
	"syscall"
	"time"
)

var (
	kernel32Stop   = syscall.NewLazyDLL("kernel32.dll")
	procOpenProc   = kernel32Stop.NewProc("OpenProcess")
	procTerminate  = kernel32Stop.NewProc("TerminateProcess")
	procCloseHnd   = kernel32Stop.NewProc("CloseHandle")
)

const (
	processTerminate = 0x0001 // PROCESS_TERMINATE
)

// stopProcessByPID terminates the process with the given PID.
// On Windows, this uses TerminateProcess (equivalent to a hard kill).
// The llama-server backend handles its own cleanup on exit.
func stopProcessByPID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	handle, _, err := procOpenProc.Call(
		uintptr(processTerminate),
		0, // bInheritHandle = FALSE
		uintptr(pid),
	)
	if handle == 0 {
		return fmt.Errorf("OpenProcess(%d) failed: %w", pid, err)
	}
	defer procCloseHnd.Call(handle)

	r, _, callErr := procTerminate.Call(handle, 0 /* exit code */)
	if r == 0 {
		return fmt.Errorf("TerminateProcess(%d) failed: %v", pid, callErr)
	}

	// Give the process a moment to release its port.
	time.Sleep(500 * time.Millisecond)
	return nil
}
