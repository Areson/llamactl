package stats

import (
	"math"
	"testing"
)

func sampleLines() []string {
	return []string{
		"14.51.311.835 I slot print_timing: id  2 | task 3626 | n_gen =    198, tg =  65.04 t/s, tg_3s =  65.36 t/s",
		"14.53.328.159 I slot print_timing: id  2 | task 3626 | prompt eval time =   34315.35 ms / 53428 tokens (    0.64 ms per token,  1556.97 tokens per second)",
		"14.53.328.164 I slot print_timing: id  2 | task 3626 |        eval time =    5045.27 ms /   336 tokens (   15.06 ms per token,    66.40 tokens per second)",
		"14.57.447.884 I slot print_timing: id  2 | task 3790 | prompt eval time =     948.68 ms /   730 tokens (    1.30 ms per token,   769.49 tokens per second)",
		"14.57.447.889 I slot print_timing: id  2 | task 3790 |        eval time =    2346.14 ms /   162 tokens (   14.57 ms per token,    68.62 tokens per second)",
		// Non-timing lines that must be ignored.
		"15.07.621.847 I slot      release: id  0 | task 5199 | stop processing: n_tokens = 95720, truncated = 0",
		"20.45.202.340 I slot print_timing: id  0 | task 5199 |    graphs reused =       4230",
		"20.45.204.981 I slot      release: id  0 | task 5199 | draft acceptance = 0.79798",
	}
}

func TestParsePairsPromptAndDecode(t *testing.T) {
	recs := Parse(sampleLines())
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}

	// First record: task 3626.
	r := recs[0]
	if r.SlotID != 2 || r.TaskID != 3626 {
		t.Errorf("wrong slot/task: %+v", r)
	}
	if r.GenTokens != 336 {
		t.Errorf("GenTokens = %d, want 336", r.GenTokens)
	}
	if math.Abs(r.GenPerSec-66.40) > 0.01 {
		t.Errorf("GenPerSec = %f, want ~66.40", r.GenPerSec)
	}
	if math.Abs(r.GenPerTokMs-15.06) > 0.01 {
		t.Errorf("GenPerTokMs = %f, want ~15.06", r.GenPerTokMs)
	}
	if r.PromptTokens != 53428 {
		t.Errorf("PromptTokens = %d, want 53428", r.PromptTokens)
	}
	if math.Abs(r.PromptPerSec-1556.97) > 0.01 {
		t.Errorf("PromptPerSec = %f, want ~1556.97", r.PromptPerSec)
	}
}

func TestParseIgnoresNonTimingLines(t *testing.T) {
	recs := Parse([]string{
		"x I slot print_timing: id 0 | task 1 |    graphs reused =       4230",
		"x I slot      release: id  0 | task 1 | stop processing: n_tokens = 100, truncated = 0",
	})
	if len(recs) != 0 {
		t.Fatalf("expected 0 records, got %d: %+v", len(recs), recs)
	}
}

func TestParseHandlesDecodeOnly(t *testing.T) {
	recs := Parse([]string{
		"x I slot print_timing: id  1 | task 9 |        eval time =    1000.00 ms /   100 tokens (   10.00 ms per token,    100.00 tokens per second)",
	})
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].GenPerSec != 100.0 {
		t.Errorf("GenPerSec = %f, want 100", recs[0].GenPerSec)
	}
	if recs[0].PromptTokens != 0 {
		t.Errorf("PromptTokens should be 0, got %d", recs[0].PromptTokens)
	}
}

func TestComputeAggregates(t *testing.T) {
	recs := []ThroughputRecord{
		{GenPerSec: 60}, {GenPerSec: 70}, {GenPerSec: 100}, {GenPerSec: 120},
	}
	agg := ComputeAggregates(recs)
	if agg.Count != 4 {
		t.Errorf("Count = %d, want 4", agg.Count)
	}
	if math.Abs(agg.AvgGen-87.5) > 0.01 {
		t.Errorf("AvgGen = %f, want 87.5", agg.AvgGen)
	}
	if agg.MinGen != 60 || agg.MaxGen != 120 {
		t.Errorf("Min/Max = %f/%f, want 60/120", agg.MinGen, agg.MaxGen)
	}
	// P95 of [60,70,100,120]: idx = round(0.95*4)-1 = 4-1 = 3 -> 120.
	if agg.P95Gen != 120 {
		t.Errorf("P95 = %f, want 120", agg.P95Gen)
	}
}

func TestComputeAggregatesEmpty(t *testing.T) {
	agg := ComputeAggregates(nil)
	if agg.Count != 0 || agg.AvgGen != 0 {
		t.Errorf("expected zeroed aggregates, got %+v", agg)
	}
}

func TestComputeAggregatesSingle(t *testing.T) {
	agg := ComputeAggregates([]ThroughputRecord{{GenPerSec: 42}})
	if agg.AvgGen != 42 || agg.MinGen != 42 || agg.MaxGen != 42 || agg.P95Gen != 42 {
		t.Errorf("single-value aggregates wrong: %+v", agg)
	}
}
