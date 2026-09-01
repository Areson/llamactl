//go:build windows

package instance

import (
	"os/exec"
	"syscall"

	windows "golang.org/x/sys/windows"
)

func setProcAttrs(cmd *exec.Cmd) {
	// Give the child its own (hidden) console so we can send it Ctrl-C
	// later (GenerateConsoleCtrlEvent) for a graceful stop.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_CONSOLE
}

// signalStop delivers the platform "interrupt" to the child process.
// On Windows that is Ctrl-C on the child's own console: llama.cpp
// imports SetConsoleCtrlHandler and shuts the server down cleanly when
// it fires. (os.Interrupt/SIGINT are no-ops for child processes on
// Windows, so this is the real thing.)
func signalStop(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, uint32(cmd.Process.Pid))
}
