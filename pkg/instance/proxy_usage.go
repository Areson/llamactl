package instance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"llamactl/pkg/backends"
	"llamactl/pkg/stats"
)

// completionPaths are backend-relative paths whose OpenAI-compatible responses
// may carry usage we turn into /stats throughput records.
var completionPaths = map[string]bool{
	"/v1/chat/completions": true,
	"/v1/completions":      true,
}

func isCompletionPath(path string) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	return completionPaths[path]
}

// ensureIncludeUsage rewrites a chat/completions JSON body so TabbyAPI (and
// OpenAI-compatible servers that gate usage on stream_options.include_usage)
// returns timed usage. No-op when already set or body is not JSON object.
func ensureIncludeUsage(body []byte) ([]byte, bool) {
	if len(body) == 0 || body[0] != '{' {
		return body, false
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, false
	}
	so, _ := m["stream_options"].(map[string]any)
	if so != nil {
		if v, ok := so["include_usage"].(bool); ok && v {
			return body, false
		}
	} else {
		so = map[string]any{}
		m["stream_options"] = so
	}
	so["include_usage"] = true
	out, err := json.Marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// injectIncludeUsageForTabby rewrites the outbound request body for TabbyAPI
// completion calls so usage (with timing) is always returned. Other backends
// are left alone: llama.cpp may reject stream_options on non-stream requests.
func (p *proxy) injectIncludeUsageForTabby(req *http.Request) {
	if p.instance == nil || p.instance.GetBackendType() != backends.BackendTypeTabbyAPI {
		return
	}
	if req.Body == nil || !isCompletionPath(req.URL.Path) {
		return
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		req.ContentLength = 0
		return
	}
	out, changed := ensureIncludeUsage(body)
	req.Body = io.NopCloser(bytes.NewReader(out))
	req.ContentLength = int64(len(out))
	if changed {
		req.Header.Set("Content-Length", strconv.Itoa(len(out)))
	}
}

// maybeCaptureUsage wraps resp.Body so a completed generation's OpenAI usage
// is recorded as print_timing lines in the instance log (feeding /stats).
func (p *proxy) maybeCaptureUsage(resp *http.Response) {
	if p.instance == nil || p.instance.IsRemote() || resp == nil || resp.Body == nil {
		return
	}
	if resp.Request == nil || !isCompletionPath(resp.Request.URL.Path) {
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	ct := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(ct, "text/event-stream")
	orig := resp.Body
	resp.Body = &usageCaptureBody{
		ReadCloser: orig,
		isSSE:      isSSE,
		record: func(u *stats.OpenAIUsage) {
			p.recordUsage(u)
		},
	}
}

func (p *proxy) recordUsage(u *stats.OpenAIUsage) {
	if u == nil || p.instance == nil {
		return
	}
	rec := stats.RecordFromUsage(u, time.Now())
	if rec == nil {
		return
	}
	p.instance.AppendThroughputTiming(*rec)
}

// AppendThroughputTiming writes llama.cpp-compatible print_timing lines for
// a proxy-observed usage sample so GetInstanceStats can scrape them.
func (i *Instance) AppendThroughputTiming(rec stats.ThroughputRecord) {
	if i == nil || i.logger == nil {
		return
	}
	i.logger.appendLines(stats.FormatPrintTiming(rec))
}

// usageCaptureBody tees the upstream response, extracting OpenAI usage from a
// JSON body or the final SSE data line without altering bytes delivered to
// the client.
type usageCaptureBody struct {
	io.ReadCloser
	isSSE  bool
	record func(*stats.OpenAIUsage)

	mu       sync.Mutex
	buf      bytes.Buffer // non-SSE: full body; SSE: incomplete line carry
	done     bool
	reported bool
}

func (b *usageCaptureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		if b.isSSE {
			b.consumeSSE(p[:n])
		} else if b.buf.Len() < 8<<20 { // cap 8 MiB
			_, _ = b.buf.Write(p[:n])
		}
		b.mu.Unlock()
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *usageCaptureBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *usageCaptureBody) finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	if b.reported || b.record == nil {
		return
	}
	if b.isSSE {
		if b.buf.Len() > 0 {
			b.consumeSSELine(b.buf.String())
			b.buf.Reset()
		}
		return
	}
	u := stats.ParseOpenAIUsage(b.buf.Bytes())
	if u != nil {
		b.reported = true
		b.record(u)
	}
}

func (b *usageCaptureBody) consumeSSE(chunk []byte) {
	_, _ = b.buf.Write(chunk)
	for {
		data := b.buf.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			if b.buf.Len() > 1<<20 {
				b.buf.Reset()
			}
			return
		}
		line := string(data[:i])
		rest := append([]byte(nil), data[i+1:]...)
		b.buf.Reset()
		_, _ = b.buf.Write(rest)
		b.consumeSSELine(line)
	}
}

func (b *usageCaptureBody) consumeSSELine(line string) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	u := stats.ParseOpenAIUsage([]byte(payload))
	if u == nil || b.reported {
		return
	}
	b.reported = true
	if b.record != nil {
		b.record(u)
	}
}