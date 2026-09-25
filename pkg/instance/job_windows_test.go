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

func TestNewProcessJobSetsKillOnJobClose(t *testing.T) {
	job, err := newProcessJob()
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

func TestProcessJobKillTreeReapsChild(t *testing.T) {
	// Parent blocks until spawn.txt appears (so we can AssignProcessToJobObject
	// before any long-running work), then runs ping while still in the job.
	job, err := newProcessJob()
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
