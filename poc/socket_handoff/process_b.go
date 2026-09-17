// B: the "new" llamactl process.
//
// B is spawned by A with --handoff-port=<P>. B:
//  1. Calls WSAStartup (H3 requirement).
//  2. Listens on port P (the dedicated handoff port).
//  3. Accepts A's connection, sends READY pid=<B_PID>.
//  4. For each DUP message: decodes the WSAPROTOCOL_INFO blob, calls
//     WSASocket to adopt the socket, sends GOT ok / GOT err.
//  5. On BYE: sends ACK, closes the handoff connection, continues serving
//     on the adopted sockets.
//
// In this POC, B's "serving" is: read one line from each adopted socket,
// write back a response stamped with B's PID, then close. This proves the
// socket is live and owned by B.

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/llamactl/poc-socket-handoff/ws2"
)

var (
	ws2ProcRecv = ws2.ExternalProc("recv")
	ws2ProcSend = ws2.ExternalProc("send")
)

const (
	AF_INET     = int16(2)
	SOCK_STREAM = int16(1)
	IPPROTO_TCP = int16(6)
)

func runB() {
	if flag.Lookup("inflight-read").Value.String() == "true" {
		runBInFlight()
		return
	}
	handoffPort := flag.Lookup("handoff-port").Value.String()
	if handoffPort == "" || handoffPort == "0" {
		fmt.Fprintln(os.Stderr, "B: --handoff-port is required")
		os.Exit(2)
	}
	handoffPortInt, _ := strconv.Atoi(handoffPort)
	bPID := uint32(os.Getpid())
	log.Printf("B: starting pid=%d handoffPort=%d", bPID, handoffPortInt)

	// H3: WSAStartup must precede WSASocket.
	if err := ws2.WSAStartup(0x0202); err != nil {
		log.Fatalf("B: WSAStartup failed: %v", err)
	}
	defer ws2.WSACleanup()
	log.Printf("B: WSAStartup OK (H3 satisfied)")

	// Listen on the handoff port.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", handoffPortInt))
	if err != nil {
		log.Fatalf("B: listen on handoff port %d failed: %v", handoffPortInt, err)
	}
	log.Printf("B: listening on handoff port %d", handoffPortInt)

	// Accept A's connection.
	conn, err := ln.Accept()
	if err != nil {
		log.Fatalf("B: accept failed: %v", err)
	}
	log.Printf("B: accepted handoff connection from %s", conn.RemoteAddr())

	sess := NewHandoffSession(conn)

	// Send READY.
	if err := sess.Send(HandoffMessage{Verb: "READY", PID: bPID}); err != nil {
		log.Fatalf("B: send READY failed: %v", err)
	}
	log.Printf("B: sent READY pid=%d", bPID)

	// Process DUP messages until BYE.
	adopted := 0
	for {
		msg, err := sess.Recv()
		if err != nil {
			log.Fatalf("B: recv failed: %v", err)
		}

		switch msg.Verb {
		case "DUP":
			// Adopt the socket from the WSAPROTOCOL_INFO blob.
			sock, err := ws2.AdoptSocket(msg.Payload, AF_INET, SOCK_STREAM, IPPROTO_TCP)
			if err != nil {
				log.Printf("B: AdoptSocket failed: %v", err)
				sess.Send(HandoffMessage{Verb: "GOT", OK: false, Err: err.Error()})
				continue
			}
			log.Printf("B: adopted socket %v (raw handle)", sock)

			// Wrap in a net.Conn so we can read/write like a normal TCP conn.
			// The underlying FD is the adopted Winsock socket.
			f := os.NewFile(uintptr(sock), "adopted-socket")
			_ = f
			// POC: read/write directly on the raw socket via syscall.
			buf := make([]byte, 4096)
			n, rerr := rawRead(sock, buf)
			if rerr != nil {
				log.Printf("B: read on adopted socket failed: %v (n=%d)", rerr, n)
				sess.Send(HandoffMessage{Verb: "GOT", OK: false, Err: rerr.Error()})
				f.Close()
				continue
			}
			if n == 0 {
				log.Printf("B: read on adopted socket returned 0 bytes (EOF)")
				sess.Send(HandoffMessage{Verb: "GOT", OK: false, Err: "EOF on adopted socket"})
				f.Close()
				continue
			}
			log.Printf("B: read %d bytes from adopted socket: %q", n, string(buf[:n]))

			// Send a response stamped with B's PID.
			resp := fmt.Sprintf("RESPONSE-FROM-B pid=%d data=%q\n", bPID, string(buf[:n]))
			if werr := rawWrite(sock, []byte(resp)); werr != nil {
				log.Printf("B: write on adopted socket failed: %v", werr)
				sess.Send(HandoffMessage{Verb: "GOT", OK: false, Err: werr.Error()})
				f.Close()
				continue
			}
			log.Printf("B: wrote response to client on adopted socket")
			f.Close()

			adopted++
			sess.Send(HandoffMessage{Verb: "GOT", OK: true})

		case "BYE":
			log.Printf("B: received BYE, adopted %d socket(s)", adopted)
			sess.Send(HandoffMessage{Verb: "ACK"})
			log.Printf("B: sent ACK, closing handoff channel")
			sess.Close()
			ln.Close()
			return

		default:
			log.Printf("B: unexpected message %q", msg.Verb)
			sess.Close()
			ln.Close()
			return
		}
	}
}

// rawRead reads up to len(buf) bytes from a raw Winsock socket.
func rawRead(s syscall.Handle, buf []byte) (int, error) {
	r1, _, err := ws2ProcRecv.Call(
		uintptr(s),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)
	n := int(r1)
	if n < 0 {
		return 0, fmt.Errorf("recv: %v (WSA %d)", err, ws2.WSAError())
	}
	return n, nil
}

// rawWrite writes len(data) bytes to a raw Winsock socket.
func rawWrite(s syscall.Handle, data []byte) error {
	r1, _, err := ws2ProcSend.Call(
		uintptr(s),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		0,
	)
	if r1 == ^uintptr(0) || (r1 < 0 && err != nil) {
		return fmt.Errorf("send: %v (WSA %d)", err, ws2.WSAError())
	}
	return nil
}

var _ = net.IPv4 // keep net import
