//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"drain_probe/ws2"
)

const (
	listenAddr   = "127.0.0.1:18180"
	workDelay    = 1500 * time.Millisecond
	drainTimeout = 5 * time.Second
)

var (
	aMu       sync.Mutex
	aInflight sync.WaitGroup
	aDraining bool
	aListener net.Listener
	aServer   *http.Server
)

func runA() {
	if err := ws2.WSAStartup(0x0202); err != nil {
		log.Fatalf("WSAStartup: %v", err)
	}
	defer ws2.WSACleanup()

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	aListener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/work", aWorkHandler)
	mux.HandleFunc("/swap", aSwapHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"server":"A","draining":%v}`, isADraining())
	})

	aServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	fmt.Printf("ready %d %s\n", os.Getpid(), ln.Addr().String())
	os.Stdout.Sync()
	log.Printf("A: serving on %s", listenAddr)

	if err := aServer.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("A: Serve: %v", err)
	}
	log.Printf("A: exited cleanly (drain complete)")
}

func aWorkHandler(w http.ResponseWriter, r *http.Request) {
	aInflight.Add(1)
	defer aInflight.Done()

	time.Sleep(workDelay)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	fmt.Fprint(w, `{"server":"A"}`)
}

func aSwapHandler(w http.ResponseWriter, r *http.Request) {
	aMu.Lock()
	aDraining = true
	aMu.Unlock()

	pidStr := r.URL.Query().Get("pid")
	var pid uint32
	fmt.Sscanf(pidStr, "%d", &pid)
	if pid == 0 {
		http.Error(w, `{"error":"invalid pid"}`, http.StatusBadRequest)
		return
	}

	// Duplicate the LISTENER socket to B's PID.
	ln, ok := aListener.(*net.TCPListener)
	if !ok {
		http.Error(w, `{"error":"listener not *net.TCPListener"}`, http.StatusInternalServerError)
		return
	}

	raw, err := ln.SyscallConn()
	if err != nil {
		http.Error(w, `{"error":"SyscallConn: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}
	var sock syscall.Handle
	if err := raw.Control(func(h uintptr) {
		sock = syscall.Handle(h)
	}); err != nil {
		http.Error(w, `{"error":"Control: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	blob, err := ws2.DuplicateSocket(sock, pid)
	if err != nil {
		http.Error(w, `{"error":"DuplicateSocket: `+err.Error()+`"}`, http.StatusInternalServerError)
		return
	}

	// Emit the blob as hex on stdout for the orchestrator to pick up.
	fmt.Printf("blob %s\n", toHex(blob))
	os.Stdout.Sync()

	log.Printf("A: listener duplicated to B (PID %d), blob=%d bytes", pid, len(blob))

	// Respond to the caller immediately. The drain happens in a goroutine.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	fmt.Fprintf(w, `{"status":"swap-started","drainTimeoutSeconds":%d}`, int(drainTimeout/time.Second))

	// Drain + shutdown in a separate goroutine so the response flushes
	// before the listener closes.
	go func() {
		// Give the response a moment to flush to the client.
		time.Sleep(200 * time.Millisecond)

		log.Printf("A: draining in-flight work (timeout %s) ...", drainTimeout)
		done := make(chan struct{})
		go func() {
			aInflight.Wait()
			close(done)
		}()
		select {
		case <-done:
			log.Printf("A: all in-flight work completed")
		case <-time.After(drainTimeout):
			log.Printf("A: drain timeout; forcing shutdown")
		}

		ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()
		if err := aServer.Shutdown(ctx); err != nil {
			log.Printf("A: Shutdown: %v", err)
		}
		log.Printf("A: shutdown complete; process will exit")
		// Small pause to let Serve() return and the process to exit cleanly.
		time.Sleep(100 * time.Millisecond)
	}()
}

func isADraining() bool {
	aMu.Lock()
	defer aMu.Unlock()
	return aDraining
}
