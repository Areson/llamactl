//go:build windows

package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"llamactl/pkg/hotswap"
)

// HotSwap replaces the running llamactl binary with a new version without
// stopping model instances. Delegates to hotswap.ASide.
func (im *instanceManager) HotSwap(binaryPath string) error {
	if err := im.AcquireSwap(); err != nil {
		return err
	}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			im.ReleaseSwap()
		}
	}()

	var sockSrc hotswap.SocketSource
	if im.connTracker != nil {
		if src, ok := im.connTracker.(hotswap.SocketSource); ok {
			sockSrc = src
		}
	}

	err := hotswap.ASide(hotswap.ASideOptions{
		BinaryPath:   binaryPath,
		DataDir:      im.globalConfig.DataDir,
		ConfigEnv:    withAbsConfigPath(os.Environ()),
		SocketSource: sockSrc,
		DrainTimeout: 5 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("hot-swap failed: %w", err)
	}

	// Release the port so B can rebind. Keep the swap mutex until exit.
	im.CloseListener()
	releaseOnReturn = false
	return nil
}

// withAbsConfigPath copies env, rewriting LLAMACTL_CONFIG_PATH to an absolute
// path so B can load the same file even if cwd differs.
func withAbsConfigPath(env []string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok || !strings.EqualFold(key, "LLAMACTL_CONFIG_PATH") {
			out = append(out, kv)
			continue
		}
		found = true
		if val != "" {
			if abs, err := filepath.Abs(val); err == nil {
				val = abs
			}
		}
		out = append(out, key+"="+val)
	}
	if !found {
		if p := os.Getenv("LLAMACTL_CONFIG_PATH"); p != "" {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			out = append(out, "LLAMACTL_CONFIG_PATH="+p)
		}
	}
	return out
}
