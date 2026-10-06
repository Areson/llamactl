//go:build windows

package instance

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const testCtrlChildArg = "__test-ctrl-child"

// TestMain lets the test binary double as the console Ctrl-C helper
// (consoleCtrlHelperExe defaults to os.Executable) and as a backend stand-in
// that shuts down cleanly on Ctrl-C.
func TestMain(m *testing.M) {
	if handled, code := RunConsoleCtrlHelper(os.Args); handled {
		os.Exit(code)
	}
	if len(os.Args) == 3 && os.Args[1] == testCtrlChildArg {
		os.Exit(runTestCtrlChild(os.Args[2]))
	}
	os.Exit(m.Run())
}

// runTestCtrlChild writes "ready" to path, then "interrupt" on Ctrl-C and
// exits 0. Exits 9 if no Ctrl-C arrives in time.
func runTestCtrlChild(path string) int {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	if err := os.WriteFile(path, []byte("ready\n"), 0644); err != nil {
		return 8
	}
	select {
	case <-sig:
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString("interrupt\n")
			f.Close()
		}
		return 0
	case <-time.After(30 * time.Second):
		return 9
	}
}

func startTestCtrlChild(t *testing.T, attrs func(*exec.Cmd)) (*exec.Cmd, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "child.txt")
	cmd := exec.Command(exe, testCtrlChildArg, marker)
	attrs(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if b, _ := os.ReadFile(marker); strings.Contains(string(b), "ready") {
			return cmd, marker
		}
		if time.Now().After(deadline) {
			t.Fatal("child never became ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConsoleCtrlC_StopsChildCleanly(t *testing.T) {
	cmd, marker := startTestCtrlChild(t, setProcAttrs)

	if err := signalStop(cmd); err != nil {
		t.Fatalf("signalStop: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exit: %v (want clean exit 0)", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("child did not exit after Ctrl-C")
	}
	if b, _ := os.ReadFile(marker); !strings.Contains(string(b), "interrupt") {
		t.Fatalf("child did not observe os.Interrupt; marker=%q", b)
	}
}

func TestConsoleCtrlC_AdoptedPathWaitsForExit(t *testing.T) {
	cmd, _ := startTestCtrlChild(t, setProcAttrs)
	pid := cmd.Process.Pid
	// Reap in the background as the owning process would; the adopted path
	// itself only knows the PID.
	go func() { _ = cmd.Wait() }()

	if err := gracefulStopPID(pid); err != nil {
		t.Fatalf("gracefulStopPID: %v", err)
	}
	if !waitPIDExit(pid, 15*time.Second) {
		t.Fatal("waitPIDExit: process still running after Ctrl-C")
	}
}

func TestConsoleCtrlC_NoConsoleFailsFast(t *testing.T) {
	// Pre-change spawn flags: DETACHED_PROCESS children have no console.
	cmd, _ := startTestCtrlChild(t, func(c *exec.Cmd) {
		c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	})

	start := time.Now()
	err := sendConsoleCtrlC(cmd.Process.Pid)
	if err == nil {
		t.Fatal("expected an error for a console-less process")
	}
	if !strings.Contains(err.Error(), "no console") {
		t.Fatalf("error = %v, want a no-console error", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("failure took %v; should be immediate so stop goes straight to the hard kill", d)
	}
}

// Guard against the self-re-exec runaway: a binary that never called
// RunConsoleCtrlHelper must not launch itself as the helper.
func TestConsoleCtrlC_RefusesWithoutHelperDispatch(t *testing.T) {
	cmd, _ := startTestCtrlChild(t, setProcAttrs)

	helperDispatched.Store(false)
	defer helperDispatched.Store(true) // TestMain dispatches for this binary

	called := false
	orig := consoleCtrlHelperExe
	consoleCtrlHelperExe = func() (string, error) { called = true; return orig() }
	defer func() { consoleCtrlHelperExe = orig }()

	err := sendConsoleCtrlC(cmd.Process.Pid)
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want helper-unavailable error", err)
	}
	if called {
		t.Fatal("helper executable was resolved/launched despite no dispatch")
	}
	if !PIDAlive(cmd.Process.Pid) {
		t.Fatal("target should be untouched (stop falls back to the hard kill)")
	}
}

func TestWaitPIDExit_TimesOutWhileRunning(t *testing.T) {
	cmd, _ := startTestCtrlChild(t, setProcAttrs)
	if waitPIDExit(cmd.Process.Pid, 300*time.Millisecond) {
		t.Fatal("waitPIDExit reported exit for a running process")
	}
}

func TestRunConsoleCtrlHelper_Args(t *testing.T) {
	if handled, _ := RunConsoleCtrlHelper([]string{"llamactl.exe"}); handled {
		t.Error("no args: should not be handled")
	}
	if handled, _ := RunConsoleCtrlHelper([]string{"llamactl.exe", "--version"}); handled {
		t.Error("--version: should not be handled")
	}
	for _, args := range [][]string{
		{"llamactl.exe", ConsoleCtrlHelperArg},
		{"llamactl.exe", ConsoleCtrlHelperArg, "abc"},
		{"llamactl.exe", ConsoleCtrlHelperArg, "-1"},
		{"llamactl.exe", ConsoleCtrlHelperArg, strconv.Itoa(1), "extra"},
	} {
		if handled, code := RunConsoleCtrlHelper(args); !handled || code != consoleCtrlUsage {
			t.Errorf("%v: handled=%v code=%d, want handled usage error", args, handled, code)
		}
	}
}
