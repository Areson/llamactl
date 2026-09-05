//go:build windows

package hotswap

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"llamactl/pkg/hotswap/ws2"
)

// RawConn implements net.Conn over a raw Winsock socket handle.
// It bypasses Go's netpoller (IOCP), which cannot associate
// WSADuplicateSocket-imported sockets (10022 on Windows).
type RawConn struct {
	sock syscall.Handle
}

// NewRawConn wraps a Winsock handle as a net.Conn.
func NewRawConn(sock syscall.Handle) *RawConn {
	return &RawConn{sock: sock}
}

func (c *RawConn) Read(b []byte) (int, error) {
	return ws2.Recv(c.sock, b)
}

func (c *RawConn) Write(b []byte) (int, error) {
	if err := ws2.Send(c.sock, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *RawConn) Close() error {
	return ws2.Close(c.sock)
}

func (c *RawConn) LocalAddr() net.Addr    { return &net.TCPAddr{} }
func (c *RawConn) RemoteAddr() net.Addr   { return &net.TCPAddr{} }
func (c *RawConn) SetDeadline(_ time.Time) error      { return nil }
func (c *RawConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *RawConn) SetWriteDeadline(_ time.Time) error { return nil }

// RawListener implements net.Listener over a raw Winsock listening socket.
type RawListener struct {
	sock syscall.Handle
}

// NewRawListener wraps a Winsock listening socket as a net.Listener.
func NewRawListener(sock syscall.Handle) *RawListener {
	return &RawListener{sock: sock}
}

func (l *RawListener) Accept() (net.Conn, error) {
	cs, err := ws2.Accept(l.sock)
	if err != nil {
		return nil, err
	}
	return NewRawConn(cs), nil
}

func (l *RawListener) Close() error {
	return ws2.Close(l.sock)
}

func (l *RawListener) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

// RawRespWriter implements http.ResponseWriter over a RawConn.
// It emits a minimal HTTP/1.1 response with Content-Length.
type RawRespWriter struct {
	conn       *RawConn
	status     int
	headerSent bool
}

// NewRawRespWriter creates a ResponseWriter for a raw connection.
func NewRawRespWriter(conn *RawConn) *RawRespWriter {
	return &RawRespWriter{conn: conn}
}

func (w *RawRespWriter) Header() http.Header {
	return http.Header{}
}

func (w *RawRespWriter) WriteHeader(code int) {
	if !w.headerSent {
		w.status = code
	}
}

func (w *RawRespWriter) Write(b []byte) (int, error) {
	if !w.headerSent {
		w.headerSent = true
		if w.status == 0 {
			w.status = http.StatusOK
		}
		statusText := http.StatusText(w.status)
		if statusText == "" {
			statusText = "OK"
		}
		fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
			w.status, statusText, len(b))
	}
	return w.conn.Write(b)
}
