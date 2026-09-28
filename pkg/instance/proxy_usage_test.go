package instance

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"llamactl/pkg/stats"
)

func TestEnsureIncludeUsageInjects(t *testing.T) {
	out, changed := ensureIncludeUsage([]byte(`{"model":"m","messages":[],"stream":true}`))
	if !changed {
		t.Fatal("expected change")
	}
	if !bytes.Contains(out, []byte(`"include_usage":true`)) {
		t.Fatalf("missing include_usage: %s", out)
	}
}

func TestEnsureIncludeUsageIdempotent(t *testing.T) {
	in := []byte(`{"model":"m","stream_options":{"include_usage":true}}`)
	out, changed := ensureIncludeUsage(in)
	if changed {
		t.Fatal("should not rewrite when already set")
	}
	if !bytes.Equal(out, in) {
		t.Fatal("body mutated")
	}
}

func TestEnsureIncludeUsageNonJSON(t *testing.T) {
	out, changed := ensureIncludeUsage([]byte("not-json"))
	if changed || string(out) != "not-json" {
		t.Fatalf("changed=%v out=%s", changed, out)
	}
}

func TestIsCompletionPath(t *testing.T) {
	if !isCompletionPath("/v1/chat/completions") || !isCompletionPath("/v1/completions/") {
		t.Fatal("expected completion paths")
	}
	if isCompletionPath("/v1/models") || isCompletionPath("/health") {
		t.Fatal("non-completion should be false")
	}
}

func TestUsageCaptureBodyJSON(t *testing.T) {
	payload := `{"id":"x","usage":{"prompt_tokens":10,"completion_tokens":5,"completion_time":0.1,"completion_tokens_per_sec":50.0,"prompt_time":0.05,"prompt_tokens_per_sec":200.0}}`
	var got *stats.OpenAIUsage
	var mu sync.Mutex
	body := &usageCaptureBody{
		ReadCloser: io.NopCloser(strings.NewReader(payload)),
		isSSE:      false,
		record: func(u *stats.OpenAIUsage) {
			mu.Lock()
			got = u
			mu.Unlock()
		},
	}
	buf := make([]byte, 64)
	for {
		_, err := body.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = body.Close()
	mu.Lock()
	defer mu.Unlock()
	if got == nil || got.CompletionTokens != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestUsageCaptureBodySSE(t *testing.T) {
	sse := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"completion_time\":0.05,\"completion_tokens_per_sec\":40.0}}\n\n" +
		"data: [DONE]\n\n"
	var got *stats.OpenAIUsage
	var mu sync.Mutex
	body := &usageCaptureBody{
		ReadCloser: io.NopCloser(strings.NewReader(sse)),
		isSSE:      true,
		record: func(u *stats.OpenAIUsage) {
			mu.Lock()
			got = u
			mu.Unlock()
		},
	}
	all, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	if !bytes.Contains(all, []byte("[DONE]")) {
		t.Fatal("client bytes not preserved")
	}
	mu.Lock()
	defer mu.Unlock()
	if got == nil || got.CompletionTokens != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.CompletionTokensPerSec != 40 {
		t.Fatalf("rate = %f", got.CompletionTokensPerSec)
	}
}

func TestFormatPrintTimingViaAppendRoundTrip(t *testing.T) {
	// Sanity: RecordFromUsage + FormatPrintTiming stays parseable (also covered
	// in pkg/stats). Keep a proxy-package smoke so refactors here can't drop it.
	u := &stats.OpenAIUsage{CompletionTokens: 8, CompletionTimeSec: 0.2, CompletionTokensPerSec: 40}
	rec := stats.RecordFromUsage(u, time.UnixMilli(42).UTC())
	if rec == nil {
		t.Fatal("nil record")
	}
	lines := stats.FormatPrintTiming(*rec)
	parsed := stats.Parse(lines)
	if len(parsed) != 1 || parsed[0].GenTokens != 8 {
		t.Fatalf("parsed %+v", parsed)
	}
}

func TestMaybeCaptureUsageSkipsNonCompletion(t *testing.T) {
	p := &proxy{instance: &Instance{}}
	req := httptest.NewRequest(http.MethodGet, "http://backend/health", nil)
	resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: req}
	p.maybeCaptureUsage(resp)
	// Body should remain the original NopCloser, not usageCaptureBody.
	if _, ok := resp.Body.(*usageCaptureBody); ok {
		t.Fatal("should not wrap non-completion responses")
	}
}