//go:build windows

package hotswap

import (
	"bufio"
	"log"
	"net/http"
)

// AcceptLoop serves HTTP connections on a raw Winsock listener.
// It replaces http.Server.Serve for the imported-socket case where
// Go's netpoller cannot associate the socket (10022 on Windows).
type AcceptLoop struct {
	listener *RawListener
}

// NewAcceptLoop wraps a RawListener for per-connection serving.
func NewAcceptLoop(listener *RawListener) *AcceptLoop {
	return &AcceptLoop{listener: listener}
}

// Serve blocks, accepting connections and serving them via mux.
// Returns nil on clean shutdown (listener closed), or an error.
func (al *AcceptLoop) Serve(mux *http.ServeMux) error {
	log.Printf("AcceptLoop: serving (mux=%p)", mux)

	for {
		conn, err := al.listener.Accept()
		if err != nil {
			log.Printf("AcceptLoop: accept ended: %v", err)
			return nil
		}

		go func(c *RawConn) {
			defer c.Close()

			br := bufio.NewReader(c)
			req, err := http.ReadRequest(br)
			if err != nil {
				log.Printf("AcceptLoop: read request: %v", err)
				return
			}

			rw := NewRawRespWriter(c)
			mux.ServeHTTP(rw, req)
			log.Printf("AcceptLoop: served %s %s", req.Method, req.URL.Path)
		}(conn.(*RawConn))
	}
}
