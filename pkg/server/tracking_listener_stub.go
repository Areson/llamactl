//go:build !windows

package server

import "net"

// TrackingListener is a no-op wrapper on non-Windows (hot-swap is Windows-only).
type TrackingListener struct {
	net.Listener
}

func NewTrackingListener(inner net.Listener) *TrackingListener {
	return &TrackingListener{Listener: inner}
}

func (t *TrackingListener) ActiveConns() []net.Conn { return nil }
