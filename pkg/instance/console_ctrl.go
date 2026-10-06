package instance

// ConsoleCtrlHelperArg is the argv[1] that makes the llamactl binary act as
// the Windows console Ctrl-C helper instead of starting the server:
//
//	llamactl.exe __console-ctrl <pid>
//
// The helper attaches to the target's console and raises CTRL_C_EVENT on it.
// It must be a separate, console-less process: AttachConsole fails while the
// caller still has a console, and a process can only be attached to one
// console at a time, so llamactl cannot do this in-process for concurrent
// stops. See docs/windows-clean-shutdown.md.
const ConsoleCtrlHelperArg = "__console-ctrl"

// Helper exit codes, mapped back to errors by sendConsoleCtrlC.
const (
	consoleCtrlOK          = 0
	consoleCtrlUsage       = 1
	consoleCtrlAttachFail  = 2
	consoleCtrlGenerateErr = 3
)
