//go:build windows

package manager

import (
	"fmt"
	"log"
	"os"
	"strings"

	"llamactl/pkg/hotswap"
)

// HotSwapB is the B-side of the hot-swap: receive client sockets from A,
// then return so main can rebind the public port and serve.
// Delegates to hotswap.BSide.
func (im *instanceManager) HotSwapB() error {
	dataDir := im.globalConfig.DataDir

	handoffPort := 0
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--handoff-port=") {
			fmt.Sscanf(arg, "--handoff-port=%d", &handoffPort)
			break
		}
	}
	if handoffPort == 0 {
		return fmt.Errorf("--handoff-port not specified")
	}

	al, err := hotswap.BSide(hotswap.BSideOptions{
		HandoffPort: handoffPort,
		DataDir:     dataDir,
	})
	if err != nil {
		return err
	}

	if al != nil {
		// V2: listener handed off — store the AcceptLoop for main to use.
		im.adoptedAcceptLoop = al
	}

	// Clean up old binary if A provided its PID.
	aPID := 0
	if state, err := hotswap.ReadHandoffState(dataDir); err == nil && state != nil {
		aPID = state.APID
	}
	if aPID > 0 {
		go im.cleanupOldBinary(aPID)
	}

	log.Printf("HotSwapB: handoff complete. Public port will be rebound by main.")
	return nil
}
