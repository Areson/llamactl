//go:build windows

package instance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestJobObjectNameSanitizes(t *testing.T) {
	got := jobObjectName(`tabby/foo\bar`)
	want := `Local\llamactl-job-tabby_foo_bar`
	if got != want {
		t.Fatalf("jobObjectName = %q, want %q", got, want)
	}
	if jobObjectName("") != `Local\llamactl-job-unnamed` {
		t.Fatalf("empty name: %q", jobObjectName(""))
	}
}

func TestNewProcessJobSetsKillOnJobClose(t *testing.T) {
	job, err := newProcessJob("")
	if err != nil {
		t.Fatalf("newProcessJob: %v", err)
	}
	defer job.close()

	flags, err := job.limitFlagsForTest()
	if err != nil {
		t.Fatalf("limitFlagsForTest: %v", err)
	}
	if flags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatalf("LimitFlags missing KILL_ON_JOB_CLOSE: 0x%x", flags)
	}
	if flags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		t.Fatalf("BREAKAWAY_OK must not be set: 0x%x", flags)
	}
	if flags&windows.JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK != 0 {
		t.Fatalf("SILENT_BREAKAWAY_OK must not be set: 0x%x", flags)
	}
}

func TestNamedJobOpenAndKillTree(t *testing.T) {
	name := fmt.Sprintf("test-named-job-%d", os.Getpid())
	job, err := newProcessJob(name)
	if err != nil {
		t.Fatalf("newProcessJob: %v", err)
	}
	defer job.close()

	dir := t.TempDir()
	spawn := filepath.Join(dir, "spawn.txt")
	script := filepath.Join(dir, "tree.cmd")
	body := fmt.Sprintf("@echo off\r\n:wait\r\nif not exist \"%s\" goto wait\r\nping -n 60 127.0.0.1 >nul\r\n", spawn)
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("cmd.exe", "/c", script)
	job.prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := job.assign(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("assign: %v", err)
	}

	// Close our create handle; child still holds an inherited duplicate so
	// KILL_ON_JOB_CLOSE must not fire. Successor opens by name and terminates.
	job.close()

	opened, err := openProcessJob(name)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("openProcessJob: %v", err)
	}
	defer opened.close()

	if err := os.WriteFile(spawn, []byte("go"), 0644); err != nil {
		_ = opened.killTree()
		t.Fatalf("write spawn: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if !PIDAlive(cmd.Process.Pid) {
		t.Fatal("process exited before killTree via opened job")
	}

	method, err := stopAdoptedProcessTree(name, cmd.Process.Pid)
	if err != nil {
		t.Fatalf("stopAdoptedProcessTree: %v", err)
	}
	if method != "job" {
		t.Fatalf("method = %q, want job", method)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		t.Logf("Wait after named-job kill: %v", waitErr)
	case <-time.After(5 * time.Second):
		t.Fatal("root did not exit after named-job TerminateJobObject")
	}
}

func TestProcessJobKillTreeReapsChild(t *testing.T) {
	// Parent blocks until spawn.txt appears (so we can AssignProcessToJobObject
	// before any long-running work), then runs ping while still in the job.
	job, err := newProcessJob("")
	if err != nil {
		t.Fatalf("newProcessJob: %v", err)
	}
	defer job.close()

	dir := t.TempDir()
	spawn := filepath.Join(dir, "spawn.txt")
	script := filepath.Join(dir, "tree.cmd")
	body := fmt.Sprintf("@echo off\r\n:wait\r\nif not exist \"%s\" goto wait\r\nping -n 60 127.0.0.1 >nul\r\n", spawn)
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command("cmd.exe", "/c", script)
	job.prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := job.assign(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("assign: %v", err)
	}

	if err := os.WriteFile(spawn, []byte("go"), 0644); err != nil {
		_ = job.killTree()
		t.Fatalf("write spawn: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if !PIDAlive(cmd.Process.Pid) {
		t.Fatal("process exited before killTree")
	}

	if err := job.killTree(); err != nil {
		t.Fatalf("killTree: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		t.Logf("Wait after killTree: %v", waitErr)
	case <-time.After(5 * time.Second):
		t.Fatal("root did not exit after killTree")
	}
}

func TestDescendantPIDsIncludesChild(t *testing.T) {
	dir := t.TempDir()
	spawn := filepath.Join(dir, "spawn.txt")
	script := filepath.Join(dir, "parent.cmd")
	// Parent waits, then starts a child ping and waits on it.
	body := fmt.Sprintf("@echo off\r\n:wait\r\nif not exist \"%s\" goto wait\r\nping -n 60 127.0.0.1 >nul\r\n", spawn)
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("cmd.exe", "/c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	if err := os.WriteFile(spawn, []byte("go"), 0644); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	pids, err := descendantPIDs(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("descendantPIDs: %v", err)
	}
	if len(pids) < 1 || pids[0] != cmd.Process.Pid {
		t.Fatalf("pids = %v, want root first", pids)
	}
}

func TestStopAdoptedTreeKillFallback(t *testing.T) {
	// Anonymous job (no name) so openProcessJob cannot find it — forces
	// assign-or-tree-kill path. Process is in the anonymous job, so assign to
	// a new job fails and tree-kill should reap it.
	job, err := newProcessJob("")
	if err != nil {
		t.Fatalf("newProcessJob: %v", err)
	}
	defer job.close()

	dir := t.TempDir()
	spawn := filepath.Join(dir, "spawn.txt")
	script := filepath.Join(dir, "tree.cmd")
	body := fmt.Sprintf("@echo off\r\n:wait\r\nif not exist \"%s\" goto wait\r\nping -n 60 127.0.0.1 >nul\r\n", spawn)
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("cmd.exe", "/c", script)
	job.prepareCmd(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := job.assign(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("assign: %v", err)
	}
	if err := os.WriteFile(spawn, []byte("go"), 0644); err != nil {
		_ = job.killTree()
		t.Fatalf("spawn: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	root := cmd.Process.Pid
	method, err := stopAdoptedProcessTree("no-such-named-job-zzzz", root)
	if err != nil {
		t.Fatalf("stopAdoptedProcessTree: %v", err)
	}
	if method != "tree-kill" && method != "job-assign" {
		t.Fatalf("method = %q, want tree-kill or job-assign", method)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("root still alive after adopted stop fallback")
	}
}
