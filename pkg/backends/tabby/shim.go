// Package tabby holds the TabbyAPI launch shim (GET /slots) that llamactl
// injects via PYTHONPATH so Tabby's install tree can stay stock.
package tabby

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
)

//go:embed python/sitecustomize.py python/README.md
var shimFS embed.FS

var (
	shimOnce sync.Once
	shimDir  string
	shimErr  error
)

// EnsureShimDir extracts the embedded Python shim into a per-user cache
// directory and returns its absolute path for PYTHONPATH. Safe to call
// concurrently; extraction is once-per-process.
func EnsureShimDir() (string, error) {
	shimOnce.Do(func() {
		shimDir, shimErr = extractShim()
	})
	return shimDir, shimErr
}

// PrependPythonPath puts dir first on a PYTHONPATH-style list, using the
// platform path list separator. Empty inputs are handled cleanly.
func PrependPythonPath(existing, dir string) string {
	if dir == "" {
		return existing
	}
	if existing == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + existing
}

func extractShim() (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		cacheRoot = os.TempDir()
	}

	sum := sha256.New()
	entries := []string{"python/sitecustomize.py", "python/README.md"}
	for _, name := range entries {
		data, readErr := shimFS.ReadFile(name)
		if readErr != nil {
			return "", fmt.Errorf("read embedded %s: %w", name, readErr)
		}
		sum.Write(data)
		sum.Write([]byte{0})
	}
	version := hex.EncodeToString(sum.Sum(nil))[:16]

	dir := filepath.Join(cacheRoot, "llamactl", "tabby-slots-shim", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create shim dir: %w", err)
	}

	for _, name := range entries {
		base := filepath.Base(name)
		dest := filepath.Join(dir, base)
		data, readErr := shimFS.ReadFile(name)
		if readErr != nil {
			return "", readErr
		}
		if sameFile(dest, data) {
			continue
		}
		if writeErr := os.WriteFile(dest, data, 0o644); writeErr != nil {
			return "", fmt.Errorf("write %s: %w", dest, writeErr)
		}
	}

	// Drop older version dirs best-effort (keep current only).
	parent := filepath.Dir(dir)
	_ = filepath.WalkDir(parent, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == parent || path == dir {
			return nil
		}
		if d.IsDir() && filepath.Dir(path) == parent {
			_ = os.RemoveAll(path)
			return fs.SkipDir
		}
		return nil
	})

	return dir, nil
}

func sameFile(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// InjectPythonPath mutates env so a TabbyAPI child loads the /slots shim.
// On extract failure it logs and leaves env unchanged (Tabby still starts).
func InjectPythonPath(env map[string]string) {
	if env == nil {
		return
	}
	dir, err := EnsureShimDir()
	if err != nil {
		log.Printf("tabby /slots shim unavailable: %v", err)
		return
	}
	cur := env["PYTHONPATH"]
	if cur == "" {
		cur = os.Getenv("PYTHONPATH")
	}
	env["PYTHONPATH"] = PrependPythonPath(cur, dir)
}
