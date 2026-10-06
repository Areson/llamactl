//go:build windows

package instance

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setProcAttrs gives the child its own hidden console (CREATE_NO_WINDOW).
//
//   - Its own console, not llamactl's: the child survives llamactl exiting
//     (hot-swap) and a Ctrl-C on llamactl's console never reaches it.
//   - A console at all: signalStop can attach to it and raise CTRL_C_EVENT for
//     a clean shutdown. DETACHED_PROCESS children have none and can only be
//     hard-killed.
//   - No CREATE_NEW_PROCESS_GROUP: it sets the inherited "ignore Ctrl-C" flag,
//     which would make the child (and everything it spawns) ignore the event.
func setProcAttrs(cmd *exec.Cmd) {
	ClearInheritedCtrlCIgnore()
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

// signalStop requests a clean shutdown: Ctrl-C on the child's console, which
// Python (SIGINT), Go (os.Interrupt), Node and llama-server all handle.
func signalStop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("no process")
	}
	return sendConsoleCtrlC(cmd.Process.Pid)
}
