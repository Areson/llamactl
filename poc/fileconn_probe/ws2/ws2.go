//go:build windows

// ws2: minimal Winsock 2 bindings for the fileconn_probe POC.
// Reuses the same approach as pkg/hotswap/ws2/ws2.go.

package ws2

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	WSA_FLAG_OVERLAPPED = 0x01
	InfoSize            = 624
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
)

func WSAStartup(version uint16) error {
	var data [512]byte
	r1, _, _ := procWSAStartup.Call(uintptr(version), uintptr(unsafe.Pointer(&data)))
	if r1 != 0 {
		return fmt.Errorf("WSAStartup(0x%04x) failed: WSA error %d", version, WSAError())
	}
	return nil
}

func WSACleanup() {
	procWSACleanup.Call()
}

func WSAError() int32 {
	r, _, _ := procWSAGetLast.Call()
	return int32(r)
}

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

func AdoptSocket(buf []byte, family, sockType, proto int16) (syscall.Handle, error) {
	if len(buf) < InfoSize {
		return 0, fmt.Errorf("WSASocket(adopt) blob too small: %d < %d", len(buf), InfoSize)
	}
	r1, _, _ := procWSASocket.Call(
		uintptr(family),
		uintptr(sockType),
		uintptr(proto),
		uintptr(unsafe.Pointer(&buf[0])),
		0,
		uintptr(WSA_FLAG_OVERLAPPED),
	)
	if r1 == ^uintptr(0) || r1 == 0xFFFFFFFFFFFFFFFF {
		return 0, fmt.Errorf("WSASocket(adopt) failed: WSA error %d", WSAError())
	}
	return syscall.Handle(r1), nil
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
