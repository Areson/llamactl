//go:build windows

package server

import (
	"log"
	"net"
	"strings"
	"sync"
	"syscall"
)

// TrackingListener wraps a net.Listener and records every accepted
// connection so the hot-swap code can duplicate them to the new process.
type TrackingListener struct {
	net.Listener

	mu    sync.Mutex
	conns map[net.Conn]bool
}

// NewTrackingListener wraps an inner listener with connection tracking.
func NewTrackingListener(inner net.Listener) *TrackingListener {
	return &TrackingListener{
		Listener: inner,
		conns:    make(map[net.Conn]bool),
	}
}

// Accept accepts a connection and records it. The returned conn unregisters
// itself on Close so ActiveConns does not retain dead sockets.
func (t *TrackingListener) Accept() (net.Conn, error) {
	conn, err := t.Listener.Accept()
	if err != nil {
		log.Printf("TrackingListener: Accept() error: %v", err)
		return nil, err
	}
	wrapped := &trackedConn{Conn: conn, tracker: t}
	t.mu.Lock()
	t.conns[wrapped] = true
	t.mu.Unlock()
	return wrapped, nil
}

// Remove unregisters a closed connection.
func (t *TrackingListener) Remove(conn net.Conn) {
	t.mu.Lock()
	delete(t.conns, conn)
	t.mu.Unlock()
}

// SetPath records the request path for a connection.
func (t *TrackingListener) SetPath(conn net.Conn, path string) {
	if tc, ok := conn.(*trackedConn); ok {
		tc.path = path
	}
}

// ActiveConns returns a snapshot of currently tracked connections.
func (t *TrackingListener) ActiveConns() []net.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	conns := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		conns = append(conns, c)
	}
	return conns
}

// SocketHandles returns the raw Winsock SOCKET for each tracked connection,
// extracted via SyscallConn. SSE/event-stream connections are skipped — the
// UI reconnects to B after the swap.
func (t *TrackingListener) SocketHandles() []syscall.Handle {
	conns := t.ActiveConns()
	out := make([]syscall.Handle, 0, len(conns))
	for _, c := range conns {
		if tc, ok := c.(*trackedConn); ok && strings.Contains(tc.path, "/events") {
			continue
		}
		if fd := socketHandle(c); fd != 0 {
			out = append(out, fd)
		}
	}
	return out
}

// Close shuts down the listener, unblocking pending Accept() calls.
func (t *TrackingListener) Close() error {
	log.Printf("TrackingListener: closing listener (%d tracked conns left open)", len(t.ActiveConns()))
	return t.Listener.Close()
}

type trackedConn struct {
	net.Conn
	tracker *TrackingListener
	once    sync.Once
	path    string
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() {
		c.tracker.Remove(c)
		err = c.Conn.Close()
	})
	return err
}

func socketHandle(conn net.Conn) syscall.Handle {
	if tc, ok := conn.(*trackedConn); ok {
		conn = tc.Conn
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return 0
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return 0
	}
	var fd syscall.Handle
	if ctlErr := raw.Control(func(h uintptr) {
		fd = syscall.Handle(h)
	}); ctlErr != nil {
		return 0
	}
	return fd
}
