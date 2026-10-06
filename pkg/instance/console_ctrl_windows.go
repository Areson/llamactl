//go:build windows

package instance

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

var (
	modKernel32Ctrl             = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole           = modKernel32Ctrl.NewProc("AttachConsole")
	procFreeConsole             = modKernel32Ctrl.NewProc("FreeConsole")
	procSetConsoleCtrlHandler   = modKernel32Ctrl.NewProc("SetConsoleCtrlHandler")
	procGenerateConsoleCtrlEvnt = modKernel32Ctrl.NewProc("GenerateConsoleCtrlEvent")
)

// consoleCtrlHelperExe resolves the binary that serves ConsoleCtrlHelperArg.
// Tests point it at the test binary (see TestMain).
var consoleCtrlHelperExe = os.Executable

const consoleCtrlHelperTimeout = 10 * time.Second

var clearCtrlCIgnoreOnce sync.Once

// clearInheritedCtrlCIgnore turns Ctrl-C processing back on for llamactl so
// backends it spawns inherit it enabled.
//
// Windows keeps a per-process "ignore Ctrl-C" flag that children inherit and
// that CREATE_NEW_PROCESS_GROUP sets. llamactl itself may carry it (hot-swap
// B is spawned with CREATE_NEW_PROCESS_GROUP; service wrappers may set it).
// With the flag set, CTRL_C_EVENT is delivered but no handler runs — the
// child silently ignores the clean-stop request.
func clearInheritedCtrlCIgnore() {
	clearCtrlCIgnoreOnce.Do(func() {
		if r, _, err := procSetConsoleCtrlHandler.Call(0, 0); r == 0 {
			log.Printf("SetConsoleCtrlHandler(NULL, FALSE) failed: %v; backends may ignore Ctrl-C stop", err)
		}
	})
}

// sendConsoleCtrlC asks the process tree on pid's console to shut down
// cleanly by raising CTRL_C_EVENT on that console, via a detached helper.
// Every process attached to the console receives it (the backend and any
// re-exec'd children), so each instance must have its own console.
func sendConsoleCtrlC(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}
	exe, err := consoleCtrlHelperExe()
	if err != nil {
		return fmt.Errorf("resolve helper executable: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), consoleCtrlHelperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, ConsoleCtrlHelperArg, strconv.Itoa(pid))
	// DETACHED_PROCESS: the helper starts with no console so it can attach
	// to the target's.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case consoleCtrlAttachFail:
			return fmt.Errorf("PID %d has no console to attach to (started without one?): %s", pid, out)
		case consoleCtrlGenerateErr:
			return fmt.Errorf("GenerateConsoleCtrlEvent for PID %d failed: %s", pid, out)
		}
	}
	return fmt.Errorf("console Ctrl-C helper for PID %d failed: %v: %s", pid, err, out)
}

// RunConsoleCtrlHelper runs the helper side when args (os.Args) select it.
// It reports whether args were a helper invocation and the exit code to use.
func RunConsoleCtrlHelper(args []string) (handled bool, exitCode int) {
	if len(args) < 2 || args[1] != ConsoleCtrlHelperArg {
		return false, 0
	}
	if len(args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s %s <pid>\n", args[0], ConsoleCtrlHelperArg)
		return true, consoleCtrlUsage
	}
	pid, err := strconv.Atoi(args[2])
	if err != nil || pid <= 0 {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", args[2])
		return true, consoleCtrlUsage
	}
	return true, raiseCtrlCOnConsoleOf(pid)
}

func raiseCtrlCOnConsoleOf(pid int) int {
	procFreeConsole.Call()
	if r, _, err := procAttachConsole.Call(uintptr(pid)); r == 0 {
		fmt.Fprintf(os.Stderr, "AttachConsole(%d): %v", pid, err)
		return consoleCtrlAttachFail
	}
	// Now on the target's console: ignore the event ourselves.
	procSetConsoleCtrlHandler.Call(0, 1)
	// Process group 0 = every process attached to this console.
	if r, _, err := procGenerateConsoleCtrlEvnt.Call(windows.CTRL_C_EVENT, 0); r == 0 {
		fmt.Fprintf(os.Stderr, "GenerateConsoleCtrlEvent: %v", err)
		procFreeConsole.Call()
		return consoleCtrlGenerateErr
	}
	procFreeConsole.Call()
	return consoleCtrlOK
}

// gracefulStopPID asks pid to shut down cleanly (Ctrl-C on its console).
func gracefulStopPID(pid int) error {
	return sendConsoleCtrlC(pid)
}

// waitPIDExit waits up to timeout for pid to exit. It reports true when the
// process is gone. Uses a SYNCHRONIZE handle rather than polling OpenProcess,
// which keeps succeeding while anyone holds a handle to an exited process.
func waitPIDExit(pid int, timeout time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// No such process (or no access to a process that is gone).
		return !PIDAlive(pid)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}
