package instance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	state := &RuntimeState{
		SchemaVersion: RuntimeStateSchemaVersion,
		PID:           os.Getpid(),
		Port:          8080,
		Generation:    1,
		LogFile:       filepath.Join(dir, "test.log"),
		LogOffset:     12345,
		Adopted:       false,
	}

	// Write
	if err := WriteRuntimeState(dir, state); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, "runtime.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("runtime.json not created: %v", err)
	}

	// Read
	got, err := ReadRuntimeState(dir)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if got == nil {
		t.Fatal("ReadRuntimeState returned nil")
	}
	if got.PID != state.PID {
		t.Errorf("PID: got %d, want %d", got.PID, state.PID)
	}
	if got.Port != state.Port {
		t.Errorf("Port: got %d, want %d", got.Port, state.Port)
	}
	if got.LogOffset != state.LogOffset {
		t.Errorf("LogOffset: got %d, want %d", got.LogOffset, state.LogOffset)
	}
	if got.Adopted != state.Adopted {
		t.Errorf("Adopted: got %v, want %v", got.Adopted, state.Adopted)
	}

	// Remove
	if err := RemoveRuntimeState(dir); err != nil {
		t.Fatalf("RemoveRuntimeState: %v", err)
	}

	// Verify gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("runtime.json should be removed")
	}
}

func TestRuntimeStateMissing(t *testing.T) {
	dir := t.TempDir()

	got, err := ReadRuntimeState(dir)
	if err != nil {
		t.Fatalf("ReadRuntimeState on missing: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestRuntimeStateCorruptFile(t *testing.T) {
	dir := t.TempDir()

	// Write invalid JSON
	path := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Should return error (not panic)
	_, err := ReadRuntimeState(dir)
	if err == nil {
		t.Error("expected error for corrupt JSON, got nil")
	}
}

func TestRemoveRuntimeStateMissing(t *testing.T) {
	dir := t.TempDir()
	// Should not error
	if err := RemoveRuntimeState(dir); err != nil {
		t.Errorf("RemoveRuntimeState on missing: %v", err)
	}
}

func TestUpdateLogOffset(t *testing.T) {
	dir := t.TempDir()
	state := &RuntimeState{
		SchemaVersion: RuntimeStateSchemaVersion,
		PID:           os.Getpid(),
		Port:          8080,
		LogOffset:     1000,
	}

	if err := WriteRuntimeState(dir, state); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}

	// Update offset
	if err := UpdateLogOffset(dir, 2000); err != nil {
		t.Fatalf("UpdateLogOffset: %v", err)
	}

	// Verify
	got, err := ReadRuntimeState(dir)
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if got.LogOffset != 2000 {
		t.Errorf("LogOffset: got %d, want 2000", got.LogOffset)
	}
}
