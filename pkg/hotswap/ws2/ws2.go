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

const (
	// WSA_FLAG_OVERLAPPED is required for sockets that Go's net poller will use.
	WSA_FLAG_OVERLAPPED = 0x01

	// InfoSize is sizeof(WSAPROTOCOL_INFOW) on 64-bit Windows (624 bytes).
	// WSADuplicateSocketW takes no length; undersizing this overruns the heap.
	InfoSize = 624
)

var (
	ws2Dll         = syscall.NewLazyDLL("ws2_32.dll")
	procWSAStartup = ws2Dll.NewProc("WSAStartup")
	procWSACleanup = ws2Dll.NewProc("WSACleanup")
	procWSADupSock = ws2Dll.NewProc("WSADuplicateSocketW")
	procWSASocket  = ws2Dll.NewProc("WSASocketW")
	procWSAGetLast = ws2Dll.NewProc("WSAGetLastError")
	procRecv       = ws2Dll.NewProc("recv")
	procSend       = ws2Dll.NewProc("send")
	procAccept     = ws2Dll.NewProc("accept")
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

// WSACleanup releases the Winsock library. Do not call this while Go's net
// package still owns sockets in the process.
func WSACleanup() {
	procWSACleanup.Call()
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

// AdoptSocket creates an overlapped socket descriptor from a WSAPROTOCOL_INFOW
// blob produced by DuplicateSocket. The caller must have called WSAStartup
// (Go's net package also does this on init).
func AdoptSocket(buf []byte, family, sockType, proto int16) (syscall.Handle, error) {
	return adoptSocketFlags(buf, family, sockType, proto, WSA_FLAG_OVERLAPPED)
}

// AdoptSocketBlocking imports a socket without WSA_FLAG_OVERLAPPED.
// Useful for listener sockets where Go's netpoller isn't needed.
func AdoptSocketBlocking(buf []byte, family, sockType, proto int16) (syscall.Handle, error) {
	return adoptSocketFlags(buf, family, sockType, proto, 0)
}

func adoptSocketFlags(buf []byte, family, sockType, proto int16, flags uint32) (syscall.Handle, error) {
	if len(buf) < InfoSize {
		return 0, fmt.Errorf("WSASocket(adopt) blob too small: %d < %d", len(buf), InfoSize)
	}
	r1, _, _ := procWSASocket.Call(
		uintptr(family),
		uintptr(sockType),
		uintptr(proto),
		uintptr(unsafe.Pointer(&buf[0])),
		0,
		uintptr(flags),
	)
	if r1 == ^uintptr(0) || r1 == 0xFFFFFFFFFFFFFFFF {
		return 0, fmt.Errorf("WSASocket(adopt) failed: WSA error %d", WSAError())
	}
	return syscall.Handle(r1), nil
}

// Close closes a socket.
func Close(s syscall.Handle) error {
	r, _, _ := procCloseSock.Call(uintptr(s))
	if r != 0 {
		return fmt.Errorf("closesocket: WSA error %d", WSAError())
	}
	return nil
}

// Accept calls accept() on a listening socket and returns the new client socket.
func Accept(lsock syscall.Handle) (syscall.Handle, error) {
	var addr [128]byte
	var addrLen int32 = 16
	r1, _, _ := procAccept.Call(
		uintptr(lsock),
		uintptr(unsafe.Pointer(&addr[0])),
		uintptr(unsafe.Pointer(&addrLen)),
		0,
	)
	cs := syscall.Handle(r1)
	if cs == syscall.Handle(0xFFFFFFFF) || cs == syscall.Handle(^uintptr(0)) || cs == 0 {
		return 0, fmt.Errorf("accept: WSA error %d", WSAError())
	}
	return cs, nil
}

// Recv reads up to len(buf) bytes from the socket.
func Recv(s syscall.Handle, buf []byte) (int, error) {
	r1, _, err := procRecv.Call(
		uintptr(s),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)
	n := int(r1)
	if n < 0 {
		return 0, fmt.Errorf("recv: %v (WSA %d)", err, WSAError())
	}
	return n, nil
}

// Send writes len(data) bytes to the socket.
func Send(s syscall.Handle, data []byte) error {
	r1, _, err := procSend.Call(
		uintptr(s),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		0,
	)
	if r1 == ^uintptr(0) || (int(r1) < 0 && err != nil) {
		return fmt.Errorf("send: %v (WSA %d)", err, WSAError())
	}
	return nil
}
