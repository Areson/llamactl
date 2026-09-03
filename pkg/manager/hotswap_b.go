//go:build windows

package manager

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"llamactl/pkg/hotswap"
	"llamactl/pkg/hotswap/ws2"
)

// HotSwapB is the B-side of the hot-swap: receive client sockets from A,
// then return so main can rebind the public port and serve.
func (im *instanceManager) HotSwapB() error {
	dataDir := im.globalConfig.DataDir

	if err := ws2.WSAStartup(0x0202); err != nil {
		return fmt.Errorf("WSAStartup failed: %w", err)
	}
	// Do not WSACleanup — Go's net poller and the adopted sockets still need Winsock.

	handoffPort := 0
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--handoff-port=") {
			fmt.Sscanf(arg, "--handoff-port=%d", &handoffPort)
			break
		}
	}
	if handoffPort == 0 {
		return fmt.Errorf("--handoff-port not specified")
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", handoffPort))
	if err != nil {
		return fmt.Errorf("failed to listen on handoff port %d: %w", handoffPort, err)
	}
	defer listener.Close()

	conn, err := listener.Accept()
	if err != nil {
		return fmt.Errorf("failed to accept A's connection: %w", err)
	}

	bPID := os.Getpid()
	channel := hotswap.NewHandoffChannelFromConn(conn)
	if err := channel.SendReady(bPID); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send READY: %w", err)
	}
	log.Printf("HotSwapB: READY sent (PID %d)", bPID)

	reader := bufio.NewReader(conn)
	adopted := 0

loop:
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

			adoptedFD, err := ws2.AdoptSocket(blob, 2, 1, 6) // AF_INET, SOCK_STREAM, IPPROTO_TCP
			if err != nil {
				fmt.Fprintf(conn, "GOT err=adopt: %v\n", err)
				continue
			}

			f := os.NewFile(uintptr(adoptedFD), fmt.Sprintf("adopted-conn-%d", adoptedFD))
			c, err := net.FileConn(f)
			if err != nil {
				log.Printf("HotSwapB: FileConn failed for fd %d: %v", adoptedFD, err)
				f.Close()
				fmt.Fprintf(conn, "GOT err=fileconn: %v\n", err)
				continue
			}
			// FileConn dups the handle; keep f so GC cannot close the SOCKET
			// out from under c on Windows.
			im.adoptedFiles = append(im.adoptedFiles, f)
			im.adoptedConns = append(im.adoptedConns, c)
			fmt.Fprintf(conn, "GOT ok\n")
			adopted++
			log.Printf("HotSwapB: adopted client socket %d (total %d)", adoptedFD, adopted)

		case "BYE":
			fmt.Fprintf(conn, "ACK\n")
			log.Printf("HotSwapB: BYE received, %d sockets adopted.", adopted)
			conn.Close()
			break loop

		default:
			log.Printf("HotSwapB: unexpected message: %s", line)
		}
	}

	aPID := 0
	if state, err := hotswap.ReadHandoffState(dataDir); err == nil && state != nil {
		aPID = state.APID
	}
	if aPID > 0 {
		go im.cleanupOldBinary(aPID)
	}

	log.Printf("HotSwapB: handoff complete. Public port will be rebound by main.")
	return nil
}
