//go:build windows

package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"drain_probe/ws2"
)

func runB() {
	if err := ws2.WSAStartup(0x0202); err != nil {
		log.Fatalf("WSAStartup: %v", err)
	}
	defer ws2.WSACleanup()

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		log.Fatalf("B: no blob on stdin")
	}
	blob, err := fromHex(scanner.Text())
	if err != nil {
		log.Fatalf("B: hex decode: %v", err)
	}
	log.Printf("B: received blob (%d bytes)", len(blob))

	sock, err := ws2.AdoptSocketBlocking(blob, 2, 1, 6)
	if err != nil {
		log.Printf("B: AdoptSocketBlocking failed: %v; trying overlapped", err)
		sock, err = ws2.AdoptSocket(blob, 2, 1, 6)
		if err != nil {
			log.Fatalf("B: AdoptSocket: %v", err)
		}
	}
	log.Printf("B: imported listener socket (handle=%v)", sock)

	fmt.Printf("B ready %d\n", os.Getpid())
	os.Stdout.Sync()
	log.Printf("B: using raw accept/recv/send loop")

	for {
		cs, err := ws2.Accept(sock)
		if err != nil {
			log.Printf("B: accept loop ended: %v", err)
			return
		}
		log.Printf("B: accepted client (sock=%v)", cs)
		go serveRaw(cs)
	}
}

// serveRaw reads one HTTP request from the socket and writes a response.
// No net.FileConn, no http.Server — just recv/send.
func serveRaw(s syscall.Handle) {
	// Read the request headers (up to \r\n\r\n).
	buf := make([]byte, 4096)
	total := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, err := ws2.Recv(s, buf[total:])
		if n > 0 {
			total += n
		}
		if err != nil || total == len(buf) {
			break
		}
		// Check for end of headers.
		if containsBytes(buf[:total], []byte("\r\n\r\n")) {
			break
		}
	}
	if total == 0 {
		return
	}

	requestLine := string(buf[:firstLineEnd(buf[:total])])
	path := extractPath(requestLine)
	log.Printf("B: raw request: %s", requestLine)

	// Dispatch by path.
	body := `{"server":"B"}`
	if path == "/work" {
		time.Sleep(1500 * time.Millisecond)
	}

	resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	respBytes := []byte(resp)
	if err := ws2.Send(s, respBytes); err != nil {
		log.Printf("B: send: %v", err)
	}

	// Close the socket.
	ws2.Close(s)
}

func containsBytes(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func firstLineEnd(b []byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			return i
		}
	}
	return len(b)
}

func extractPath(requestLine string) string {
	// "GET /work HTTP/1.1" → "/work"
	fields := splitN(requestLine, ' ', 3)
	if len(fields) >= 2 {
		return fields[1]
	}
	return "/"
}

func splitN(s string, sep byte, n int) []string {
	var out []string
	start := 0
	for i := 0; i < len(s) && len(out) < n-1; i++ {
		if s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
