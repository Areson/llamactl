//go:build !windows

package instance

import "testing"

func TestNewProcessJobNoopOnUnix(t *testing.T) {
	job, err := newProcessJob("test")
	if err != nil {
		t.Fatalf("newProcessJob: %v", err)
	}
	if job != nil {
		t.Fatalf("expected nil job on Unix, got %#v", job)
	}
}
