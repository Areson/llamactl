//go:build !windows

package instance

import (
	"os"
	"os/exec"
)

// processJob is a no-op stub on non-Windows. Unix process-group kill remains
// Setpgid + signalStop (see process_group_unix.go); this change does not add
// cgroup work.
type processJob struct{}

func newProcessJob() (*processJob, error) { return nil, nil }

func (j *processJob) prepareCmd(cmd *exec.Cmd) {}

func (j *processJob) assign(proc *os.Process) error { return nil }

func (j *processJob) killTree() error { return nil }

func (j *processJob) close() {}
