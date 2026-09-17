// Package main implements the handoff protocol shared by A (old) and B (new).
//
// The protocol is line-based, human-readable, and runs over a dedicated TCP
// port that A picks and passes to B at spawn time.
//
// Message flow:
//
//	B → A:  READY pid=<B_PID>
//	A → B:  DUP <base64(WSAPROTOCOL_INFO)>
//	B → A:  GOT ok | GOT err=<message>
//	  (repeat DUP/GOT for each live socket)
//	A → B:  BYE
//	B → A:  ACK
//
// A exits after receiving ACK. B continues serving on the adopted sockets.

package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// HandoffMessage is one line of the protocol.
type HandoffMessage struct {
	Verb    string // READY, DUP, GOT, SERVE, DONE, BYE, ACK
	PID     uint32 // present on READY
	Payload []byte // present on DUP (the WSAPROTOCOL_INFO blob)
	OK      bool   // present on GOT
	Err     string // present on GOT when OK is false
}

// Encode serializes a HandoffMessage to a single line.
func (m HandoffMessage) Encode() string {
	switch m.Verb {
	case "READY":
		return fmt.Sprintf("READY pid=%d", m.PID)
	case "DUP":
		return "DUP " + base64.StdEncoding.EncodeToString(m.Payload)
	case "GOT":
		if m.OK {
			return "GOT ok"
		}
		return "GOT err=" + m.Err
	case "BYE":
		return "BYE"
	case "ACK":
		return "ACK"
	case "SERVE":
		return "SERVE"
	case "DONE":
		return "DONE"
	default:
		return "UNKNOWN"
	}
}

// Decode parses one line of the protocol.
func Decode(line string) (HandoffMessage, error) {
	line = strings.TrimRight(line, "\r\n")
	parts := strings.SplitN(line, " ", 2)
	verb := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}

	switch verb {
	case "READY":
		// "READY pid=12345"
		if !strings.HasPrefix(rest, "pid=") {
			return HandoffMessage{}, fmt.Errorf("READY missing pid: %q", line)
		}
		pidStr := strings.TrimPrefix(rest, "pid=")
		pid, err := strconv.ParseUint(pidStr, 10, 32)
		if err != nil {
			return HandoffMessage{}, fmt.Errorf("READY bad pid %q: %w", pidStr, err)
		}
		return HandoffMessage{Verb: "READY", PID: uint32(pid)}, nil

	case "DUP":
		// "DUP <base64>"
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			return HandoffMessage{}, fmt.Errorf("DUP bad base64: %w", err)
		}
		return HandoffMessage{Verb: "DUP", Payload: raw}, nil

	case "GOT":
		if rest == "ok" {
			return HandoffMessage{Verb: "GOT", OK: true}, nil
		}
		if strings.HasPrefix(rest, "err=") {
			return HandoffMessage{Verb: "GOT", OK: false, Err: strings.TrimPrefix(rest, "err=")}, nil
		}
		return HandoffMessage{}, fmt.Errorf("GOT unknown form: %q", line)

	case "BYE":
		return HandoffMessage{Verb: "BYE"}, nil
	case "ACK":
		return HandoffMessage{Verb: "ACK"}, nil
	case "SERVE":
		return HandoffMessage{Verb: "SERVE"}, nil
	case "DONE":
		return HandoffMessage{Verb: "DONE"}, nil

	default:
		return HandoffMessage{}, fmt.Errorf("unknown verb %q", verb)
	}
}

// Send writes one message line to the connection.
func Send(conn net.Conn, m HandoffMessage) error {
	_, err := fmt.Fprintf(conn, "%s\n", m.Encode())
	return err
}

// Recv reads one message line from the connection.
// NOTE: creates a new bufio.Reader each call — fine for the POC's low
// message count, but not for high-throughput use.
func Recv(conn net.Conn) (HandoffMessage, error) {
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return HandoffMessage{}, err
	}
	return Decode(line)
}

// HandoffSession is a bidirectional message exchange over a TCP connection.
type HandoffSession struct {
	conn net.Conn
	r    *bufio.Reader
}

// NewHandoffSession wraps a connection for protocol use.
func NewHandoffSession(conn net.Conn) *HandoffSession {
	return &HandoffSession{conn: conn, r: bufio.NewReader(conn)}
}

// Send writes one message and flushes.
func (s *HandoffSession) Send(m HandoffMessage) error {
	_, err := fmt.Fprintf(s.conn, "%s\n", m.Encode())
	return err
}

// Recv reads one message.
func (s *HandoffSession) Recv() (HandoffMessage, error) {
	line, err := s.r.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return HandoffMessage{}, io.EOF
		}
		return HandoffMessage{}, err
	}
	return Decode(line)
}

// Close closes the underlying connection.
func (s *HandoffSession) Close() error {
	return s.conn.Close()
}
