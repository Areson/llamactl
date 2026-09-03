//go:build windows

package manager

import (
	"encoding/json"
	"fmt"
	"os"
)

// readHandoffState is a local helper to avoid importing the hotswap
// package directly in cleanup.go (which is also used by non-hotswap paths).
func readHandoffState(dataDir string) (phase string, aPID int, err error) {
	data, err := os.ReadFile(dataDir + "\\handoff-state.json")
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, nil
		}
		return "", 0, err
	}
	var s struct {
		Phase string `json:"phase"`
		APID  int    `json:"a_pid"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return "", 0, fmt.Errorf("failed to parse handoff state: %w", err)
	}
	return s.Phase, s.APID, nil
}
