//go:build windows

package instance

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processJob wraps a Windows Job Object used to track a backend process tree.
//
// Applied to ALL backends spawned via process.start() on Windows (shared path),
// not only tabby_api — any backend that forks/re-execs (Tabby venv Python,
// uvicorn workers, etc.) can leave orphans that TerminateProcess on the root
// PID does not reach.
//
// Design (ported from the Path B C launcher intent):
//   - Anonymous job (NULL name) so concurrent instances do not share a job.
//   - JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so the last handle close kills members.
//   - No BREAKAWAY_OK / SILENT_BREAKAWAY_OK: children of members stay in the job
//     (backends must not pass CREATE_BREAKAWAY_FROM_JOB).
//   - Handle is inheritable and passed via AdditionalInheritedHandles so a
//     hot-swap (A exits, B adopts) does not kill the tree: the child still holds
//     a job handle after A's handles are closed on process exit. Intentional
//     stop uses TerminateJobObject — CloseHandle alone would not kill while the
//     child still holds a duplicate.
//
// Race: Go's os/exec closes the primary thread handle inside StartProcess, so
// CREATE_SUSPENDED + ResumeThread (as in the C launcher) is not available via
// stock os/exec. We AssignProcessToJobObject immediately after cmd.Start().
// Tabby re-exec / uvicorn workers run after interpreter startup, so the window
// is small; any grandchild created before assign would not join the job.
type processJob struct {
	handle windows.Handle
}

// newProcessJob creates and configures a kill-tree job. The handle is
// inheritable so it can be passed to the child for hot-swap survival.
func newProcessJob() (*processJob, error) {
	sa := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	h, err := windows.CreateJobObject(sa, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("SetInformationJobObject(KILL_ON_JOB_CLOSE): %w", err)
	}

	return &processJob{handle: h}, nil
}

// prepareCmd adds the job handle to the child's inherited handle list so the
// child keeps the job alive across a parent (llamactl) exit during hot-swap.
func (j *processJob) prepareCmd(cmd *exec.Cmd) {
	if j == nil || cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(
		cmd.SysProcAttr.AdditionalInheritedHandles,
		syscall.Handle(j.handle),
	)
}

// assign associates an already-started process with the job.
func (j *processJob) assign(proc *os.Process) error {
	if j == nil || proc == nil {
		return nil
	}
	var assignErr error
	err := proc.WithHandle(func(h uintptr) {
		assignErr = windows.AssignProcessToJobObject(j.handle, windows.Handle(h))
	})
	if err != nil {
		return fmt.Errorf("Process.WithHandle: %w", err)
	}
	if assignErr != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", assignErr)
	}
	return nil
}

// killTree terminates every process in the job (root and descendants).
func (j *processJob) killTree() error {
	if j == nil || j.handle == 0 {
		return nil
	}
	if err := windows.TerminateJobObject(j.handle, 1); err != nil {
		return fmt.Errorf("TerminateJobObject: %w", err)
	}
	return nil
}

// close releases the local job handle. With KILL_ON_JOB_CLOSE, if this is the
// last handle the kernel also terminates remaining members.
func (j *processJob) close() {
	if j == nil || j.handle == 0 {
		return
	}
	_ = windows.CloseHandle(j.handle)
	j.handle = 0
}

// limitFlagsForTest returns the job's LimitFlags (test helper).
func (j *processJob) limitFlagsForTest() (uint32, error) {
	if j == nil || j.handle == 0 {
		return 0, fmt.Errorf("nil job")
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	err := windows.QueryInformationJobObject(
		j.handle,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return 0, err
	}
	return info.BasicLimitInformation.LimitFlags, nil
}
