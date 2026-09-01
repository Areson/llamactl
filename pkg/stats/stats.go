// Package stats extracts per-request throughput records from llama.cpp
// server logs. llama-server writes one block per completed generation to
// stdout (captured by llamactl into <name>.log):
//
//	... I slot print_timing: id  2 | task 3920 | prompt eval time =  1217.14 ms /  1018 tokens (   1.20 ms per token,   836.39 tokens per second)
//	... I slot print_timing: id  2 | task 3920 |        eval time =  3477.18 ms /   225 tokens (  15.52 ms per token,    64.42 tokens per second)
//
// The two lines share a slot id + task id; the first carries prompt
// (prefill) timing, the second carries generation (decode) timing. We
// parse both into a single ThroughputRecord per (slot, task).
package stats

import (
	"regexp"
	"sort"
	"strconv"
)

// ThroughputRecord is one completed generation, combining the prompt-eval
// and decode-eval timings reported by llama.cpp.
type ThroughputRecord struct {
	SlotID  int     `json:"slot_id"`
	TaskID  int     `json:"task_id"`
	CapturedAt string `json:"captured_at"` // wall-clock, set by the caller

	// Decode (generation) timing — the "model speed" people care about.
	GenTokens   int     `json:"gen_tokens"`
	GenTimeMs   float64 `json:"gen_time_ms"`
	GenPerTokMs float64 `json:"gen_per_token_ms"`
	GenPerSec   float64 `json:"gen_tokens_per_second"`

	// Prompt (prefill) timing.
	PromptTokens   int     `json:"prompt_tokens"`
	PromptTimeMs   float64 `json:"prompt_time_ms"`
	PromptPerTokMs float64 `json:"prompt_per_token_ms"`
	PromptPerSec   float64 `json:"prompt_tokens_per_second"`
}

// Aggregates are computed over the decode (gen) throughput only, since that
// is the stable "model speed" signal. Prompt speed is highly variable with
// cache hits and context length, so it's reported per-record, not averaged.
type Aggregates struct {
	Count   int     `json:"count"`
	AvgGen  float64 `json:"avg_gen_tokens_per_second"`
	MinGen  float64 `json:"min_gen_tokens_per_second"`
	MaxGen  float64 `json:"max_gen_tokens_per_second"`
	P95Gen  float64 `json:"p95_gen_tokens_per_second"`
	AvgGenMs float64 `json:"avg_gen_per_token_ms"`
}

// Stats is the API response shape for an instance's throughput history.
type Stats struct {
	Instance  string           `json:"instance"`
	Records   []ThroughputRecord `json:"records"` // most recent first
	Aggregates Aggregates      `json:"aggregates"`
}

var (
	reSlotTask = regexp.MustCompile(`print_timing:\s+id\s+(\d+)\s*\|\s*task\s+(\d+)\s*\|`)
	// Combined eval matcher: "prompt eval time = X ms / N tokens (a ms per token, b tokens per second)"
	// The optional "prompt " prefix distinguishes prefill (prompt) from decode (gen) timing.
	reEval = regexp.MustCompile(`(?:(prompt) )?eval time\s*=\s*([\d.]+)\s*ms\s*/\s*(\d+)\s*tokens\s*\(\s*([\d.]+)\s*ms per token,\s*([\d.]+)\s*tokens per second`)
)

// LineMatch is the result of matching one log line: the slot/task identity
// plus any eval timing it carries. Apply() merges the timing into a record.
type LineMatch struct {
	slotID int
	taskID int
	isEval bool
	prompt bool
	timeMs float64
	tokens int
	perTokMs float64
	perSec float64
}

func (m *LineMatch) Key() string {
	return strconv.Itoa(m.slotID) + "|" + strconv.Itoa(m.taskID)
}

// Record returns a fresh record seeded with this line's slot/task identity.
func (m *LineMatch) Record() *ThroughputRecord {
	return &ThroughputRecord{SlotID: m.slotID, TaskID: m.taskID}
}

