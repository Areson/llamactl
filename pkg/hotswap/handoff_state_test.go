package hotswap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHandoffStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := &HandoffState{
		Phase:             "in_progress",
		APID:              1234,
		HandoffPort:       54321,
		SocketsHandedOff:  10,
		TotalSockets:      15,
	}

	// Write
	if err := WriteHandoffState(dir, state); err != nil {
		t.Fatalf("WriteHandoffState: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, "handoff-state.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("handoff-state.json not created: %v", err)
	}

	// Read
	got, err := ReadHandoffState(dir)
	if err != nil {
		t.Fatalf("ReadHandoffState: %v", err)
	}
	if got == nil {
		t.Fatal("ReadHandoffState returned nil")
	}
	if got.Phase != "in_progress" {
		t.Errorf("Phase: got %q, want %q", got.Phase, "in_progress")
	}
	if got.APID != 1234 {
		t.Errorf("APID: got %d, want 1234", got.APID)
	}
	if got.HandoffPort != 54321 {
		t.Errorf("HandoffPort: got %d, want 54321", got.HandoffPort)
	}
	if got.SocketsHandedOff != 10 {
		t.Errorf("SocketsHandedOff: got %d, want 10", got.SocketsHandedOff)
	}
	if got.TotalSockets != 15 {
		t.Errorf("TotalSockets: got %d, want 15", got.TotalSockets)
	}

	// Remove
	if err := RemoveHandoffState(dir); err != nil {
		t.Fatalf("RemoveHandoffState: %v", err)
	}

	// Verify gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("handoff-state.json should be removed")
	}
}

func TestHandoffStateMissing(t *testing.T) {
	dir := t.TempDir()

	got, err := ReadHandoffState(dir)
	if err != nil {
		t.Fatalf("ReadHandoffState on missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestHandoffStateCorruptFile(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "handoff-state.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := ReadHandoffState(dir)
	if err == nil {
		t.Error("expected error for corrupt JSON, got nil")
	}
}

func TestRemoveHandoffStateMissing(t *testing.T) {
	dir := t.TempDir()
	if err := RemoveHandoffState(dir); err != nil {
		t.Errorf("RemoveHandoffState on missing: %v", err)
	}
}

func TestHandoffStateAllPhases(t *testing.T) {
	dir := t.TempDir()

	phases := []HandoffPhase{PhaseIdle, PhaseStarting, PhaseInProgress, PhaseComplete, PhaseFailed}
	for _, phase := range phases {
		state := &HandoffState{Phase: phase}
		if err := WriteHandoffState(dir, state); err != nil {
			t.Fatalf("WriteHandoffState(%s): %v", phase, err)
		}

		got, err := ReadHandoffState(dir)
		if err != nil {
			t.Fatalf("ReadHandoffState(%s): %v", phase, err)
		}
		if got.Phase != phase {
			t.Errorf("Phase: got %q, want %q", got.Phase, phase)
		}
	}

	// Cleanup
	if err := RemoveHandoffState(dir); err != nil {
		t.Fatalf("RemoveHandoffState: %v", err)
	}
}
