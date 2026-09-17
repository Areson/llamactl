//go:build windows

package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	iAmA        = flag.Bool("i-am-a", false, "Run as A")
	iAmB        = flag.Bool("i-am-b", false, "Run as B (raw Winsock)")
	iAmBOptionA = flag.Bool("i-am-b-optA", false, "Run as B with net.Conn adapter (Option A)")
	iAmC        = flag.Bool("i-am-c", false, "Run as C")
	iAmO        = flag.Bool("i-am-o", false, "Run as orchestrator")
	iAmP        = flag.Bool("i-am-p", false, "Run preset test")

	addr    = flag.String("addr", "127.0.0.1:18180", "bind address")
	timeout = flag.Int("timeout", 5, "drain timeout seconds")
)

func main() {
	flag.Parse()

	switch {
	case *iAmO:
		runOrchestrator()
	case *iAmA:
		runA()
	case *iAmB:
		runB()
	case *iAmBOptionA:
		runBOptionA()
	case *iAmC:
		runC()
	case *iAmP:
		presetTest()
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `drain_probe: hot-swap drain POC
  --i-am-a         Role A: serve, duplicate, drain, exit
  --i-am-b         Role B: raw Winsock accept/recv/send
  --i-am-b-optA    Role B: net.Conn adapter + http.ServeMux (Option A)
  --i-am-c         Role C: client, reconnect
  --i-am-o         Orchestrator: spawn A, C, B; coordinate swap
  --i-am-p         Preset: test SetFileCompletionNotificationModes
  --addr ADDR      Bind address (default 127.0.0.1:18180)
  --timeout SECS   Drain timeout in seconds (default 5)`)
}
