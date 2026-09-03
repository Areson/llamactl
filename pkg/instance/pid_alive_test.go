package instance

import (
	"os"
	"testing"
)

func TestPIDAlive(t *testing.T) {
	// Current process should be alive
	if !PIDAlive(os.Getpid()) {
		t.Error("PIDAlive() should return true for current process")
	}

	// Non-existent PID should not be alive
	if PIDAlive(499999) {
		t.Error("PIDAlive() should return false for non-existent PID")
	}
}

func TestPIDAliveEdgeCases(t *testing.T) {
	// PID 0 should not be alive (it's a special value)
	if PIDAlive(0) {
		t.Error("PIDAlive(0) should return false")
	}

	// Negative PID should not be alive
	if PIDAlive(-1) {
		t.Error("PIDAlive(-1) should return false")
	}
}

func TestPIDAliveMultipleCalls(t *testing.T) {
	// Multiple calls should be consistent
	pid := os.Getpid()
	first := PIDAlive(pid)
	second := PIDAlive(pid)
	third := PIDAlive(pid)

	if first != second || second != third {
		t.Errorf("PIDAlive() should be consistent: %v %v %v", first, second, third)
	}

	if !first {
		t.Error("PIDAlive() should return true for current process")
	}
}
