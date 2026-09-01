package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// InstanceEvent is the wire format for an SSE instance event. It mirrors
// manager.InstanceEvent but with JSON-friendly fields.
type sseInstanceEvent struct {
	Type      string `json:"type"` // "status_change"
	Name      string `json:"name"`
	OldStatus string `json:"oldStatus"`
	NewStatus string `json:"newStatus"`
}

// InstanceEvents is a Server-Sent Events endpoint that pushes instance
// status changes to the client in real time. The client can stop polling
// /instances and rely on these events to update individual cards.
//
// A keep-alive comment is sent every 15s so proxies and browsers don't close
// the idle connection.
func (h *Handler) InstanceEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// SSE requires a flushing response writer.
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "sse_unsupported", "Streaming not supported")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // nginx: disable buffering
		w.WriteHeader(http.StatusOK)

		// Send an initial "connected" event so the client knows the stream is live.
		fmt.Fprintf(w, "event: connected\ndata: {\"type\":\"connected\"}\n\n")
		flusher.Flush()

		sub := h.InstanceManager.Subscribe()
		defer sub.Unsubscribe()

		// Keep-alive ticker (15s) to prevent proxies from closing the connection.
		keepAlive := time.NewTicker(15 * time.Second)
		defer keepAlive.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				// Client disconnected or server shutting down.
				return
			case ev, ok := <-sub.Ch:
				if !ok {
					// Unsubscribed — channel closed.
					return
				}
				data, err := json.Marshal(sseInstanceEvent{
					Type:      "status_change",
					Name:      ev.Name,
					OldStatus: ev.OldStatus.String(),
					NewStatus: ev.NewStatus.String(),
				})
				if err != nil {
					// Malformed event — skip it rather than break the stream.
					continue
				}
				fmt.Fprintf(w, "event: status_change\ndata: %s\n\n", data)
				flusher.Flush()
			case <-keepAlive.C:
				// SSE keep-alive comment (a line starting with ':' is ignored by clients).
				fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
			}
		}
	}
}
