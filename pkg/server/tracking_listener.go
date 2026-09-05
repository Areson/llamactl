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
	paths map[net.Conn]string // request path per connection (set by ConnPathMiddleware)
}

// NewTrackingListener wraps an inner listener with connection tracking.
func NewTrackingListener(inner net.Listener) *TrackingListener {
	return &TrackingListener{
		Listener: inner,
		conns:    make(map[net.Conn]bool),
		paths:    make(map[net.Conn]string),
	}
}

// Accept accepts a connection and records it. The returned conn unregisters
// itself on Close so ActiveConns does not retain dead sockets.
func (t *TrackingListener) Accept() (net.Conn, error) {
	log.Printf("TrackingListener: Accept() called")
	conn, err := t.Listener.Accept()
	if err != nil {
		log.Printf("TrackingListener: Accept() error: %v", err)
		return nil, err
	}
	log.Printf("TrackingListener: Accept() got conn from %s", conn.RemoteAddr())
	wrapped := &trackedConn{Conn: conn, tracker: t}
	t.mu.Lock()
	t.conns[wrapped] = true
	t.mu.Unlock()
	// Register for ConnPathMiddleware lookup (keyed by client RemoteAddr).
	if addr := conn.RemoteAddr().String(); addr != "" {
		registerTrackerByAddr(addr, t)
	}
	return wrapped, nil
}

// Remove unregisters a closed connection.
func (t *TrackingListener) Remove(conn net.Conn) {
	t.mu.Lock()
	delete(t.conns, conn)
	delete(t.paths, conn)
	t.mu.Unlock()
	// Unregister from ConnPathMiddleware lookup.
	if addr := conn.RemoteAddr().String(); addr != "" {
		trackedListenersMu.Lock()
		delete(trackedListeners, addr)
		trackedListenersMu.Unlock()
	}
}

// SetPath records the request path for a connection.
func (t *TrackingListener) SetPath(conn net.Conn, path string) {
	t.mu.Lock()
	t.paths[conn] = path
	t.mu.Unlock()
}

// FindConnByAddr finds a tracked connection by its RemoteAddr.
func (t *TrackingListener) FindConnByAddr(addr string) net.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	for c := range t.conns {
		if c.RemoteAddr().String() == addr {
			return c
		}
	}
	return nil
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
// UI reconnects to B after the swap. Model proxy connections (short-lived,
// no in-flight client I/O at swap time) are included.
func (t *TrackingListener) SocketHandles() []syscall.Handle {
	conns := t.ActiveConns()
	out := make([]syscall.Handle, 0, len(conns))
	for _, c := range conns {
		// Skip SSE/event-stream connections — the UI reconnects.
		if path := t.pathFor(c); strings.Contains(path, "/events") {
			log.Printf("SocketHandles: skipping SSE conn %s (path=%s)", c.RemoteAddr(), path)
			continue
		}
		if fd := socketHandle(c); fd != 0 {
			out = append(out, fd)
		}
	}
	return out
}

func (t *TrackingListener) pathFor(conn net.Conn) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.paths[conn]
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
