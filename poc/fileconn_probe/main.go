//go:build windows

package main

import (
	"flag"
	"log"
)

var (
	iAmA = flag.Bool("i-am-a", false, "A: orchestrator")
	iAmB = flag.Bool("i-am-b", false, "B: adopter")
	iAmC = flag.Bool("i-am-c", false, "C: client")
)

func main() {
	flag.Parse()
	switch {
	case *iAmA:
		runA()
	case *iAmB:
		runB()
	case *iAmC:
		runC()
	default:
		log.Fatal("specify --i-am-a, --i-am-b, or --i-am-c")
	}
}
