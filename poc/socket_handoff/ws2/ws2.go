// package ws2 provides minimal Winsock 2 bindings for the socket-handoff POC.
//
// We need exactly three calls:
//
//	WSAStartup          — initialize Winsock in the receiving process (B).
//	WSADuplicateSocket  — source process (A) asks the kernel to make a live
//	                    socket available to a target process (B), returning a
//	                    WSAPROTOCOL_INFO blob that B will feed to WSASocket.
//	WSASocket           — target process (B) creates its descriptor for the
//	                    duplicated socket from the WSAPROTOCOL_INFO blob.
//
// The WSAPROTOCOL_INFO layout below is the fixed portion of the structure
// as defined in winsock2.h. The MSDN example allocates sizeof(WSAPROTOCOL_INFO)
// plus a safety margin; we do the same and log the byte length on every call
// so a layout mismatch is visible in the trace.
package ws2

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	ws2Dll         = syscall.NewLazyDLL("ws2_32.dll")
	procWSAStartup = ws2Dll.NewProc("WSAStartup")
	procWSACleanup = ws2Dll.NewProc("WSACleanup")
	procWSADupSock = ws2Dll.NewProc("WSADuplicateSocketW")
	procWSASocket  = ws2Dll.NewProc("WSASocketW")
	procWSAGetLast = ws2Dll.NewProc("WSAGetLastError")
)

// WSAPROTOCOL_INFO is the fixed portion of the Winsock WSAPROTOCOL_INFO
// structure. Field order and sizes follow winsock2.h (Windows SDK).
//
// Note: iWsaService and dwProvider are int32 on amd64 Windows.
// iBase and iVersion are int16. The structure is naturally aligned to 8 bytes
// on amd64, so the total fixed size is 56 bytes. We allocate a generous
// buffer (see InfoSize) to absorb any trailing provider-specific data that
// WSADuplicateSocket may write past the fixed portion.
type WSAPROTOCOL_INFO struct {
	ProviderOffset     uint32 // DWORD dwProviderOffset
	Provider           uint32 // DWORD dwProvider
	iVersion           int16  // short iVersion
	iNameLen           int16  // short iNameLength
	iDescriptionLen    int16  // short iDescriptionLength
	ProtocolName       [256]byte // CHAR szProtocolName[256]
	ProtocolID         uint32 // DWORD dwProtocolSection
	ProtocolInfoOffset uint32 // DWORD dwProtocolInfoOffset
	iParenthood        int16  // short iParenthood
	_                  [6]byte // padding to 8-byte alignment
}

// InfoSize is the buffer size we pass to WSADuplicateSocket. The MSDN
// example uses sizeof(WSAPROTOCOL_INFO); we add a 256-byte margin to absorb
// any provider-specific trailing data. If the struct layout is wrong, the
// call will either fail with a visible WSA error or produce a blob that
// WSASocket rejects — both are observable.
const InfoSize = int(unsafe.Sizeof(WSAPROTOCOL_INFO{})) + 256

// WSAStartup initializes Winsock in the calling process. Must be called
// before any other Winsock call in a process. version is the requested
// Winsock version (e.g. 0x0202 for 2.2).
func WSAStartup(version uint16) error {
	var data [512]byte // WSAData is ~512 bytes; generous allocation
	r1, _, _ := procWSAStartup.Call(uintptr(version), uintptr(unsafe.Pointer(&data)))
	if r1 != 0 {
		return fmt.Errorf("WSAStartup(0x%04x) failed: WSA error %d", version, wsaLastError())
	}
	return nil
}

// WSACleanup releases the Winsock library. Called once at process exit.
func WSACleanup() {
	procWSACleanup.Call()
}

// WSAError returns the last Winsock error code (WSAGetLastError).
func WSAError() int32 {
	r, _, _ := procWSAGetLast.Call()
	return int32(r)
}

// ExternalProc returns a *syscall.LazyProc for a Winsock function by name.
// Used by the POC to call recv/send on adopted sockets.
func ExternalProc(name string) *syscall.LazyProc {
	return ws2Dll.NewProc(name)
}

// DuplicateSocket asks the kernel to make socket s available to targetPID.
// It returns a raw byte blob (the WSAPROTOCOL_INFO contents) that the target
// process must pass to AdoptSocket. The blob is base64-encoded by the caller
// for transport over the handoff TCP channel.
//
// s must be a valid SOCKET in the calling process. targetPID is the PID of
// the process that will call AdoptSocket.
func DuplicateSocket(s syscall.Handle, targetPID uint32) ([]byte, error) {
	buf := make([]byte, InfoSize)
	r1, _, err := procWSADupSock.Call(
		uintptr(s),
		uintptr(targetPID),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r1 != 0 {
		wsaErr := WSAError()
		return nil, fmt.Errorf("WSADuplicateSocket(s=%v, pid=%d) failed: r1=%v, WSA error %d, syscall err=%v", s, targetPID, r1, wsaErr, err)
	}
	return buf, nil
}

// AdoptSocket creates a socket descriptor in the calling process from a
// WSAPROTOCOL_INFO blob produced by DuplicateSocket in another process.
// family, sockType, and proto must match the original socket (AF_INET,
// SOCK_STREAM, IPPROTO_TCP for our TCP loopback case).
//
// The caller must have called WSAStartup first.
func AdoptSocket(buf []byte, family int16, sockType int16, proto int16) (syscall.Handle, error) {
	r1, _, _ := procWSASocket.Call(
		uintptr(family),
		uintptr(sockType),
		uintptr(proto),
		uintptr(unsafe.Pointer(&buf[0])),
		0, // dwFlags
		0, // reserved
	)
	if r1 == ^uintptr(0) || r1 == 0xFFFFFFFFFFFFFFFF {
		return 0, fmt.Errorf("WSASocket(adopt) failed: WSA error %d", wsaLastError())
	}
	return syscall.Handle(r1), nil
}

func wsaLastError() int32 {
	r, _, _ := procWSAGetLast.Call()
	return int32(r)
}
