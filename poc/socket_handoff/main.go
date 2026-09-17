// A: the orchestrator (old llamactl stand-in).
//
// A:
//  1. Picks a free handoff port (bind :0, read back, close).
//  2. Spawns B detached with --handoff-port=<P> --i-am-b.
//  3. Spawns C (client) with --main-port=<mainPort> --b-pid=<B_PID>.
//  4. Listens on mainPort (the "public" port, like :8079).
//  5. Waits for C to connect.
//  6. Waits for B to be ready (connects to handoff port, reads READY).
//  7. Calls WSADuplicateSocket on C's live socket, targeting B's PID.
//  8. Sends the WSAPROTOCOL_INFO blob to B over the handoff channel.
//  9. Receives GOT ok from B.
// 10. Sends BYE, receives ACK.
// 11. Exits. C should now be talking to B, with no drop.
//
// The key insight: A's listener on mainPort keeps accepting. C connects to
// A. A duplicates C's socket to B. B now owns C's connection. A exits. C
// never saw a drop.

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"github.com/llamactl/poc-socket-handoff/ws2"
)

const (
	midstreamPrefix = "PREFIX-CONSUMED-BY-A\n"
	midstreamTail   = "TAIL-CONSUMED-BY-B\n"
)

func runA() {
	if flag.Lookup("inflight-read").Value.String() == "true" {
		runAInFlight()
		return
	}
	log.Printf("A: starting pid=%d", os.Getpid())

	// Step 1: Pick a free handoff port.
	handoffPort, err := pickFreePort()
	if err != nil {
		log.Fatalf("A: pickFreePort failed: %v", err)
	}
	log.Printf("A: picked handoff port %d", handoffPort)

	// Pick a main port (the "public" port C will connect to).
	mainPort, err := pickFreePort()
	if err != nil {
		log.Fatalf("A: pickFreePort (main) failed: %v", err)
	}
	log.Printf("A: picked main port %d", mainPort)

	// Step 2: Spawn B detached.
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("A: os.Executable failed: %v", err)
	}
	cmdB := exec.Command(exe, "--i-am-b", "--handoff-port", fmt.Sprint(handoffPort))
	cmdB.SysProcAttr = &syscall.SysProcAttr{
		// DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		CreationFlags: 0x00000008 | 0x00000200,
	}
	cmdB.Stdout = os.Stdout
	cmdB.Stderr = os.Stderr
	if err := cmdB.Start(); err != nil {
		log.Fatalf("A: spawn B failed: %v", err)
	}
	bPID := uint32(cmdB.Process.Pid)
	log.Printf("A: spawned B pid=%d (detached)", bPID)

	// Step 3: Listen on mainPort (the public port).
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", mainPort))
	if err != nil {
		log.Fatalf("A: listen on main port %d failed: %v", mainPort, err)
	}
	log.Printf("A: listening on main port %d", mainPort)

	// Step 4: Connect to B's handoff port and wait for READY.
	var sess *HandoffSession
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", handoffPort))
		if err != nil {
			if time.Now().After(deadline) {
				log.Fatalf("A: connect to B handoff port %d timed out: %v", handoffPort, err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		sess = NewHandoffSession(conn)
		break
	}
	log.Printf("A: connected to B handoff channel on port %d", handoffPort)

	// Read READY and verify PID.
	msg, err := sess.Recv()
	if err != nil {
		log.Fatalf("A: read READY failed: %v", err)
	}
	if msg.Verb != "READY" {
		log.Fatalf("A: expected READY, got %q", msg.Verb)
	}
	log.Printf("A: B sent READY pid=%d", msg.PID)
	if msg.PID != bPID {
		log.Fatalf("A: PID mismatch — A thinks B is %d, B says %d", bPID, msg.PID)
	}
	log.Printf("A: PID sanity check passed (H: B's PID matches A's CreateProcess result)")

	// Step 5: Verify B is ready (PID sanity check).
	// (READY was already read above; this is the sanity check.)
	log.Printf("A: PID sanity check passed (B's PID matches A's CreateProcess result)")

	// Step 6: Spawn C (client). C connects to A's main port.
	cmdCArgs := []string{"--i-am-c", "--main-port", fmt.Sprint(mainPort), "--b-pid", fmt.Sprint(bPID)}
	midstream := flag.Lookup("midstream").Value.String() == "true"
	if midstream {
		cmdCArgs = append(cmdCArgs, "--midstream")
	}
	cmdC := exec.Command(exe, cmdCArgs...)
	cmdC.Stdout = os.Stdout
	cmdC.Stderr = os.Stderr
	if err := cmdC.Start(); err != nil {
		log.Fatalf("A: spawn C failed: %v", err)
	}
	log.Printf("A: spawned C pid=%d", cmdC.Process.Pid)

	// Step 7: Accept C's connection on the main port (with timeout).
	log.Printf("A: waiting for C to connect on main port %d...", mainPort)
	var cConn net.Conn
	acceptDone := make(chan net.Conn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		c, e := ln.Accept()
		if e != nil {
			acceptErr <- e
			return
		}
		acceptDone <- c
	}()
	select {
	case c := <-acceptDone:
		cConn = c
		log.Printf("A: accepted C connection from %s", cConn.RemoteAddr())
	case e := <-acceptErr:
		log.Fatalf("A: accept C failed: %v", e)
	case <-time.After(10 * time.Second):
		log.Fatalf("A: accept C timed out after 10s")
	}

	if midstream {
		// Deliberately consume exactly one framed prefix before the handoff.
		// The tail remains in the TCP receive buffer for B. This isolates the
		// question: can B receive bytes after A completed a prior read?
		prefix := make([]byte, len(midstreamPrefix))
		if _, err := io.ReadFull(cConn, prefix); err != nil {
			log.Fatalf("A: read midstream prefix failed: %v", err)
		}
		if string(prefix) != midstreamPrefix {
			log.Fatalf("A: unexpected midstream prefix %q", string(prefix))
		}
		log.Printf("A: consumed prefix %q; leaving tail for B", string(prefix))
	} else {
		// IMPORTANT: Do NOT read the payload here. The payload is still in the
		// kernel buffer. We duplicate the socket to B, and B reads it.
		log.Printf("A: NOT reading payload (leaving it in kernel buffer for B)")
	}

	// Step 6: Duplicate C's socket to B.
	fd := extractFD(cConn)
	if midstream {
		// This matches the product's extraction path and avoids File(), which
		// attempts to disassociate the source handle from Go's netpoller.
		fd = extractFDViaSyscallConn(cConn)
	}
	log.Printf("A: C's socket raw FD = %d (0x%x)", fd, fd)

	// Verify it's actually a socket.
	var peerAddr [16]byte
	var addrLen int32 = 16
	procGetPeerName := ws2.ExternalProc("getpeername")
	r, _, _ := procGetPeerName.Call(
		uintptr(fd),
		uintptr(unsafe.Pointer(&peerAddr[0])),
		uintptr(unsafe.Pointer(&addrLen)),
	)
	log.Printf("A: getpeername(s=%d) r=%d (0=ok, SOCKET_ERROR=fail)", fd, r)

	blob, err := ws2.DuplicateSocket(syscall.Handle(fd), bPID)
	if err != nil {
		log.Fatalf("A: WSADuplicateSocket failed: %v", err)
	}
	log.Printf("A: WSADuplicateSocket OK, blob size = %d bytes", len(blob))

	// Step 7: Send the blob to B.
	if err := sess.Send(HandoffMessage{Verb: "DUP", Payload: blob}); err != nil {
		log.Fatalf("A: send DUP failed: %v", err)
	}
	log.Printf("A: sent DUP to B (%d bytes base64)", len(blob))

	// Step 8: Receive GOT from B.
	gotMsg, err := sess.Recv()
	if err != nil {
		log.Fatalf("A: read GOT failed: %v", err)
	}
	if gotMsg.Verb != "GOT" {
		log.Fatalf("A: expected GOT, got %q", gotMsg.Verb)
	}
	if !gotMsg.OK {
		log.Fatalf("A: B failed to adopt socket: %s", gotMsg.Err)
	}
	log.Printf("A: B confirmed socket adoption (GOT ok)")

	// Step 9: BYE / ACK.
	if err := sess.Send(HandoffMessage{Verb: "BYE"}); err != nil {
		log.Fatalf("A: send BYE failed: %v", err)
	}
	ackMsg, err := sess.Recv()
	if err != nil {
		log.Fatalf("A: read ACK failed: %v", err)
	}
	if ackMsg.Verb != "ACK" {
		log.Fatalf("A: expected ACK, got %q", ackMsg.Verb)
	}
	log.Printf("A: received ACK, handoff complete")

	// Step 10: Close the handoff channel and the main listener.
	sess.Close()
	ln.Close()
	// Close A's copy of C's socket — B now owns it.
	cConn.Close()

	// Spawn a stand-in child process (simulates llama-server) for the H4
	// orphan-survival check. This child is detached and should survive A's exit.
	cmdChild := exec.Command("cmd", "/c", "timeout", "/t", "60")
	cmdChild.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
	cmdChild.Stdout = os.Stdout
	cmdChild.Stderr = os.Stderr
	if err := cmdChild.Start(); err != nil {
		log.Printf("A: WARN: spawn stand-in child failed: %v (H4 orphan check skipped)", err)
	} else {
		childPID := uint32(cmdChild.Process.Pid)
		log.Printf("A: spawned stand-in child pid=%d (H4 orphan check target)", childPID)

		// Verify the child is alive right now.
		if err := cmdChild.Process.Signal(syscall.Signal(0)); err != nil {
			log.Printf("A: WARN: stand-in child not alive after spawn: %v", err)
		}
	}

	log.Printf("A: exiting. C should now be talking to B (pid=%d) with no drop.", bPID)
	log.Printf("A: H1 (cross-process transfer): awaiting C's verdict...")

	// A exits. C will report PASS/FAIL independently.
	os.Exit(0)
}

// pickFreePort binds to :0, reads back the assigned port, closes, returns it.
func pickFreePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// extractFD gets the raw Winsock SOCKET from a net.Conn.
// On Windows, Go's net.TCPConn.File() returns an *os.File whose Fd() IS the
// Winsock SOCKET handle (Winsock sockets are kernel handles).
func extractFD(conn net.Conn) int {
	type fileInterface interface {
		File() (*os.File, error)
	}
	fi, ok := conn.(fileInterface)
	if !ok {
		return -1
	}
	f, err := fi.File()
	if err != nil {
		return -1
	}
	// Don't close f — we're just reading the FD. The conn still owns it.
	fd := int(f.Fd())
	// Re-acquire the conn (File() may have put it in exclusive mode).
	return fd
}

// extractFDViaSyscallConn returns the original Winsock SOCKET without changing
// the source connection's blocking or ownership mode.
func extractFDViaSyscallConn(conn net.Conn) int {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return -1
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return -1
	}
	fd := -1
	if err := raw.Control(func(handle uintptr) { fd = int(handle) }); err != nil {
		return -1
	}
	return fd
}
