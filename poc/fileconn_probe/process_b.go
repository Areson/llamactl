//go:build windows

// B: adopter. Receives a duplicated socket from A, tries each candidate
// wrap strategy, and reports which (if any) work.

package main

import (
	"bufio"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/llamactl/poc-fileconn-probe/ws2"
)

const (
	AF_INET     = int16(2)
	SOCK_STREAM = int16(1)
	IPPROTO_TCP = int16(6)
)

var (
	handoffPort = flag.String("handoff-port", "", "handoff port for B")
)

func runB() {
	handoffPortInt := 0
	fmt.Sscanf(*handoffPort, "%d", &handoffPortInt)
	if handoffPortInt == 0 {
		log.Fatal("B: --handoff-port is required")
	}
	bPID := os.Getpid()
	log.Printf("B: starting pid=%d handoffPort=%d", bPID, handoffPortInt)

	if err := ws2.WSAStartup(0x0202); err != nil {
		log.Fatalf("B: WSAStartup failed: %v", err)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", handoffPortInt))
	if err != nil {
		log.Fatalf("B: listen on handoff port %d failed: %v", handoffPortInt, err)
	}
	defer ln.Close()
	log.Printf("B: listening on handoff port %d", handoffPortInt)

	conn, err := ln.Accept()
	if err != nil {
		log.Fatalf("B: accept failed: %v", err)
	}
	log.Printf("B: accepted handoff connection from %s", conn.RemoteAddr())

	fmt.Fprintf(conn, "READY pid=%d\n", bPID)
	log.Printf("B: sent READY pid=%d", bPID)

	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		parts := strings.SplitN(line, " ", 2)
		msgType := parts[0]

		switch msgType {
		case "DUP":
			if len(parts) < 2 {
				fmt.Fprintf(conn, "GOT err=missing blob\n")
				continue
			}
			blob, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				fmt.Fprintf(conn, "GOT err=decode: %v\n", err)
				continue
			}
			adoptedFD, err := ws2.AdoptSocket(blob, AF_INET, SOCK_STREAM, IPPROTO_TCP)
			if err != nil {
				fmt.Fprintf(conn, "GOT err=adopt: %v\n", err)
				continue
			}
			log.Printf("B: adopted socket %d (raw handle)", adoptedFD)

			// Try each candidate wrap strategy.
			result := tryCandidates(adoptedFD)
			if result.success {
				fmt.Fprintf(conn, "GOT ok strategy=%s detail=%s\n", result.strategy, result.detail)
			} else {
				fmt.Fprintf(conn, "GOT err=strategy=%s detail=%s\n", result.strategy, result.detail)
			}
			syscall.CloseHandle(syscall.Handle(adoptedFD))

		case "BYE":
			fmt.Fprintf(conn, "ACK\n")
			log.Printf("B: BYE received, closing handoff channel")
			conn.Close()
			return

		default:
			log.Printf("B: unexpected message: %s", line)
		}
	}
}

type candidateResult struct {
	success  bool
	strategy string
	detail   string
}

// tryCandidates attempts each wrap strategy in order and returns the first
// that succeeds (or the last failure if none work).
func tryCandidates(fd syscall.Handle) candidateResult {
	strategies := []struct {
		name string
		fn   func(fd syscall.Handle) (net.Conn, *os.File, error)
	}{
		{"baseline", tryBaseline},
		{"setnonblock", trySetNonblock},
		{"completionnotify", tryCompletionNotify},
		{"raw", tryRaw},
	}

	var lastFail candidateResult
	for _, s := range strategies {
		c, f, err := s.fn(fd)
		if err != nil {
			detail := err.Error()
			log.Printf("B: strategy %s FAILED: %s", s.name, detail)
			lastFail = candidateResult{success: false, strategy: s.name, detail: detail}
			if f != nil {
				f.Close()
			}
			continue
		}
		// Read a payload from C (with timeout) to prove the conn is functional.
		payload, readErr := readPayload(c, 3*time.Second)
		if readErr != nil {
			detail := fmt.Sprintf("read failed: %v", readErr)
			log.Printf("B: strategy %s wrap OK but read failed: %s", s.name, detail)
			c.Close()
			if f != nil {
				f.Close()
			}
			lastFail = candidateResult{success: false, strategy: s.name, detail: detail}
			continue
		}
		resp := fmt.Sprintf("RESPONSE-FROM-B pid=%d strategy=%s data=%q\n", os.Getpid(), s.name, payload)
		if _, writeErr := c.Write([]byte(resp)); writeErr != nil {
			detail := fmt.Sprintf("write failed: %v", writeErr)
			log.Printf("B: strategy %s read OK but write failed: %s", s.name, detail)
			c.Close()
			if f != nil {
				f.Close()
			}
			lastFail = candidateResult{success: false, strategy: s.name, detail: detail}
			continue
		}
		log.Printf("B: strategy %s SUCCESS — read %q, wrote response", s.name, payload)
		c.Close()
		if f != nil {
			f.Close()
		}
		return candidateResult{success: true, strategy: s.name, detail: fmt.Sprintf("read+write OK, payload=%q", payload)}
	}
	return lastFail
}

// readPayload reads up to 4096 bytes from c with a timeout.
func readPayload(c net.Conn, timeout time.Duration) (string, error) {
	c.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("EOF")
	}
	return string(buf[:n]), nil
}

// tryBaseline: os.NewFile + net.FileConn (the current product path).
func tryBaseline(fd syscall.Handle) (net.Conn, *os.File, error) {
	f := os.NewFile(uintptr(fd), "adopted-baseline")
	c, err := net.FileConn(f)
	if err != nil {
		return nil, f, fmt.Errorf("net.FileConn: %w", err)
	}
	return c, f, nil
}

// trySetNonblock: toggle nonblocking before FileConn.
func trySetNonblock(fd syscall.Handle) (net.Conn, *os.File, error) {
	if err := syscall.SetNonblock(syscall.Handle(fd), true); err != nil {
		return nil, nil, fmt.Errorf("SetNonblock: %w", err)
	}
	f := os.NewFile(uintptr(fd), "adopted-setnonblock")
	c, err := net.FileConn(f)
	if err != nil {
		return nil, f, fmt.Errorf("net.FileConn after SetNonblock: %w", err)
	}
	return c, f, nil
}

// tryCompletionNotify: set file completion notification modes before FileConn.
// This is a placeholder — the real windows.SetFileCompletionNotificationModes
// call needs the golang.org/x/sys/windows package.
func tryCompletionNotify(fd syscall.Handle) (net.Conn, *os.File, error) {
	f := os.NewFile(uintptr(fd), "adopted-completionnotify")
	c, err := net.FileConn(f)
	if err != nil {
		return nil, f, fmt.Errorf("net.FileConn (completionnotify placeholder): %w", err)
	}
	return c, f, nil
}

// tryRaw: raw recv/send (sanity check — matches poc/socket_handoff).
func tryRaw(fd syscall.Handle) (net.Conn, *os.File, error) {
	c := &rawConn{fd: fd}
	return c, nil, nil
}

type rawConn struct {
	fd syscall.Handle
}

func (r *rawConn) Read(b []byte) (int, error) {
	return ws2.Recv(r.fd, b)
}

func (r *rawConn) Write(b []byte) (int, error) {
	if err := ws2.Send(r.fd, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (r *rawConn) Close() error   { return nil }
func (r *rawConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
}
func (r *rawConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
}
func (r *rawConn) SetDeadline(time.Time) error      { return nil }
func (r *rawConn) SetReadDeadline(time.Time) error  { return nil }
func (r *rawConn) SetWriteDeadline(time.Time) error { return nil }
