package stats

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

// OpenAIUsage is the subset of OpenAI-compatible usage we care about for
// throughput cards. TabbyAPI extends the stock shape with prompt_time /
// completion_time (seconds) and *_tokens_per_sec; llama.cpp often omits
// those but may still send token counts.
type OpenAIUsage struct {
	PromptTokens           int
	CompletionTokens       int
	PromptTimeSec          float64
	CompletionTimeSec      float64
	PromptTokensPerSec     float64
	CompletionTokensPerSec float64
}

// ParseOpenAIUsage extracts usage from a JSON object that may be a full
// chat.completion response, a streaming chunk, or a bare {"usage": ...}.
// Returns nil when no usable usage is present.
func ParseOpenAIUsage(data []byte) *OpenAIUsage {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	raw, ok := root["usage"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	u := &OpenAIUsage{
		PromptTokens:           jsonInt(fields["prompt_tokens"]),
		CompletionTokens:       jsonInt(fields["completion_tokens"]),
		PromptTimeSec:          jsonFloat(fields["prompt_time"]),
		CompletionTimeSec:      jsonFloat(fields["completion_time"]),
		PromptTokensPerSec:     jsonFloat(fields["prompt_tokens_per_sec"]),
		CompletionTokensPerSec: jsonFloat(fields["completion_tokens_per_sec"]),
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 &&
		u.CompletionTokensPerSec == 0 && u.CompletionTimeSec == 0 {
		return nil
	}
	return u
}

// RecordFromUsage builds a ThroughputRecord from OpenAI-compatible usage.
// Returns nil when decode timing cannot be derived (no t/s and no time).
// SlotID is 0 so proxy-derived rows are distinguishable from Tabby's
// TABBYAPI_LLAMACTL_TIMING emitter (which uses slot 1).
func RecordFromUsage(u *OpenAIUsage, at time.Time) *ThroughputRecord {
	if u == nil {
		return nil
	}
	genTokens := u.CompletionTokens
	genPerSec := u.CompletionTokensPerSec
	genTimeMs := u.CompletionTimeSec * 1000
	if genPerSec <= 0 && genTimeMs > 0 && genTokens > 0 {
		genPerSec = float64(genTokens) / (genTimeMs / 1000)
	}
	if genPerSec <= 0 && genTokens <= 0 {
		return nil
	}
	// Still require a rate (or enough to compute one) so the UI gets t/s.
	if genPerSec <= 0 {
		return nil
	}
	if genTimeMs <= 0 && genTokens > 0 {
		genTimeMs = float64(genTokens) / genPerSec * 1000
	}
	genPerTokMs := 0.0
	if genTokens > 0 && genTimeMs > 0 {
		genPerTokMs = genTimeMs / float64(genTokens)
	} else if genPerSec > 0 {
		genPerTokMs = 1000 / genPerSec
	}

	rec := &ThroughputRecord{
		SlotID:      0,
		TaskID:      int(at.UnixMilli()),
		CapturedAt:  at.UTC().Format(time.RFC3339Nano),
		GenTokens:   genTokens,
		GenTimeMs:   round2(genTimeMs),
		GenPerTokMs: round2(genPerTokMs),
		GenPerSec:   round2(genPerSec),
	}

	promptTokens := u.PromptTokens
	promptPerSec := u.PromptTokensPerSec
	promptTimeMs := u.PromptTimeSec * 1000
	if promptPerSec <= 0 && promptTimeMs > 0 && promptTokens > 0 {
		promptPerSec = float64(promptTokens) / (promptTimeMs / 1000)
	}
	if promptTokens > 0 && (promptPerSec > 0 || promptTimeMs > 0) {
		if promptTimeMs <= 0 && promptPerSec > 0 {
			promptTimeMs = float64(promptTokens) / promptPerSec * 1000
		}
		promptPerTokMs := 0.0
		if promptTokens > 0 && promptTimeMs > 0 {
			promptPerTokMs = promptTimeMs / float64(promptTokens)
		} else if promptPerSec > 0 {
			promptPerTokMs = 1000 / promptPerSec
		}
		rec.PromptTokens = promptTokens
		rec.PromptTimeMs = round2(promptTimeMs)
		rec.PromptPerTokMs = round2(promptPerTokMs)
		rec.PromptPerSec = round2(promptPerSec)
	}
	return rec
}

// FormatPrintTiming renders a record as llama.cpp-compatible print_timing
// lines so the existing log scraper (and /stats) can pick them up without a
// parallel store. Matches pkg/stats MatchSlotTask / reEval.
func FormatPrintTiming(rec ThroughputRecord) []string {
	lines := make([]string, 0, 2)
	if rec.PromptTokens > 0 || rec.PromptTimeMs > 0 {
		lines = append(lines, fmt.Sprintf(
			"print_timing: id %d | task %d | prompt eval time = %.2f ms / %d tokens (%.2f ms per token, %.2f tokens per second)",
			rec.SlotID, rec.TaskID, rec.PromptTimeMs, rec.PromptTokens, rec.PromptPerTokMs, rec.PromptPerSec,
		))
	}
	lines = append(lines, fmt.Sprintf(
		"print_timing: id %d | task %d | eval time = %.2f ms / %d tokens (%.2f ms per token, %.2f tokens per second)",
		rec.SlotID, rec.TaskID, rec.GenTimeMs, rec.GenTokens, rec.GenPerTokMs, rec.GenPerSec,
	))
	return lines
}

func jsonInt(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int(f)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, _ := strconv.Atoi(s)
		return v
	}
	return 0
}

func jsonFloat(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	return 0
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}