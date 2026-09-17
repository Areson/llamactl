//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runOrchestrator:
//  1. Spawn A (HTTP server on 18180).
//  2. Spawn C (client). C waits for A, fires /work (in-flight during swap).
//  3. Spawn B (adopter). B blocks on stdin, waiting for the blob.
//  4. Fire /swap?pid=<B_PID> on A. A duplicates the LISTENER to B,
//     emits "blob <hex>" on stdout, drains, Shutdown, exits.
//  5. Read the blob from A's stdout, write it to B's stdin.
//  6. B imports the listener, starts serving.
//  7. C's in-flight /work completes on A (drained). C's keep-alive drops.
//     C reconnects to B, fires /work → gets {"server":"B"}.
func runOrchestrator() {
	bin, _ := os.Executable()
	if bin == "" {
		bin = "drain.exe"
	}
	fmt.Println("orchestrator: starting")
	fmt.Printf("  bin=%s\n", bin)

	// --- Spawn A ---
	aCmd := exec.Command(bin, "--i-am-a")
	aStdout, _ := aCmd.StdoutPipe()
	aStderr, _ := aCmd.StderrPipe()
	if err := aCmd.Start(); err != nil {
		fatal("spawn A: %v", err)
	}
	fmt.Printf("  A started (PID %d)\n", aCmd.Process.Pid)

	var aErr strings.Builder
	go drainTo(aStderr, &aErr)

	aLines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(aStdout)
		for sc.Scan() {
			aLines <- sc.Text()
		}
		close(aLines)
	}()

	// Wait for A ready.
	readyOK := false
	readyDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(readyDeadline) {
		select {
		case line, ok := <-aLines:
			if !ok {
				fatal("A stdout closed before ready")
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			fmt.Printf("  A: %s\n", line)
			if strings.HasPrefix(line, "ready") {
				readyOK = true
				break
			}
			// Non-ready line before ready — log and keep waiting.
			fmt.Printf("  A (pre-ready): %s\n", line)
		case <-time.After(100 * time.Millisecond):
		}
		if readyOK {
			break
		}
	}
	if !readyOK {
		fatal("timeout waiting for A ready")
	}

	// --- Spawn C ---
	cCmd := exec.Command(bin, "--i-am-c")
	cOut, _ := cCmd.StdoutPipe()
	cErr, _ := cCmd.StderrPipe()
	if err := cCmd.Start(); err != nil {
		fatal("spawn C: %v", err)
	}
	fmt.Printf("  C started (PID %d)\n", cCmd.Process.Pid)

	var cOutBuf, cErrBuf strings.Builder
	go drainTo(cOut, &cOutBuf)
	go drainTo(cErr, &cErrBuf)

	// --- Spawn B (blocks on stdin until we write the blob) ---
	bCmd := exec.Command(bin, "--i-am-b-optA")
	bStdin, _ := bCmd.StdinPipe()
	bOut, _ := bCmd.StdoutPipe()
	bErr, _ := bCmd.StderrPipe()
	if err := bCmd.Start(); err != nil {
		fatal("spawn B: %v", err)
	}
	fmt.Printf("  B started (PID %d), waiting for blob\n", bCmd.Process.Pid)

	var bOutBuf, bErrBuf strings.Builder
	go drainTo(bOut, &bOutBuf)
	go drainTo(bErr, &bErrBuf)

	// --- Fire /swap on A with B's PID ---
	fmt.Println("  Triggering swap on A ...")
	swapURL := fmt.Sprintf("http://127.0.0.1:18180/swap?pid=%d", bCmd.Process.Pid)
	resp, err := httpGet(swapURL)
	if err != nil {
		fatal("/swap: %v", err)
	}
	fmt.Printf("  /swap response: %s\n", resp)

	// --- Read the blob from A's stdout ---
	var blobHex string
	blobDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(blobDeadline) {
		select {
		case line := <-aLines:
			fmt.Printf("  A: %s\n", line)
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[0] == "blob" {
				blobHex = parts[1]
			}
		default:
			time.Sleep(50 * time.Millisecond)
		}
		if blobHex != "" {
			break
		}
	}
	if blobHex == "" {
		fatal("no blob received from A")
	}
	fmt.Printf("  Blob received (%d hex chars)\n", len(blobHex))

	// --- Write the blob to B's stdin ---
	if _, err := bStdin.Write([]byte(blobHex + "\n")); err != nil {
		fatal("write blob to B stdin: %v", err)
	}
	bStdin.Close()
	fmt.Println("  Blob written to B")

	// --- Wait for B ready ---
	bDeadline := time.Now().Add(15 * time.Second)
	bReady := false
	for time.Now().Before(bDeadline) {
		if strings.Contains(bOutBuf.String(), "B-optA ready") {
			bReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if bReady {
		fmt.Printf("  B is ready (imported listener, serving)\n")
	} else {
		fmt.Printf("  WARNING: B not ready in 15s\n")
		fmt.Printf("  --- B stdout ---\n%s\n", bOutBuf.String())
		fmt.Printf("  --- B stderr ---\n%s\n", bErrBuf.String())
	}

	// --- Wait for A to exit ---
	aDone := make(chan error, 1)
	go func() { aDone <- aCmd.Wait() }()
	select {
	case aErrCode := <-aDone:
		if aErrCode != nil {
			fmt.Printf("  A exited with: %v\n", aErrCode)
		} else {
			fmt.Printf("  A exited cleanly\n")
		}
	case <-time.After(15 * time.Second):
		fmt.Printf("  WARNING: A still running after 15s\n")
	}
	fmt.Printf("  --- A stderr ---\n%s\n", aErr.String())

	// --- Wait for C to finish ---
	cDone := make(chan error, 1)
	go func() { cDone <- cCmd.Wait() }()
	select {
	case cErrCode := <-cDone:
		if cErrCode != nil {
			fmt.Printf("  C exited with: %v\n", cErrCode)
		} else {
			fmt.Printf("  C exited\n")
		}
	case <-time.After(30 * time.Second):
		fmt.Printf("  WARNING: C still running after 30s\n")
	}

	// Let B drain, then kill.
	time.Sleep(2 * time.Second)
	if bCmd.Process != nil {
		bCmd.Process.Kill()
		bCmd.Wait()
	}

	fmt.Println()
	fmt.Println("========== C VERDICT ==========")
	fmt.Print(cOutBuf.String())
	fmt.Println("========== C stderr ==========")
	fmt.Print(cErrBuf.String())
	fmt.Println("========== B stdout ==========")
	fmt.Print(bOutBuf.String())
	fmt.Println("========== B stderr ==========")
	fmt.Print(bErrBuf.String())
}

func drainTo(r interface{ Read([]byte) (int, error) }, buf *strings.Builder) {
	b := make([]byte, 4096)
	for {
		n, err := r.Read(b)
		if n > 0 {
			buf.WriteString(string(b[:n]))
		}
		if err != nil {
			return
		}
	}
}

func fatal(format string, args ...any) {
	fmt.Printf("FATAL: "+format+"\n", args...)
	os.Exit(1)
}
