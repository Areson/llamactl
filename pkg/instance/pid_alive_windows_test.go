//go:build windows

package instance

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// An exited process stays openable while any handle to it is open (the
// llamactl supervisor holds one to each generation). PIDAlive must report
// it dead anyway — after a live hot-swap, B's cleanup saw the exited A as
// "still alive after 60s" and left llamactl.exe.old behind.
func TestPIDAlive_ExitedProcessWithOpenHandle(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test binary with a filter that matches nothing exits immediately.
	cmd := exec.Command(exe, "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	// Hold our own handle, like the supervisor's Process object.
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	if _, err := cmd.Process.Wait(); err != nil {
		t.Fatal(err)
	}
	if ev, _ := windows.WaitForSingleObject(h, 5000); ev != windows.WAIT_OBJECT_0 {
		t.Fatal("child did not exit")
	}

	if PIDAlive(pid) {
		t.Fatal("PIDAlive reported an exited process (with an open handle) as alive")
	}
}

func TestPIDAlive_RunningAndBogus(t *testing.T) {
	if !PIDAlive(os.Getpid()) {
		t.Error("PIDAlive(self) = false")
	}
	if PIDAlive(0) || PIDAlive(-1) {
		t.Error("PIDAlive(<=0) = true")
	}
	cmd := startTestCtrlChildForAlive(t)
	if !PIDAlive(cmd.Process.Pid) {
		t.Error("PIDAlive(running child) = false")
	}
}

// startTestCtrlChildForAlive starts a long-lived child (the Ctrl-C stand-in
// from console_ctrl_windows_test.go) and kills it on cleanup.
func startTestCtrlChildForAlive(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd, _ := startTestCtrlChild(t, setProcAttrs)
	time.Sleep(50 * time.Millisecond)
	return cmd
}
