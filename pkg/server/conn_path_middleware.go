//go:build windows

package server

import (
	"net/http"
	"sync"
)

// ConnPathMiddleware records the request path on the underlying connection
// for the TrackingListener. This lets SocketHandles() skip SSE/event-stream
// connections during hot-swap.
//
// Simpler approach: a global registry of *TrackingListener instances.
// The middleware looks up the tracker by the connection's RemoteAddr.
var (
	trackedListenersMu sync.Mutex
	trackedListeners   = make(map[string]*TrackingListener) // RemoteAddr → tracker
)

func registerTrackerByAddr(addr string, tl *TrackingListener) {
	trackedListenersMu.Lock()
	trackedListeners[addr] = tl
	trackedListenersMu.Unlock()
}

func ConnPathMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteAddr := r.RemoteAddr
		trackedListenersMu.Lock()
		var tl *TrackingListener
		for _, t := range trackedListeners {
			if conn := t.FindConnByAddr(remoteAddr); conn != nil {
				tl = t
				break
			}
		}
		trackedListenersMu.Unlock()
		if tl != nil {
			if conn := tl.FindConnByAddr(remoteAddr); conn != nil {
				tl.SetPath(conn, r.URL.Path)
			}
		}
		next.ServeHTTP(w, r)
	})
}
