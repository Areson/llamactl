//go:build windows

// C: client. Connects to A's dummy server, sends a payload, waits for response.

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"time"
)

var (
	serverPort = flag.String("server-port", "", "server port for C")
)

func runC() {
	port := 0
	fmt.Sscanf(*serverPort, "%d", &port)
	if port == 0 {
		log.Fatal("C: --server-port is required")
	}
	log.Printf("C: starting, connecting to 127.0.0.1:%d", port)

	// Retry connect for up to 10s (A may not have accepted yet).
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if conn == nil {
		log.Fatalf("C: failed to connect to 127.0.0.1:%d within 10s: %v", port, err)
	}
	defer conn.Close()
	log.Printf("C: connected to 127.0.0.1:%d", port)

	// Send payload.
	payload := "HELLO-FROM-C pid=...\n"
	if _, err := conn.Write([]byte(payload)); err != nil {
		log.Fatalf("C: failed to send payload: %v", err)
	}
	log.Printf("C: sent payload: %q", payload)

	// Wait for response (with timeout).
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		log.Fatalf("C: failed to read response: %v", err)
	}
	resp := string(buf[:n])
	log.Printf("C: received response: %q", resp)

	if n > 0 {
		log.Printf("C: PASS — got a response from the adopted socket")
	} else {
		log.Printf("C: FAIL — empty response")
	}
}