// Apply merges this line's eval timing (if any) into an existing record.
func (m *LineMatch) Apply(rec *ThroughputRecord) {
	if !m.isEval {
		return
	}
	if m.prompt {
		rec.PromptTimeMs = m.timeMs
		rec.PromptTokens = m.tokens
		rec.PromptPerTokMs = m.perTokMs
		rec.PromptPerSec = m.perSec
	} else {
		rec.GenTimeMs = m.timeMs
		rec.GenTokens = m.tokens
		rec.GenPerTokMs = m.perTokMs
		rec.GenPerSec = m.perSec
	}
}

// MatchSlotTask extracts the slot/task identity and eval timing from a single
// log line, or returns nil if the line is not a timing line. Used by both the
// batch Parse() and the streaming reader so the two never drift.
func MatchSlotTask(line string) *LineMatch {
	st := reSlotTask.FindStringSubmatch(line)
	if st == nil {
		return nil
	}
	slotID, _ := strconv.Atoi(st[1])
	taskID, _ := strconv.Atoi(st[2])
	m := &LineMatch{slotID: slotID, taskID: taskID}

	if e := reEval.FindStringSubmatch(line); e != nil {
		m.isEval = true
		m.prompt = e[1] == "prompt"
		m.timeMs, _ = strconv.ParseFloat(e[2], 64)
		m.tokens, _ = strconv.Atoi(e[3])
		m.perTokMs, _ = strconv.ParseFloat(e[4], 64)
		m.perSec, _ = strconv.ParseFloat(e[5], 64)
	}
	return m
}

// Parse extracts throughput records from raw llama.cpp log lines.
//
// The log timestamp prefix on these lines is a sub-second offset (e.g.
// "14.51.311.835") that is not an absolute wall-clock time, so Parse leaves
// CapturedAt empty; the caller should stamp records with the time it read
// them (or infer from file mtime) for ordering/display. Records are returned
// in the order they appear in the log (oldest first).
func Parse(lines []string) []ThroughputRecord {
	partial := make(map[string]*ThroughputRecord)
	order := make([]string, 0)

	for _, line := range lines {
		m := MatchSlotTask(line)
		if m == nil {
			continue
		}
		key := m.Key()
		rec := partial[key]
		if rec == nil {
			rec = m.Record()
			partial[key] = rec
			order = append(order, key)
		}
		m.Apply(rec)
	}

	records := make([]ThroughputRecord, 0, len(order))
	for _, key := range order {
		rec := partial[key]
		if rec.GenPerSec > 0 || rec.GenTokens > 0 {
			records = append(records, *rec)
		}
	}
	return records
}

// ComputeAggregates summarizes the decode throughput across records.
func ComputeAggregates(records []ThroughputRecord) Aggregates {
	if len(records) == 0 {
		return Aggregates{}
	}

	gens := make([]float64, 0, len(records))
	ms := make([]float64, 0, len(records))
	for _, r := range records {
		if r.GenPerSec > 0 {
			gens = append(gens, r.GenPerSec)
			ms = append(ms, r.GenPerTokMs)
		}
	}
	if len(gens) == 0 {
		return Aggregates{Count: len(records)}
	}

	var sum, sumMs, min, max float64
	min, max = gens[0], gens[0]
	for i, g := range gens {
		sum += g
		sumMs += ms[i]
		if g < min {
			min = g
		}
		if g > max {
			max = g
		}
	}

	// P95: sort a copy, index at ceil(0.95*n)-1.
	sorted := append([]float64(nil), gens...)
	sort.Float64s(sorted)
	idx := int(float64(len(sorted))*0.95+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx > len(sorted)-1 {
		idx = len(sorted) - 1
	}
	p95 := sorted[idx]

	n := float64(len(gens))
	return Aggregates{
		Count:    len(records),
		AvgGen:   sum / n,
		MinGen:   min,
		MaxGen:   max,
		P95Gen:   p95,
		AvgGenMs: sumMs / n,
	}
}
