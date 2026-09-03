// Package hotswap implements the hot-swap mechanism for llamactl:
// replacing the running control plane with a new binary without stopping
// model instances or dropping client connections.
//
// The mechanism uses WSADuplicateSocket (Windows) to hand live TCP
// connections from the old process (A) to the new process (B), with a
// state file on disk as the safety mechanism for partial failures.
package hotswap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HandoffPhase is the lifecycle phase of a hot-swap.
type HandoffPhase string

const (
	PhaseIdle       HandoffPhase = "idle"
	PhaseStarting   HandoffPhase = "starting"
	PhaseInProgress HandoffPhase = "in_progress"
	PhaseComplete   HandoffPhase = "complete"
	PhaseFailed     HandoffPhase = "failed"
)

// HandoffState is the on-disk schema for handoff-state.json.
// Written by A (the old process) and read by B (the new process) on startup.
type HandoffState struct {
	Phase              HandoffPhase `json:"phase"`
	APID               int          `json:"a_pid"`
	BPID               int          `json:"b_pid"`
	HandoffPort        int          `json:"handoff_port"`
	BinaryPath         string       `json:"binary_path"`
	StartedAt          time.Time    `json:"started_at"`
	CompletedAt        *time.Time   `json:"completed_at,omitempty"`
	SocketsHandedOff   int          `json:"sockets_handed_off"`
	TotalSockets       int          `json:"total_sockets"`
	ListenerHandedOff  bool         `json:"listener_handed_off"`
	Error              string       `json:"error,omitempty"`
}

const (
	handoffStateFileName = "handoff-state.json"

	// HandoffTimeout is the maximum time A will wait for B to ACK.
	HandoffTimeout = 30 * time.Second
)

var (
	handoffMu sync.Mutex // Serializes concurrent read/write of handoff-state.json
)

// HandoffStatePath returns the absolute path to handoff-state.json.
func HandoffStatePath(dataDir string) string {
	return filepath.Join(dataDir, handoffStateFileName)
}

// WriteHandoffState atomically writes the handoff state to disk.
func WriteHandoffState(dataDir string, state *HandoffState) error {
	handoffMu.Lock()
	defer handoffMu.Unlock()

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal handoff state: %w", err)
	}

	tmpPath := HandoffStatePath(dataDir) + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp handoff state: %w", err)
	}

	if err := os.Rename(tmpPath, HandoffStatePath(dataDir)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename handoff state: %w", err)
	}

	return nil
}

// ReadHandoffState reads the handoff state from disk.
// Returns nil (no error) if the file does not exist.
func ReadHandoffState(dataDir string) (*HandoffState, error) {
	handoffMu.Lock()
	defer handoffMu.Unlock()

	path := HandoffStatePath(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read handoff state: %w", err)
	}

	var state HandoffState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse handoff state: %w", err)
	}

	return &state, nil
}

// RemoveHandoffState deletes the handoff state file (idempotent).
func RemoveHandoffState(dataDir string) error {
	handoffMu.Lock()
	defer handoffMu.Unlock()

	err := os.Remove(HandoffStatePath(dataDir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove handoff state: %w", err)
	}
	return nil
}
