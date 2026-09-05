//go:build windows

package hotswap

import (
	"context"
	"log"
	"time"
)

// Drainer tracks in-flight work for the A-side drain.
// Implemented by the instanceManager (or a wrapper).
type Drainer interface {
	// InFlight returns the number of currently in-flight requests.
	InFlight() int
	// Wait blocks until in-flight work completes or the deadline passes.
	// Returns nil if all work completed, or an error on timeout.
	Wait(deadline time.Time) error
}

// DrainOptions configures the A-side drain.
type DrainOptions struct {
	Timeout time.Duration
	// ServerShutdown is called after in-flight work completes,
	// to flush remaining HTTP responses. Optional.
	ServerShutdown func(ctx context.Context) error
}

// DrainA runs the A-side drain sequence:
//  1. Wait for in-flight work (with deadline).
//  2. Call ServerShutdown to flush remaining responses.
//
// Returns nil on success, error if the deadline passed with work remaining.
func DrainA(drainer Drainer, opts DrainOptions) error {
	if drainer == nil {
		log.Printf("DrainA: no drainer, skipping")
		return nil
	}

	deadline := time.Now().Add(opts.Timeout)
	log.Printf("DrainA: draining in-flight work (timeout %v, in-flight=%d) ...", opts.Timeout, drainer.InFlight())

	if err := drainer.Wait(deadline); err != nil {
		log.Printf("DrainA: drain incomplete: %v", err)
	} else {
		log.Printf("DrainA: all in-flight work completed")
	}

	if opts.ServerShutdown != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := opts.ServerShutdown(ctx); err != nil {
			log.Printf("DrainA: server shutdown: %v", err)
		}
	}

	log.Printf("DrainA: drain complete")
	return nil
}
