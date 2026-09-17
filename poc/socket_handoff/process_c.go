// C: the client process.
//
// C is spawned by A. C:
//  1. Connects to A's listener on 127.0.0.1:<mainPort>.
//  2. Sends a known payload.
//  3. Reads a response.
//  4. Verifies the response is stamped with B's PID (proves the socket
//     was actually handed to B, not served by A).
//  5. Prints PASS/FAIL and the full byte stream it saw.
//
// C runs as a separate process so it outlives A and can report whether
// it saw a TCP reset or a clean handoff.

package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

func runC() {
	if flag.Lookup("inflight-read").Value.String() == "true" {
		runCInFlight()
		return
	}
	mainPortStr := flag.Lookup("main-port").Value.String()
	bPIDStr := flag.Lookup("b-pid").Value.String()
	payloadFile := flag.Lookup("payload-file").Value.String()
	midstream := flag.Lookup("midstream").Value.String() == "true"

	mainPort, _ := strconv.Atoi(mainPortStr)
	bPID, _ := strconv.Atoi(bPIDStr)

	if mainPort == 0 {
		fmt.Fprintln(os.Stderr, "C: --main-port is required")
		os.Exit(2)
	}

	log.Printf("C: starting pid=%d mainPort=%d expectedB=%d", os.Getpid(), mainPort, bPID)

	// Connect to A's listener.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", mainPort))
	if err != nil {
		log.Fatalf("C: connect to :%d failed: %v", mainPort, err)
	}
	log.Printf("C: connected to 127.0.0.1:%d", mainPort)

	// Read the payload.
	var payload []byte
	if payloadFile != "" {
		data, err := os.ReadFile(payloadFile)
		if err != nil {
			log.Fatalf("C: read payload file failed: %v", err)
		}
		payload = data
	} else if midstream {
		payload = []byte(midstreamPrefix + midstreamTail)
	} else {
		payload = []byte("HELLO-FROM-C pid=" + fmt.Sprint(os.Getpid()) + "\n")
	}

	// Send the payload.
	if _, err := conn.Write(payload); err != nil {
		log.Fatalf("C: send payload failed: %v", err)
	}
	log.Printf("C: sent %d bytes: %q", len(payload), string(payload))

	// Read the response (with a generous timeout — the swap takes a moment).
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	resp, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		log.Printf("C: FAIL — read response failed: %v", err)
		log.Printf("C: saw a TCP reset or timeout. The connection DROPPED during the swap.")
		os.Exit(1)
	}

	log.Printf("C: received response: %q", resp)

	// Verify the response is from B.
	expectedMarker := fmt.Sprintf("pid=%d", bPID)
	if !strings.Contains(resp, expectedMarker) {
		log.Printf("C: FAIL — response does not contain %q. Served by the wrong process?", expectedMarker)
		os.Exit(1)
	}

	// Verify the response echoes our payload.
	// B formats the payload with %q (Go quoting), so we check for the
	// unquoted content as a substring of the quoted form.
	payloadCore := strings.TrimRight(string(payload), "\n")
	if midstream {
		payloadCore = strings.TrimRight(midstreamTail, "\n")
	}
	if !strings.Contains(resp, payloadCore) {
		log.Printf("C: FAIL — response does not echo the sent payload. Data was lost.")
		os.Exit(1)
	}

	if midstream {
		log.Printf("C: PASS — B read the tail after A consumed the prefix; no TCP reset seen.")
	} else {
		log.Printf("C: PASS — response is from B (pid=%d), payload echoed intact, no TCP reset seen.", bPID)
	}
	log.Printf("C: The connection survived the swap. The socket was handed off cleanly.")
	conn.Close()
}
