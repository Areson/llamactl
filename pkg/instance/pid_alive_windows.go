//go:build windows

package instance

import "golang.org/x/sys/windows"

// stillActive is GetExitCodeProcess's code for a process that has not exited.
const stillActive = 259 // STILL_ACTIVE

// PIDAlive reports whether a process with the given PID is running.
//
// Opening the PID is not enough: an exited process stays openable for as
// long as anyone holds a handle to it (the supervisor holds one to each
// llamactl generation), so check that it has not exited.
func PIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err == nil {
		defer windows.CloseHandle(h)
		ev, werr := windows.WaitForSingleObject(h, 0)
		return werr == nil && ev == uint32(windows.WAIT_TIMEOUT)
	}
	// No SYNCHRONIZE access (e.g. a protected process): fall back to the
	// exit code, which reads STILL_ACTIVE while running.
	h, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
