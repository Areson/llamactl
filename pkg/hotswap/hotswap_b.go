//go:build windows

// Package hotswap: B-side orchestration.
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
	HandoffPort int
	DataDir     string
}

// BSide runs the B-side of a hot-swap:
//  1. WSAStartup
//  2. Listen on the handoff port
//  3. Accept A's connection, exchange READY/DUP/BYE/ACK
func BSide(opts BSideOptions) error {
	if err := ws2.WSAStartup(0x0202); err != nil {
		return fmt.Errorf("WSAStartup failed: %w", err)
	}
	// Do not WSACleanup — Go's net poller still needs Winsock.

	if opts.HandoffPort == 0 {
		return fmt.Errorf("handoff port not specified")
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", opts.HandoffPort))
	if err != nil {
		return fmt.Errorf("failed to listen on handoff port %d: %w", opts.HandoffPort, err)
	}
	defer listener.Close()

	conn, err := listener.Accept()
	if err != nil {
		return fmt.Errorf("failed to accept A's connection: %w", err)
	}

	channel := NewHandoffChannelFromConn(conn)
	if err := channel.SendReady(os.Getpid()); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send READY: %w", err)
	}

	adopted := 0
	for {
		line, err := channel.ReadLine()
		if err != nil {
			break
		}
		switch {
		case line == "DUP" || len(line) > 3 && line[:4] == "DUP ":
			blob, err := ParseDUP(line)
			if err != nil {
				fmt.Fprintf(conn, "GOT err=parse: %v\n", err)
				continue
			}
			_ = blob
			adopted++
			fmt.Fprintf(conn, "GOT ok\n")

		case line == "BYE":
			fmt.Fprintf(conn, "ACK\n")
			conn.Close()
			return nil

		default:
			fmt.Fprintf(conn, "GOT err=unexpected: %s\n", line)
		}
	}

	if adopted > 0 {
		log.Printf("BSide: handoff complete, %d sockets acknowledged", adopted)
	} else {
		log.Printf("BSide: handoff complete (no sockets)")
	}
	return nil
}
