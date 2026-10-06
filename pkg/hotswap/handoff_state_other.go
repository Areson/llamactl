//go:build !windows

package hotswap

import "os"

// readFileShared reads path. Renames over an open file already succeed on
// non-Windows platforms.
func readFileShared(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// replaceFile renames src over dst.
func replaceFile(src, dst string) error {
	return os.Rename(src, dst)
}

// isRenameContention is always false off Windows.
func isRenameContention(err error) bool {
	return false
}
