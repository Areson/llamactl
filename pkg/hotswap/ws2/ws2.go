//go:build windows

// Package ws2 provides minimal Winsock 2 bindings for the hot-swap
// socket-handoff mechanism.
//
// WSADuplicateSocketW writes a WSAPROTOCOL_INFOW blob (624 bytes on amd64).
// The blob is treated as opaque; we never marshal the C struct ourselves.
package ws2

import (
	"fmt"
	"syscall"
	"unsafe"
)

// InfoSize is sizeof(WSAPROTOCOL_INFOW) on 64-bit Windows (624 bytes).
// WSADuplicateSocketW takes no length; undersizing this overruns the heap.
const InfoSize = 624

var (
	ws2Dll         = syscall.NewLazyDLL("ws2_32.dll")
	procWSAStartup = ws2Dll.NewProc("WSAStartup")
	procWSADupSock = ws2Dll.NewProc("WSADuplicateSocketW")
	procWSAGetLast = ws2Dll.NewProc("WSAGetLastError")
	procCloseSock  = ws2Dll.NewProc("closesocket")
)

// WSAStartup initializes Winsock in the calling process.
func WSAStartup(version uint16) error {
	var data [512]byte
	r1, _, _ := procWSAStartup.Call(uintptr(version), uintptr(unsafe.Pointer(&data)))
	if r1 != 0 {
		return fmt.Errorf("WSAStartup(0x%04x) failed: WSA error %d", version, WSAError())
	}
	return nil
}

// WSAError returns the last Winsock error code.
func WSAError() int32 {
	r, _, _ := procWSAGetLast.Call()
	return int32(r)
}

// DuplicateSocket asks the kernel to make socket s available to targetPID.
func DuplicateSocket(s syscall.Handle, targetPID uint32) ([]byte, error) {
	buf := make([]byte, InfoSize)
	r1, _, _ := procWSADupSock.Call(
		uintptr(s),
		uintptr(targetPID),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r1 != 0 {
		return nil, fmt.Errorf("WSADuplicateSocket(s=%v, pid=%d) failed: WSA error %d", s, targetPID, WSAError())
	}
	return buf, nil
}

// Close closes a socket.
func Close(s syscall.Handle) error {
	r, _, _ := procCloseSock.Call(uintptr(s))
	if r != 0 {
		return fmt.Errorf("closesocket: WSA error %d", WSAError())
	}
	return nil
}
