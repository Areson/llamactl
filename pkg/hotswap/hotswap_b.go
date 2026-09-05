//go:build windows

// Package hotswap: B-side orchestration.
// Moved from pkg/manager/hotswap_b.go.

package hotswap

import (
	"fmt"
	"log"
	"net"
	"os"

	"llamactl/pkg/hotswap/ws2"
)

// BSideOptions configures the B-side of a hot-swap.
type BSideOptions struct {
	// HandoffPort is the TCP port A will connect to for the handoff protocol.
	HandoffPort int
	// DataDir is the data directory (for handoff-state.json).
	DataDir string
}

// BSide runs the B-side of a hot-swap:
//  1. WSAStartup
//  2. Listen on the handoff port
//  3. Accept A's connection, exchange READY/DUP/BYE/ACK
//  4. If sockets were handed off, wrap in an AcceptLoop
//
// Returns an AcceptLoop if the listener was handed off (V2), or nil (V1 rebind-only).
func BSide(opts BSideOptions) (*AcceptLoop, error) {
	if err := ws2.WSAStartup(0x0202); err != nil {
		return nil, fmt.Errorf("WSAStartup failed: %w", err)
	}
	// Do not WSACleanup — Go's net poller and adopted sockets still need Winsock.

	if opts.HandoffPort == 0 {
		return nil, fmt.Errorf("handoff port not specified")
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", opts.HandoffPort))
	if err != nil {
		return nil, fmt.Errorf("failed to listen on handoff port %d: %w", opts.HandoffPort, err)
	}
	defer listener.Close()

	conn, err := listener.Accept()
	if err != nil {
		return nil, fmt.Errorf("failed to accept A's connection: %w", err)
	}

	bPID := os.Getpid()
	channel := NewHandoffChannelFromConn(conn)
	if err := channel.SendReady(bPID); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to send READY: %w", err)
	}
	log.Printf("BSide: READY sent (PID %d)", bPID)

	// Read messages until BYE.
	adoptedConns := 0
	var importedListener *RawListener

loop:
	for {
		line, err := channel.ReadLine()
		if err != nil {
			log.Printf("BSide: protocol ended: %v", err)
			break
		}

		switch {
		case line == "DUP" || len(line) > 3 && line[:4] == "DUP ":
			// V1: connected-socket handoff (FileConn path, may fail on Windows).
			// V2: listener handoff (RawListener + AcceptLoop).
			// For now, acknowledge and log. The FileConn path is kept
			// for backward compatibility but is known to fail with 10022.
			log.Printf("BSide: received DUP (socket handoff)")
			// Parse the base64 blob after "DUP ".
			blob, err := ParseDUP(line)
			if err != nil {
				fmt.Fprintf(conn, "GOT err=parse: %v\n", err)
				continue
			}
			_ = blob
			adoptedConns++
			fmt.Fprintf(conn, "GOT ok\n")
			log.Printf("BSide: acknowledged socket handoff (total %d)", adoptedConns)

		case line == "BYE":
			fmt.Fprintf(conn, "ACK\n")
			log.Printf("BSide: BYE received, %d sockets acknowledged", adoptedConns)
			conn.Close()
			break loop

		default:
			log.Printf("BSide: unexpected message: %s", line)
		}
	}

	// V1: no listener handoff. B will rebind the public port normally.
	// V2: if a listener blob was received, wrap in AcceptLoop.
	// (Listener handoff is not yet wired in the protocol; V1 is rebind-only.)
	if importedListener != nil {
		al := NewAcceptLoop(importedListener)
		log.Printf("BSide: listener handed off, AcceptLoop ready")
		return al, nil
	}

	log.Printf("BSide: handoff complete (rebind-only). Public port will be rebound by main.")
	return nil, nil
}
