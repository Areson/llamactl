//go:build windows

// poc: test whether pre-setting FILE_SKIP_SET_EVENT_ON_HANDLE on a
// WSADuplicateSocket-imported socket allows net.FileConn to succeed.
package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
	

	"drain_probe/ws2"
)

func presetTest() {
	if err := ws2.WSAStartup(0x0202); err != nil {
		fatal("WSAStartup: %v", err)
	}
	defer ws2.WSACleanup()

	// Create a listener.
	ln, err := net.Listen("tcp", "127.0.0.1:18181")
	if err != nil {
		fatal("listen: %v", err)
	}
	defer ln.Close()
	fmt.Printf("listener: %s\n", ln.Addr())

	tcpLn := ln.(*net.TCPListener)
	raw, _ := tcpLn.SyscallConn()
	var sock syscall.Handle
	raw.Control(func(h uintptr) { sock = syscall.Handle(h) })

	// Duplicate to our own PID (self-dup for testing).
	pid := uint32(os.Getpid())
	blob, err := ws2.DuplicateSocket(sock, pid)
	if err != nil {
		fatal("DuplicateSocket: %v", err)
	}
	fmt.Printf("blob: %d bytes\n", len(blob))

	// Import (blocking).
	imported, err := ws2.AdoptSocketBlocking(blob, 2, 1, 6)
	if err != nil {
		fatal("AdoptSocketBlocking: %v", err)
	}
	fmt.Printf("imported: handle=%v\n", imported)

	// Pre-set FILE_SKIP_SET_EVENT_ON_HANDLE.
	const FILE_SKIP_SET_EVENT_ON_HANDLE = 0x1
	err = syscall.SetFileCompletionNotificationModes(imported, FILE_SKIP_SET_EVENT_ON_HANDLE)
	if err != nil {
		fmt.Printf("SetFileCompletionNotificationModes: %v (continuing)\n", err)
	} else {
		fmt.Println("SetFileCompletionNotificationModes: OK")
	}

	// Now try net.FileConn.
	f := os.NewFile(uintptr(imported), "test-imported")
	defer f.Close()
	conn, err := net.FileConn(f)
	if err != nil {
		fmt.Printf("net.FileConn FAILED: %v\n", err)
	} else {
		fmt.Printf("net.FileConn SUCCEEDED: %T, addr=%s\n", conn, conn.LocalAddr())
		conn.Close()
	}

	// Also try net.FileListener.
	imported2, err := ws2.AdoptSocketBlocking(blob, 2, 1, 6)
	if err != nil {
		fatal("AdoptSocketBlocking2: %v", err)
	}
	err = syscall.SetFileCompletionNotificationModes(imported2, FILE_SKIP_SET_EVENT_ON_HANDLE)
	if err != nil {
		fmt.Printf("SetFileCompletionNotificationModes2: %v (continuing)\n", err)
	}
	f2 := os.NewFile(uintptr(imported2), "test-imported2")
	defer f2.Close()
	ln2, err := net.FileListener(f2)
	if err != nil {
		fmt.Printf("net.FileListener FAILED: %v\n", err)
	} else {
		fmt.Printf("net.FileListener SUCCEEDED: %T, addr=%s\n", ln2, ln2.Addr())
		ln2.Close()
	}
}



