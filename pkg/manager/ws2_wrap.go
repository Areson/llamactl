//go:build windows

package manager

import (
	"syscall"

	"llamactl/pkg/hotswap/ws2"
)

// ws2DuplicateSocket wraps ws2.DuplicateSocket.
func ws2DuplicateSocket(fd syscall.Handle, targetPID uint32) ([]byte, error) {
	return ws2.DuplicateSocket(fd, targetPID)
}
