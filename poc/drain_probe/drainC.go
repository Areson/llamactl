//go:build windows

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const cBase = "http://127.0.0.1:18180"

func runC() {
	fmt.Println("C: starting")

	clientA := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     15 * time.Second,
		},
		Timeout: 30 * time.Second,
	}

	// Wait for A.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := clientA.Get(cBase + "/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("C: A is up")

	// Fire /work against A (in-flight when the swap triggers).
	workResult := make(chan string, 1)
	go func() {
		resp, err := clientA.Get(cBase + "/work")
		if err != nil {
			workResult <- "ERROR: " + err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		workResult <- string(body)
	}()

	time.Sleep(500 * time.Millisecond)

	select {
	case result := <-workResult:
		fmt.Printf("C: /work on A → %s\n", result)
	case <-time.After(15 * time.Second):
		fmt.Println("C: TIMEOUT waiting for /work on A")
		os.Exit(1)
	}

	// Probe after A drain — expect the keep-alive conn to be dead.
	probeResult := make(chan string, 1)
	go func() {
		resp, err := clientA.Get(cBase + "/healthz")
		if err != nil {
			probeResult <- "DROPPED: " + err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		probeResult <- "ALIVE: " + string(body)
	}()
	select {
	case result := <-probeResult:
		fmt.Printf("C: probe after A drain → %s\n", result)
	case <-time.After(10 * time.Second):
		fmt.Println("C: TIMEOUT on post-drain probe")
	}

	// Reconnect to B with a fresh transport.
	fmt.Println("C: reconnecting to B ...")
	clientB := &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     15 * time.Second,
		},
		Timeout: 30 * time.Second,
	}

	bDeadline := time.Now().Add(15 * time.Second)
	bUp := false
	for time.Now().Before(bDeadline) {
		resp, err := clientB.Get(cBase + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if len(body) > 0 {
				bUp = true
				fmt.Printf("C: B healthz → %s\n", body)
			}
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !bUp {
		fmt.Println("C: TIMEOUT waiting for B")
		os.Exit(1)
	}

	resp, err := clientB.Get(cBase + "/work")
	// Retry: B's accept() loop may be briefly busy after the first conn.
	for attempt := 0; err != nil && attempt < 5; attempt++ {
		time.Sleep(500 * time.Millisecond)
		resp, err = clientB.Get(cBase + "/work")
	}
	if err != nil {
		fmt.Printf("C: /work on B ERROR: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("C: /work on B → %s\n", body)

	if contains(string(body), `"B"`) || contains(string(body), `"B-optA"`) {
		fmt.Println("C: VERDICT: PASS — C reconnected to B and got a response")
	} else {
		fmt.Println("C: VERDICT: FAIL — expected B, got:", string(body))
		os.Exit(1)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
