//go:build windows

// A: orchestrator. Starts a dummy TCP server, spawns B and C,
// duplicates the connected socket to B, and reports results.

package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/llamactl/poc-fileconn-probe/ws2"
)

func runA() {
	aPID := os.Getpid()
	log.Printf("A: starting pid=%d", aPID)

	if err := ws2.WSAStartup(0x0202); err != nil {
		log.Fatalf("A: WSAStartup failed: %v", err)
	}

	// Dummy TCP server on a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("A: listen failed: %v", err)
	}
	defer ln.Close()
	serverPort := ln.Addr().(*net.TCPAddr).Port
	log.Printf("A: dummy server listening on 127.0.0.1:%d", serverPort)

	// Pick a free handoff port.
	handoffPort := pickFreePort()
	log.Printf("A: handoff port = %d", handoffPort)

	// Spawn B.
	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("A: os.Executable failed: %v", err)
	}
	bCmd := exec.Command(exePath, "--i-am-b", fmt.Sprintf("--handoff-port=%d", handoffPort))
	bCmd.Stdout = os.Stdout
	bCmd.Stderr = os.Stderr
	if err := bCmd.Start(); err != nil {
		log.Fatalf("A: failed to spawn B: %v", err)
	}
	defer bCmd.Process.Kill()
	log.Printf("A: spawned B (PID %d)", bCmd.Process.Pid)

	// Wait for B to be ready on the handoff port.
	var handoffConn net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		handoffConn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", handoffPort), 500*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if handoffConn == nil {
		log.Fatalf("A: B did not open handoff port %d within 10s", handoffPort)
	}
	defer handoffConn.Close()
	log.Printf("A: connected to B's handoff port")

	// Read READY from B.
	reader := bufio.NewReader(handoffConn)
	readyLine, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("A: failed to read READY: %v", err)
	}
	log.Printf("A: B says: %s", strings.TrimSpace(readyLine))

	// Spawn C first (it retries connect for up to 10s).
	cCmd := exec.Command(exePath, "--i-am-c", fmt.Sprintf("--server-port=%d", serverPort))
	cCmd.Stdout = os.Stdout
	cCmd.Stderr = os.Stderr
	if err := cCmd.Start(); err != nil {
		log.Fatalf("A: failed to spawn C: %v", err)
	}
	defer cCmd.Process.Kill()
	log.Printf("A: spawned C (PID %d)", cCmd.Process.Pid)

	// Accept the client connection (C is already trying to connect).
	// No deadline — C retries for up to 10s, so Accept will eventually succeed.
	log.Printf("A: about to accept client connection")
	clientConn, err := ln.Accept()
	if err != nil {
		log.Fatalf("A: accept failed: %v", err)
	}
	log.Printf("A: accepted client from %s", clientConn.RemoteAddr())

	// Get the client socket handle via TCPConn.File() (Winsock SOCKET on Windows).
	tcpConn := clientConn.(*net.TCPConn)
	f, err := tcpConn.File()
	if err != nil {
		log.Fatalf("A: TCPConn.File failed: %v", err)
	}
	defer f.Close()
	socketHandle := syscall.Handle(f.Fd())
	log.Printf("A: client socket handle = %d", socketHandle)

	// Duplicate the socket to B.
	bPID := uint32(bCmd.Process.Pid)
	blob, err := ws2.DuplicateSocket(socketHandle, bPID)
	if err != nil {
		log.Fatalf("A: DuplicateSocket failed: %v", err)
	}
	log.Printf("A: WSADuplicateSocket OK, blob size = %d bytes", len(blob))

	// Send DUP to B.
	b64 := base64.StdEncoding.EncodeToString(blob)
	fmt.Fprintf(handoffConn, "DUP %s\n", b64)
	log.Printf("A: sent DUP to B")

	// Read GOT from B.
	gotLine, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("A: failed to read GOT: %v", err)
	}
	gotLine = strings.TrimSpace(gotLine)
	log.Printf("A: B says: %s", gotLine)

	if strings.HasPrefix(gotLine, "GOT ok") {
		log.Printf("A: SUCCESS — B adopted the socket and served the client")
	} else {
		log.Printf("A: FAIL — %s", gotLine)
	}

	// Send BYE.
	fmt.Fprintf(handoffConn, "BYE\n")
	ackLine, err := reader.ReadString('\n')
	if err == nil {
		log.Printf("A: B says: %s", strings.TrimSpace(ackLine))
	}

	// Wait for C to finish.
	cCmd.Wait()
	bCmd.Wait()

	log.Printf("A: done")
}

func pickFreePort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("A: pickFreePort failed: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
