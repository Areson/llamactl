//go:build windows

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
)

type respWriter struct {
	conn   net.Conn
	status int
	hdr    http.Header
}

func newRespWriter(conn net.Conn) *respWriter {
	return &respWriter{conn: conn, hdr: make(http.Header)}
}

func (w *respWriter) Header() http.Header {
	return w.hdr
}

func (w *respWriter) WriteHeader(status int) {
	w.status = status
}

func (w *respWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	statusText := "OK"
	if w.status != 200 {
		statusText = "Error"
	}
	ct := w.hdr.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nConnection: close\r\n\r\n", w.status, statusText, ct)
	return w.conn.Write(data)
}

func serveOneConn(conn net.Conn, mux *http.ServeMux) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		if err != io.EOF {
			fmt.Fprintf(conn, "HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
		}
		conn.Close()
		return
	}
	w := newRespWriter(conn)
	mux.ServeHTTP(w, req)
	conn.Close()
}
