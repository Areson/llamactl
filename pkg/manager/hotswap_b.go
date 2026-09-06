//go:build windows

package manager

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"llamactl/pkg/hotswap"
)

// HotSwapB is the B-side of the hot-swap: receive client sockets from A,
// then return so main can rebind the public port and serve.
func (im *instanceManager) HotSwapB() error {
	dataDir := im.globalConfig.DataDir

	handoffPort := 0
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--handoff-port=") {
			handoffPort, _ = strconv.Atoi(strings.TrimPrefix(arg, "--handoff-port="))
			break
		}
	}
	if handoffPort == 0 {
		return fmt.Errorf("--handoff-port not specified")
	}

	if err := hotswap.BSide(hotswap.BSideOptions{
		HandoffPort: handoffPort,
		DataDir:     dataDir,
	}); err != nil {
		return err
	}

	// Clean up old binary if A provided its PID.
	if state, err := hotswap.ReadHandoffState(dataDir); err == nil && state != nil && state.APID > 0 {
		go im.cleanupOldBinary(state.APID)
	}

	return nil
}
