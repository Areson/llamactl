package server

import (
	"net"
	"sync"
)

// chainedListener Accepts pre-adopted connections first, then falls through
// to the inner listener (the rebound public port).
type chainedListener struct {
	mu      sync.Mutex
	pending []net.Conn
	inner   net.Listener
}

// NewChainedListener yields pending conns from Accept before inner.Accept.
func NewChainedListener(inner net.Listener, pending []net.Conn) net.Listener {
	copied := make([]net.Conn, len(pending))
	copy(copied, pending)
	return &chainedListener{pending: copied, inner: inner}
}

func (l *chainedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if len(l.pending) > 0 {
		c := l.pending[0]
		l.pending = l.pending[1:]
		l.mu.Unlock()
		return c, nil
	}
	l.mu.Unlock()
	return l.inner.Accept()
}

func (l *chainedListener) Close() error {
	l.mu.Lock()
	pending := l.pending
	l.pending = nil
	l.mu.Unlock()
	for _, c := range pending {
		_ = c.Close()
	}
	return l.inner.Close()
}

func (l *chainedListener) Addr() net.Addr {
	return l.inner.Addr()
}
