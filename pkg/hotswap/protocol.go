package hotswap

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// Handoff protocol messages (line-based over a dedicated TCP port).
//
// A (old process) → B (new process):
//   READY  pid=<B_PID>         — B is listening and ready
//   DUP    <base64_blob>        — A is handing off a socket
//   BYE                              — A is done, B should close the port
//
// B (new process) → A (old process):
//   GOT  ok                       — B successfully adopted the socket
//   GOT  err=<msg>               — B failed to adopt the socket
//   ACK                              — B received BYE and closed the port

// SocketInfo represents a single socket being handed off.
type SocketInfo struct {
	FD         int    // The socket FD in A's address space
	Blob       []byte // The WSAPROTOCOL_INFO blob (base64-encoded on the wire)
}

// HandoffChannel wraps the TCP connection between A and B.
type HandoffChannel struct {
	conn    net.Conn
	reader  *bufio.Reader
	writer  io.Writer
}

// NewHandoffChannelFromConn wraps an existing TCP connection.
func NewHandoffChannelFromConn(conn net.Conn) *HandoffChannel {
	return &HandoffChannel{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: conn,
	}
}

// SendReady tells B that A is ready to hand off sockets.
func (h *HandoffChannel) SendReady(pid int) error {
	_, err := fmt.Fprintf(h.writer, "READY pid=%d\n", pid)
	return err
}

// SendDUP sends a socket handoff message.
func (h *HandoffChannel) SendDUP(blob []byte) error {
	encoded := base64.StdEncoding.EncodeToString(blob)
	_, err := fmt.Fprintf(h.writer, "DUP %s\n", encoded)
	return err
}

// SendBYE tells B that A is done.
func (h *HandoffChannel) SendBYE() error {
	_, err := fmt.Fprintf(h.writer, "BYE\n")
	return err
}

// ReadLine reads a single line from the channel.
func (h *HandoffChannel) ReadLine() (string, error) {
	line, err := h.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ParseMessage parses a protocol line into a message type and payload.
func ParseMessage(line string) (msgType string, payload string, err error) {
	parts := strings.SplitN(line, " ", 2)
	msgType = parts[0]
	if len(parts) > 1 {
		payload = parts[1]
	}
	return
}

// ParseReady parses a READY message.
func ParseReady(line string) (pid int, err error) {
	msgType, payload, err := ParseMessage(line)
	if err != nil {
		return 0, err
	}
	if msgType != "READY" {
		return 0, fmt.Errorf("expected READY, got %s", msgType)
	}
	// payload: "pid=<B_PID>"
	pidStr := strings.TrimPrefix(payload, "pid=")
	pid, err = strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse PID from READY: %w", err)
	}
	return pid, nil
}

// ParseDUP parses a DUP message and returns the decoded blob.
func ParseDUP(line string) (blob []byte, err error) {
	msgType, payload, err := ParseMessage(line)
	if err != nil {
		return nil, err
	}
	if msgType != "DUP" {
		return nil, fmt.Errorf("expected DUP, got %s", msgType)
	}
	blob, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode DUP blob: %w", err)
	}
	return blob, nil
}

// ParseGOT parses a GOT message.
func ParseGOT(line string) (ok bool, errMsg string, err error) {
	msgType, payload, err := ParseMessage(line)
	if err != nil {
		return false, "", err
	}
	if msgType != "GOT" {
		return false, "", fmt.Errorf("expected GOT, got %s", msgType)
	}
	if payload == "ok" {
		return true, "", nil
	}
	if strings.HasPrefix(payload, "err=") {
		return false, strings.TrimPrefix(payload, "err="), nil
	}
	return false, payload, nil
}

// ParseACK verifies an ACK message.
func ParseACK(line string) error {
	msgType, _, err := ParseMessage(line)
	if err != nil {
		return err
	}
	if msgType != "ACK" {
		return fmt.Errorf("expected ACK, got %s", msgType)
	}
	return nil
}
