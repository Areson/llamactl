//go:build windows

package instance

import (
	"os/exec"
	"syscall"
)

// Windows process creation flags for detached, swappable child processes.
const (
	// DETACHED_PROCESS: the child has no console. Survives the parent's death.
	DETACHED_PROCESS = 0x00000008
	// CREATE_NEW_PROCESS_GROUP: the child gets its own process group.
	// Allows it to be managed independently (not killed when the parent's
	// console session ends).
	CREATE_NEW_PROCESS_GROUP = 0x00000200
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procGenerateEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

func setProcAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// DETACHED_PROCESS: child has no console, survives parent's death.
	// CREATE_NEW_PROCESS_GROUP: child is in its own process group,
	// not tied to the parent's console lifecycle.
	cmd.SysProcAttr.CreationFlags |= DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
}

// signalStop on Windows. SIGINT does not reach child processes, and
// GenerateConsoleCtrlEvent is unreliable in a service session.
// The primary stop mechanism is stdin close (EOF) handled by the backend;
// this is a best-effort fallback.
func signalStop(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Best-effort: try to send a CTRL_C_EVENT to the child's process group.
	// This may not work if the child is detached; the force-kill in
	// process.go is the reliable fallback.
	procGenerateEvent.Call(2 /* CTRL_C_EVENT */, uintptr(cmd.Process.Pid))
}
