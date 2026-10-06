//go:build windows

package manager

import (
	"llamactl/pkg/hotswap"
)

// readHandoffState returns the phase and A's PID from handoff-state.json
// (zero values if there is none). It goes through hotswap.ReadHandoffState
// so the file is opened shareably: during a swap A may be replacing it at
// the same moment B's startup sweep reads it.
func readHandoffState(dataDir string) (phase string, aPID int, err error) {
	state, err := hotswap.ReadHandoffState(dataDir)
	if err != nil || state == nil {
		return "", 0, err
	}
	return string(state.Phase), state.APID, nil
}
