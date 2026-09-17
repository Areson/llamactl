//go:build windows

package main

// Option A: B uses a net.Conn adapter over raw Winsock,
// fed into Go's http.ServeMux per-connection.

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	"drain_probe/ws2"
)

// rawConn implements net.Conn over a raw Winsock socket handle.
type rawConn struct {
	sock syscall.Handle
}

func (c *rawConn) Read(b []byte) (int, error) {
	return ws2.Recv(c.sock, b)
}

func (c *rawConn) Write(b []byte) (int, error) {
	if err := ws2.Send(c.sock, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *rawConn) Close() error {
	ws2.Close(c.sock)
	return nil
}

func (c *rawConn) LocalAddr() net.Addr { return &net.TCPAddr{} }
func (c *rawConn) RemoteAddr() net.Addr { return &net.TCPAddr{} }
func (c *rawConn) SetDeadline(_ time.Time) error      { return nil }
func (c *rawConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *rawConn) SetWriteDeadline(_ time.Time) error { return nil }

// rawListener implements net.Listener over a raw Winsock listening socket.
type rawListener struct {
	sock syscall.Handle
}

func (l *rawListener) Accept() (net.Conn, error) {
	cs, err := ws2.Accept(l.sock)
	if err != nil {
		return nil, err
	}
	return &rawConn{sock: cs}, nil
}

func (l *rawListener) Close() error {
	ws2.Close(l.sock)
	return nil
}

func (l *rawListener) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

// rawRespWriter is a minimal http.ResponseWriter over rawConn.
type rawRespWriter struct {
	conn       *rawConn
	status     int
	headerSent bool
}

func (w *rawRespWriter) Header() http.Header { return http.Header{} }

func (w *rawRespWriter) WriteHeader(code int) {
	if !w.headerSent {
		w.status = code
	}
}

func (w *rawRespWriter) Write(b []byte) (int, error) {
	if !w.headerSent {
		w.headerSent = true
		if w.status == 0 {
			w.status = 200
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

// runBOptionA is B using the net.Conn adapter + http.ServeMux per-conn.
func runBOptionA() {
	if err := ws2.WSAStartup(0x0202); err != nil {
		fmt.Fprintf(os.Stderr, "B-optA: WSAStartup: %v\n", err)
		os.Exit(1)
	}
	defer ws2.WSACleanup()

	blob, err := readBlobFromStdin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "B-optA: read blob: %v\n", err)
		os.Exit(1)
	}
	listenerSock, err := ws2.AdoptSocketBlocking(blob, 2, 1, 6)
	if err != nil {
		fmt.Fprintf(os.Stderr, "B-optA: import listener: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "B-optA: imported listener %x\n", listenerSock)

	// Build the mux — same handlers as the product.
	mux := http.NewServeMux()
	mux.HandleFunc("/work", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		fmt.Fprintf(w, `{"server":"B-optA"}`)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"server":"B-optA"}`)
	})

	// rawListener wraps the Winsock handle
	rl := &rawListener{sock: listenerSock}

	fmt.Printf("B-optA ready %d\n", os.Getpid())
	os.Stdout.Sync()
	fmt.Fprintf(os.Stderr, "B-optA: using net.Conn adapter + http.ServeMux\n")

	for {
		conn, err := rl.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "B-optA: accept: %v\n", err)
			break
		}
		go func(c net.Conn) {
			defer c.Close()
			br := bufio.NewReader(c)
			req, err := http.ReadRequest(br)
			if err != nil {
				fmt.Fprintf(os.Stderr, "B-optA: read request: %v\n", err)
				return
			}
			rw := &rawRespWriter{conn: c.(*rawConn)}
			mux.ServeHTTP(rw, req)
			fmt.Fprintf(os.Stderr, "B-optA: served %s %s\n", req.Method, req.URL.Path)
		}(conn)
	}
}

// readBlobFromStdin reads a hex-encoded blob from stdin.
func readBlobFromStdin() ([]byte, error) {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no blob on stdin")
	}
	return fromHex(scanner.Text())
}
