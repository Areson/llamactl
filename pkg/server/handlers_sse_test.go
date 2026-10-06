package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"llamactl/pkg/config"
	"llamactl/pkg/manager"
)

// sseFakeManager gives the SSE handler a working subscription (the shared
// fake returns a zero EventSubscriber, whose nil Unsubscribe would panic).
type sseFakeManager struct {
	*fakeInstanceManager
}

func (sseFakeManager) Subscribe() manager.EventSubscriber {
	ch := make(chan manager.InstanceEvent)
	return manager.EventSubscriber{Ch: ch, Unsubscribe: func() {}}
}

// serveForShutdown runs h's router on a real http.Server wired like main.go.
func serveForShutdown(t *testing.T, h *Handler, extra http.HandlerFunc) (*http.Server, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/events", h.InstanceEvents())
	if extra != nil {
		mux.Handle("/slow", extra)
	}
	srv := &http.Server{Handler: mux}
	srv.RegisterOnShutdown(h.CloseStreams)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv, "http://" + ln.Addr().String()
}

// openSSE connects and waits for the initial "connected" event.
func openSSE(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url + "/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("no connected event: %q, %v", line, err)
	}
	return resp
}

func TestShutdown_EndsOpenSSEStreams(t *testing.T) {
	h := NewHandler(sseFakeManager{newFakeInstanceManager()}, nil, config.AppConfig{}, nil)
	srv, url := serveForShutdown(t, h, nil)
	openSSE(t, url)
	openSSE(t, url)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v (open SSE streams held it until the deadline)", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Shutdown took %v with open SSE streams; want prompt return", d)
	}
}

func TestShutdown_StillDrainsInFlightRequests(t *testing.T) {
	h := NewHandler(sseFakeManager{newFakeInstanceManager()}, nil, config.AppConfig{}, nil)
	started := make(chan struct{})
	srv, url := serveForShutdown(t, h, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-time.After(time.Second): // stand-in for a proxied inference request
			w.Write([]byte("done"))
		case <-r.Context().Done():
		}
	})
	openSSE(t, url)

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get(url + "/slow")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		var b strings.Builder
		_, err = bufio.NewReader(resp.Body).WriteTo(&b)
		got <- result{b.String(), err}
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	r := <-got
	if r.err != nil || r.body != "done" {
		t.Fatalf("in-flight request was cut off: body=%q err=%v", r.body, r.err)
	}
}

func TestCloseStreams_IdempotentAndZeroHandlerSafe(t *testing.T) {
	h := NewHandler(sseFakeManager{newFakeInstanceManager()}, nil, config.AppConfig{}, nil)
	h.CloseStreams()
	h.CloseStreams()
	(&Handler{}).CloseStreams()
}
