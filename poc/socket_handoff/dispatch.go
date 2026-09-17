// Command socket-handoff is a POC that proves WSADuplicateSocket can hand
// a live TCP connection from process A to process B on Windows, with the
// client (C) seeing zero interruption.
//
// Usage:
//
//	socket-handoff --i-am-a                # clean-stream handoff
//	socket-handoff --i-am-a --midstream    # A reads prefix; B reads tail
//	socket-handoff --i-am-a --inflight-read # A has pending Go Read; B probes adoption
//	socket-handoff --i-am-b --handoff-port=<P>   # run as B (new process)
//	socket-handoff --i-am-c --main-port=<P> --b-pid=<PID>  # run as C (client)
//
// The POC is driven by A. See README.md for the full test matrix (H1–H5).

package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	// Parse flags common to all roles.
	iAmA := flag.Bool("i-am-a", false, "run as process A (orchestrator)")
	iAmB := flag.Bool("i-am-b", false, "run as process B (new process)")
	iAmC := flag.Bool("i-am-c", false, "run as process C (client)")
	flag.Int("handoff-port", 0, "handoff TCP port (B)")
	flag.Int("main-port", 0, "main TCP port (C)")
	flag.Int("b-pid", 0, "expected B PID (C)")
	flag.String("payload-file", "", "payload file for C")
	flag.Bool("midstream", false, "have A read a prefix before handing the remaining stream to B")
	flag.Bool("inflight-read", false, "hold A in a pending Go TCPConn.Read while handing off the socket")
	flag.Parse()

	switch {
	case *iAmA:
		runA()
	case *iAmB:
		runB()
	case *iAmC:
		runC()
	default:
		fmt.Fprintln(os.Stderr, "specify --i-am-a, --i-am-b, or --i-am-c")
		fmt.Fprintln(os.Stderr, "see README.md for usage")
		os.Exit(2)
	}
}
