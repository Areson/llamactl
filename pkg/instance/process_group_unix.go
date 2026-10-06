//go:build !windows

package instance

import (
	"fmt"
	"os/exec"
	"syscall"
)

func setProcAttrs(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalStop delivers SIGINT (os.Interrupt) to the child.
func signalStop(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("no process")
	}
	return cmd.Process.Signal(syscall.SIGINT)
}
