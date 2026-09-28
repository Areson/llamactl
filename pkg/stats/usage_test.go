package stats

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseOpenAIUsageTabbyShape(t *testing.T) {
	raw := []byte(`{
		"id": "chatcmpl-x",
		"usage": {
			"prompt_tokens": 57,
			"prompt_tokens_details": {"cached_tokens": 0},
			"prompt_time": 0.21,
			"prompt_tokens_per_sec": 271.43,
			"completion_tokens": 20,
			"completion_tokens_details": {"accepted_prediction_tokens": 11, "rejected_prediction_tokens": 30},
			"completion_time": 0.35,
			"completion_tokens_per_sec": 57.59,
			"total_tokens": 77,
			"total_time": 0.56
		}
	}`)
	u := ParseOpenAIUsage(raw)
	if u == nil {
		t.Fatal("expected usage")
	}
	if u.PromptTokens != 57 || u.CompletionTokens != 20 {
		t.Fatalf("tokens = %d/%d", u.PromptTokens, u.CompletionTokens)
	}
	if math.Abs(u.CompletionTokensPerSec-57.59) > 0.01 {
		t.Fatalf("completion_tokens_per_sec = %f", u.CompletionTokensPerSec)
	}
	if math.Abs(u.PromptTimeSec-0.21) > 0.001 {
		t.Fatalf("prompt_time = %f", u.PromptTimeSec)
	}
}

func TestParseOpenAIUsageNull(t *testing.T) {
	if ParseOpenAIUsage([]byte(`{"usage":null}`)) != nil {
		t.Fatal("null usage should yield nil")
	}
	if ParseOpenAIUsage([]byte(`{"choices":[]}`)) != nil {
		t.Fatal("missing usage should yield nil")
	}
	if ParseOpenAIUsage([]byte(`not-json`)) != nil {
		t.Fatal("invalid JSON should yield nil")
	}
}

func TestParseOpenAIUsageStringRates(t *testing.T) {
	raw := []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_per_sec":"40.5","completion_time":"0.123"}}`)
	u := ParseOpenAIUsage(raw)
	if u == nil {
		t.Fatal("expected usage")
	}
	if math.Abs(u.CompletionTokensPerSec-40.5) > 0.01 {
		t.Fatalf("rate = %f", u.CompletionTokensPerSec)
	}
	if math.Abs(u.CompletionTimeSec-0.123) > 0.001 {
		t.Fatalf("time = %f", u.CompletionTimeSec)
	}
}

func TestRecordFromUsage(t *testing.T) {
	u := &OpenAIUsage{
		PromptTokens:           57,
		PromptTimeSec:          0.21,
		PromptTokensPerSec:     271.43,
		CompletionTokens:       20,
		CompletionTimeSec:      0.35,
		CompletionTokensPerSec: 57.59,
	}
	at := time.UnixMilli(1790635472000).UTC()
	rec := RecordFromUsage(u, at)
	if rec == nil {
		t.Fatal("expected record")
	}
	if rec.SlotID != 0 {
		t.Fatalf("SlotID = %d, want 0 (proxy-derived)", rec.SlotID)
	}
	if rec.TaskID != 1790635472000 {
		t.Fatalf("TaskID = %d", rec.TaskID)
	}
	if rec.GenTokens != 20 || math.Abs(rec.GenPerSec-57.59) > 0.01 {
		t.Fatalf("gen = %+v", rec)
	}
	if rec.PromptTokens != 57 || math.Abs(rec.PromptPerSec-271.43) > 0.01 {
		t.Fatalf("prompt = %+v", rec)
	}
	if math.Abs(rec.GenTimeMs-350) > 0.1 {
		t.Fatalf("GenTimeMs = %f, want 350", rec.GenTimeMs)
	}
}

func TestRecordFromUsageTimeOnly(t *testing.T) {
	u := &OpenAIUsage{CompletionTokens: 100, CompletionTimeSec: 2.0}
	rec := RecordFromUsage(u, time.Now())
	if rec == nil {
		t.Fatal("expected record")
	}
	if math.Abs(rec.GenPerSec-50) > 0.01 {
		t.Fatalf("GenPerSec = %f, want 50", rec.GenPerSec)
	}
}

func TestRecordFromUsageNoTiming(t *testing.T) {
	if RecordFromUsage(&OpenAIUsage{CompletionTokens: 10}, time.Now()) != nil {
		t.Fatal("token counts without timing should not invent t/s")
	}
}

func TestFormatPrintTimingRoundTrip(t *testing.T) {
	u := &OpenAIUsage{
		PromptTokens:           8192,
		PromptTimeSec:          1.92,
		PromptTokensPerSec:     4266.67,
		CompletionTokens:       512,
		CompletionTimeSec:      4.31,
		CompletionTokensPerSec: 118.79,
	}
	rec := RecordFromUsage(u, time.UnixMilli(1790123216449).UTC())
	lines := FormatPrintTiming(*rec)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	for _, line := range lines {
		if !strings.Contains(line, "print_timing:") {
			t.Fatalf("bad line: %s", line)
		}
	}
	parsed := Parse(lines)
	if len(parsed) != 1 {
		t.Fatalf("Parse got %d records: %+v", len(parsed), parsed)
	}
	p := parsed[0]
	if p.SlotID != 0 || p.TaskID != 1790123216449 {
		t.Fatalf("identity %+v", p)
	}
	if p.GenTokens != 512 || math.Abs(p.GenPerSec-118.79) > 0.05 {
		t.Fatalf("gen %+v", p)
	}
	if p.PromptTokens != 8192 || math.Abs(p.PromptPerSec-4266.67) > 0.05 {
		t.Fatalf("prompt %+v", p)
	}
}

func TestFormatPrintTimingDecodeOnly(t *testing.T) {
	rec := ThroughputRecord{SlotID: 0, TaskID: 42, GenTokens: 8, GenTimeMs: 210, GenPerTokMs: 26.25, GenPerSec: 38.10}
	lines := FormatPrintTiming(rec)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %v", lines)
	}
	parsed := Parse(lines)
	if len(parsed) != 1 || parsed[0].GenTokens != 8 {
		t.Fatalf("parsed %+v", parsed)
	}
}

func TestParseLiveSlot0Lines(t *testing.T) {
	lines := []string{
		"print_timing: id 0 | task 1790635805710 | prompt eval time = 120.00 ms / 57 tokens (2.11 ms per token, 475.00 tokens per second)",
		"print_timing: id 0 | task 1790635805710 | eval time = 80.00 ms / 12 tokens (6.67 ms per token, 142.05 tokens per second)",
	}
	recs := Parse(lines)
	if len(recs) != 1 {
		t.Fatalf("got %d: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.PromptTokens != 57 {
		t.Fatalf("PromptTokens=%d want 57; full=%+v", r.PromptTokens, r)
	}
	if r.GenTokens != 12 || math.Abs(r.GenPerSec-142.05) > 0.01 {
		t.Fatalf("gen %+v", r)
	}
}
