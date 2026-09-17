package main

// This probe isolates the problematic production condition: A has a real Go
// TCPConn.Read pending in the Windows netpoller when it duplicates the socket.
//
// A duplicates twice: B uses the first copy only to test net.FileConn, then
// uses the second copy with raw Winsock I/O. A closes its own handle before C
// sends the payload, so B is the only remaining receiver for that payload.

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/llamactl/poc-socket-handoff/ws2"
)

const inflightTail = "TAIL-AFTER-INFLIGHT-READ\n"

func runAInFlight() {
	log.Printf("A[inflight]: starting pid=%d", os.Getpid())
	handoffPort, err := pickFreePort()
	if err != nil {
		log.Fatalf("A[inflight]: pick handoff port: %v", err)
	}
	mainPort, err := pickFreePort()
	if err != nil {
		log.Fatalf("A[inflight]: pick main port: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("A[inflight]: executable: %v", err)
	}
	cmdB := exec.Command(exe, "--i-am-b", "--inflight-read", fmt.Sprintf("--handoff-port=%d", handoffPort))
	cmdB.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
	cmdB.Stdout, cmdB.Stderr = os.Stdout, os.Stderr
	if err := cmdB.Start(); err != nil {
		log.Fatalf("A[inflight]: start B: %v", err)
	}
	bPID := uint32(cmdB.Process.Pid)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", mainPort))
	if err != nil {
		log.Fatalf("A[inflight]: listen: %v", err)
	}
	defer ln.Close()

	var control net.Conn
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		control, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", handoffPort), 500*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if control == nil {
		log.Fatalf("A[inflight]: B did not open handoff port: %v", err)
	}
	defer control.Close()
	session := NewHandoffSession(control)
	ready, err := session.Recv()
	if err != nil || ready.Verb != "READY" || ready.PID != bPID {
		log.Fatalf("A[inflight]: invalid READY: msg=%+v err=%v", ready, err)
	}

	cmdC := exec.Command(exe, "--i-am-c", "--inflight-read", fmt.Sprintf("--main-port=%d", mainPort), fmt.Sprintf("--b-pid=%d", bPID))
	cmdC.Stdout, cmdC.Stderr = os.Stdout, os.Stderr
	if err := cmdC.Start(); err != nil {
		log.Fatalf("A[inflight]: start C: %v", err)
	}

	client, err := ln.Accept()
	if err != nil {
		log.Fatalf("A[inflight]: accept C: %v", err)
	}
	readStarted := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		close(readStarted)
		var one [1]byte
		_, err := client.Read(one[:])
		readDone <- err
	}()
	<-readStarted
	// Give the goroutine time to issue WSARecv and park in Go's netpoller.
	time.Sleep(250 * time.Millisecond)
	log.Printf("A[inflight]: Go TCPConn.Read is pending; duplicating product-style handle")

	fd := extractFDViaSyscallConn(client)
	if fd < 0 {
		log.Fatalf("A[inflight]: SyscallConn extraction failed")
	}

	// First duplicate: isolate net.FileConn under pending source I/O.
	probeBlob, err := ws2.DuplicateSocket(syscall.Handle(fd), bPID)
	if err != nil {
		log.Fatalf("A[inflight]: duplicate FileConn probe: %v", err)
	}
	if err := session.Send(HandoffMessage{Verb: "DUP", Payload: probeBlob}); err != nil {
		log.Fatalf("A[inflight]: send probe DUP: %v", err)
	}
	probeResult, err := session.Recv()
	if err != nil || probeResult.Verb != "GOT" {
		log.Fatalf("A[inflight]: probe GOT: msg=%+v err=%v", probeResult, err)
	}
	if probeResult.OK {
		log.Printf("A[inflight]: UNEXPECTED FileConn success while A read was pending")
	} else {
		log.Printf("A[inflight]: expected FileConn failure: %s", probeResult.Err)
	}

	// Second duplicate remains intact for the raw-read continuity assertion.
	serveBlob, err := ws2.DuplicateSocket(syscall.Handle(fd), bPID)
	if err != nil {
		log.Fatalf("A[inflight]: duplicate raw serve socket: %v", err)
	}
	if err := session.Send(HandoffMessage{Verb: "DUP", Payload: serveBlob}); err != nil {
		log.Fatalf("A[inflight]: send serve DUP: %v", err)
	}
	serveResult, err := session.Recv()
	if err != nil || serveResult.Verb != "GOT" || !serveResult.OK {
		log.Fatalf("A[inflight]: raw serve GOT: msg=%+v err=%v", serveResult, err)
	}

	// Cancel A's outstanding WSARecv and relinquish A's copy before C writes.
	if err := client.Close(); err != nil {
		log.Fatalf("A[inflight]: close source client socket: %v", err)
	}
	if err := <-readDone; err == nil {
		log.Fatalf("A[inflight]: pending source read unexpectedly completed")
	} else {
		log.Printf("A[inflight]: pending Go read cancelled by close: %v", err)
	}

	if err := session.Send(HandoffMessage{Verb: "SERVE"}); err != nil {
		log.Fatalf("A[inflight]: send SERVE: %v", err)
	}
	done, err := session.Recv()
	if err != nil || done.Verb != "DONE" {
		log.Fatalf("A[inflight]: read DONE: msg=%+v err=%v", done, err)
	}
	if err := session.Send(HandoffMessage{Verb: "BYE"}); err != nil {
		log.Fatalf("A[inflight]: send BYE: %v", err)
	}
	ack, err := session.Recv()
	if err != nil || ack.Verb != "ACK" {
		log.Fatalf("A[inflight]: read ACK: msg=%+v err=%v", ack, err)
	}
	if err := cmdC.Wait(); err != nil {
		log.Fatalf("A[inflight]: C failed: %v", err)
	}
	log.Printf("A[inflight]: PASS — raw socket continuity survived pending Go netpoller read")
}

