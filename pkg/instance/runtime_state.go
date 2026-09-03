// Package instance provides the runtime state file (runtime.json) for
// model instance adoption across llamactl hot-swaps.
//
// The runtime.json file is written by the owning llamactl (A) when it
// starts a model child, and read by the successor (B) on startup to
// adopt the running child. It is the source of truth for:
//
//   - the child's PID (for liveness checks and stop signals)
//   - the child's port (for /health probes)
//   - the log file path and read offset (for log tail re-attachment)
//   - the generation counter (for stale-monitor detection)
//
// The file is written atomically (write to temp, rename) and removed
// on clean stop. A missing file means the instance was never started
// by this llamactl generation.
package instance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RuntimeState is the on-disk schema for runtime.json.
type RuntimeState struct {
	SchemaVersion int       `json:"schema_version"` // Always 1
	PID           int       `json:"pid"`
	Port          int       `json:"port"`
	StartedAt     time.Time `json:"started_at"`
	Generation    int       `json:"generation"`
	LogFile       string    `json:"log_file"`
	LogOffset     int64     `json:"log_offset"` // Byte offset of last byte read by A
	Adopted       bool      `json:"adopted"`    // True if B adopted this instance
}

const (
	// RuntimeStateSchemaVersion is the current schema version.
	RuntimeStateSchemaVersion = 1

	// runtimeStateFileName is the filename within the instance directory.
	runtimeStateFileName = "runtime.json"
)

var (
	runtimeMu sync.Mutex // Serializes concurrent read/write of runtime.json
)

// RuntimeStatePath returns the absolute path to the runtime.json file
// for the given instance directory.
func RuntimeStatePath(instanceDir string) string {
	return filepath.Join(instanceDir, runtimeStateFileName)
}

// WriteRuntimeState atomically writes the runtime state to disk.
// It writes to a temp file in the same directory, then renames over
// the target. This ensures readers never see a partial write.
func WriteRuntimeState(instanceDir string, state *RuntimeState) error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	if state.SchemaVersion == 0 {
		state.SchemaVersion = RuntimeStateSchemaVersion
	}

	if err := os.MkdirAll(instanceDir, 0755); err != nil {
		return fmt.Errorf("failed to create instance dir %s: %w", instanceDir, err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal runtime state: %w", err)
	}

	// Write to temp file, then rename (atomic on Windows and Unix).
	tmpPath := filepath.Join(instanceDir, runtimeStateFileName+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp runtime state: %w", err)
	}

	finalPath := filepath.Join(instanceDir, runtimeStateFileName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		// Clean up the temp file on failure.
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename runtime state: %w", err)
	}

	return nil
}

// ReadRuntimeState reads and validates the runtime state from disk.
// Returns nil (no error) if the file does not exist — the caller
// should treat this as "not started by this generation."
func ReadRuntimeState(instanceDir string) (*RuntimeState, error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	path := filepath.Join(instanceDir, runtimeStateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Not started; not an error.
		}
		return nil, fmt.Errorf("failed to read runtime state: %w", err)
	}

	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse runtime state: %w", err)
	}

	if state.SchemaVersion != RuntimeStateSchemaVersion {
		return nil, fmt.Errorf("runtime state schema version %d != expected %d", state.SchemaVersion, RuntimeStateSchemaVersion)
	}

	return &state, nil
}

// RemoveRuntimeState deletes the runtime state file.
// Not an error if the file doesn't exist (idempotent).
func RemoveRuntimeState(instanceDir string) error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	path := filepath.Join(instanceDir, runtimeStateFileName)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove runtime state: %w", err)
	}
	return nil
}

// UpdateLogOffset updates the log offset in the runtime state.
// Called periodically by the logger as it reads output.
func UpdateLogOffset(instanceDir string, offset int64) error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()

	state, err := ReadRuntimeStateUnlocked(instanceDir)
	if err != nil {
		return err
	}
	if state == nil {
		return nil // No state file; nothing to update.
	}

	state.LogOffset = offset
	return WriteRuntimeStateUnlocked(instanceDir, state)
}

// ReadRuntimeStateUnlocked is the lock-free variant for use inside
// UpdateLogOffset (which already holds the lock).
func ReadRuntimeStateUnlocked(instanceDir string) (*RuntimeState, error) {
	path := filepath.Join(instanceDir, runtimeStateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read runtime state: %w", err)
	}

	var state RuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse runtime state: %w", err)
	}
	return &state, nil
}

// WriteRuntimeStateUnlocked is the lock-free variant for use inside
// UpdateLogOffset (which already holds the lock).
func WriteRuntimeStateUnlocked(instanceDir string, state *RuntimeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal runtime state: %w", err)
	}

	tmpPath := filepath.Join(instanceDir, runtimeStateFileName+".tmp")
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp runtime state: %w", err)
	}

	finalPath := filepath.Join(instanceDir, runtimeStateFileName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename runtime state: %w", err)
	}
	return nil
}
