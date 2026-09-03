//go:build windows

package instance

import "syscall"

var (
	kernel32Alive = syscall.NewLazyDLL("kernel32.dll")
	procOpenAlive = kernel32Alive.NewProc("OpenProcess")
)

const processQueryLimit = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION

// PIDAlive reports whether a process with the given PID is running.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, _, _ := procOpenAlive.Call(
		uintptr(processQueryLimit),
		0,
		uintptr(pid),
	)
	if handle == 0 {
		return false
	}
	procCloseHnd.Call(handle)
	return true
}