func runBInFlight() {
	handoffPort := flag.Lookup("handoff-port").Value.String()
	bPID := uint32(os.Getpid())
	if err := ws2.WSAStartup(0x0202); err != nil {
		log.Fatalf("B[inflight]: WSAStartup: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+handoffPort)
	if err != nil {
		log.Fatalf("B[inflight]: listen: %v", err)
	}
	defer ln.Close()
	control, err := ln.Accept()
	if err != nil {
		log.Fatalf("B[inflight]: accept: %v", err)
	}
	defer control.Close()
	session := NewHandoffSession(control)
	if err := session.Send(HandoffMessage{Verb: "READY", PID: bPID}); err != nil {
		log.Fatalf("B[inflight]: READY: %v", err)
	}

	var rawSocket syscall.Handle
	for attempt := 0; ; {
		msg, err := session.Recv()
		if err != nil {
			log.Fatalf("B[inflight]: receive: %v", err)
		}
		switch msg.Verb {
		case "DUP":
			sock, err := ws2.AdoptSocket(msg.Payload, AF_INET, SOCK_STREAM, IPPROTO_TCP)
			if err != nil {
				session.Send(HandoffMessage{Verb: "GOT", OK: false, Err: err.Error()})
				continue
			}
			if attempt == 0 {
				attempt++
				f := os.NewFile(uintptr(sock), "inflight-fileconn-probe")
				c, fileErr := net.FileConn(f)
				if c != nil {
					_ = c.Close()
				}
				_ = f.Close()
				if fileErr != nil {
					log.Printf("B[inflight]: expected FileConn failure: %v", fileErr)
					session.Send(HandoffMessage{Verb: "GOT", OK: false, Err: "fileconn: " + fileErr.Error()})
				} else {
					log.Printf("B[inflight]: FileConn succeeded")
					session.Send(HandoffMessage{Verb: "GOT", OK: true})
				}
				continue
			}
			rawSocket = sock
			attempt++
			log.Printf("B[inflight]: adopted raw serving socket %d", rawSocket)
			session.Send(HandoffMessage{Verb: "GOT", OK: true})
		case "SERVE":
			if rawSocket == 0 {
				log.Fatalf("B[inflight]: SERVE before raw socket adoption")
			}
			buf := make([]byte, 4096)
			n, err := rawRead(rawSocket, buf)
			if err != nil {
				log.Fatalf("B[inflight]: raw read: %v", err)
			}
			payload := string(buf[:n])
			log.Printf("B[inflight]: raw read after A closed: %q", payload)
			if err := rawWrite(rawSocket, []byte(fmt.Sprintf("RESPONSE-FROM-B pid=%d data=%q\n", bPID, payload))); err != nil {
				log.Fatalf("B[inflight]: raw write: %v", err)
			}
			syscall.CloseHandle(rawSocket)
			rawSocket = 0
			session.Send(HandoffMessage{Verb: "DONE"})
		case "BYE":
			session.Send(HandoffMessage{Verb: "ACK"})
			return
		default:
			log.Fatalf("B[inflight]: unexpected verb %q", msg.Verb)
		}
	}
}

func runCInFlight() {
	mainPort := flag.Lookup("main-port").Value.String()
	bPID := flag.Lookup("b-pid").Value.String()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", "127.0.0.1:"+mainPort, 500*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		log.Fatalf("C[inflight]: connect: %v", err)
	}
	defer conn.Close()
	// A has 250ms to establish the pending read, perform both handoffs, and
	// close its copy before C makes bytes readable on the transferred socket.
	time.Sleep(1500 * time.Millisecond)
	if _, err := conn.Write([]byte(inflightTail)); err != nil {
		log.Fatalf("C[inflight]: write tail: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		log.Fatalf("C[inflight]: read response: %v", err)
	}
	response := string(buf[:n])
	if !contains(response, inflightTail[:len(inflightTail)-1]) || !contains(response, "pid="+bPID) {
		log.Fatalf("C[inflight]: wrong response: %q", response)
	}
	log.Printf("C[inflight]: PASS — B read post-handoff tail after A's pending read was cancelled")
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
