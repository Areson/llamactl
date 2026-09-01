package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"llamactl/pkg/stats"
	"llamactl/pkg/validation"

	"github.com/go-chi/chi/v5"
)

// GetInstanceStats returns per-request throughput history parsed from the
// instance's llama.cpp log, plus rolling aggregates over the decode (gen)
// throughput. This is the source for the model-throughput card widget and for
// long-term model speed / configuration evaluation.
//
// @Summary Get instance throughput stats
// @Description Parses llama.cpp per-request timing lines (prompt eval + decode) from the instance log and returns the most recent generations plus aggregates.
// @Tags Instances
// @Security ApiKeyAuth
// @Accept json
// @Produce json
// @Param name path string true "Instance Name"
// @Param limit query int false "Max records to return (default 50, max 500)"
// @Success 200 {object} stats.Stats
// @Failure 400 {string} string "Invalid name or parameter"
// @Failure 404 {string} string "Instance not found or no local log"
// @Router /api/v1/instances/{name}/stats [get]
func (h *Handler) GetInstanceStats() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		validatedName, err := validation.ValidateInstanceName(name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_instance_name", err.Error())
			return
		}

		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			parsed, aerr := strconv.Atoi(l)
			if aerr != nil {
				writeError(w, http.StatusBadRequest, "invalid_parameter", "Invalid limit parameter: "+aerr.Error())
				return
			}
			if parsed < 1 {
				limit = 1
			}
			if parsed > 500 {
				limit = 500
			}
			limit = parsed
		}

		// Resolve the local log file. Remote instances have no local log.
		logPath, err := h.InstanceManager.GetInstanceLogPath(validatedName)
		if err != nil {
			writeError(w, http.StatusNotFound, "instance_not_found", "Instance not found: "+err.Error())
			return
		}
		if logPath == "" {
			writeJSON(w, http.StatusOK, stats.Stats{
				Instance: validatedName,
				Records:  []stats.ThroughputRecord{},
				Aggregates: stats.Aggregates{},
			})
			return
		}
		if _, statErr := os.Stat(logPath); os.IsNotExist(statErr) {
			// Log not yet written (instance never ran locally). Return empty, not an error.
			writeJSON(w, http.StatusOK, stats.Stats{
				Instance: validatedName,
				Records:  []stats.ThroughputRecord{},
				Aggregates: stats.Aggregates{},
			})
			return
		}

		records, err := readThroughputRecords(logPath, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "stats_failed", "Failed to parse instance log: "+err.Error())
			return
		}

		// Stamp captured-at from the file's last-modified time as a coarse
		// upper bound (the per-line timestamps are sub-second offsets, not
		// absolute wall-clock). Good enough for ordering + display.
		if fi, fiErr := os.Stat(logPath); fiErr == nil {
			for i := range records {
				records[i].CapturedAt = fi.ModTime().Format(time.RFC3339)
			}
		}

		out := stats.Stats{
			Instance:   validatedName,
			Records:    records,
			Aggregates: stats.ComputeAggregates(records),
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// readThroughputRecords reads a log file, keeps only the most recent `limit`
// throughput records (newest first), and returns them. It scans the whole file
// (the parser is linear and cheap) but only materializes the tail, which keeps
// memory bounded for large logs.
func readThroughputRecords(logPath string, limit int) ([]stats.ThroughputRecord, error) {
	f, err := os.Open(filepath.Clean(logPath))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// We need the LAST `limit` records. Strategy: parse all timing lines in a
	// single pass, but only keep a rolling window of the most recent records so
	// memory stays O(limit) regardless of file size.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)

	// Rolling window keyed by slot|task, preserving first-seen order.
	window := make(map[string]*stats.ThroughputRecord)
	order := make([]string, 0, limit)

	seenRecords := 0
	for sc.Scan() {
		line := sc.Text()
		st := stats.MatchSlotTask(line)
		if st == nil {
			continue
		}
		key := st.Key()

		rec := window[key]
		if rec == nil {
			rec = st.Record()
			window[key] = rec
			order = append(order, key)
			seenRecords++

			// Evict the oldest key once we exceed the window, so the map and
			// slice both stay bounded at ~`limit` entries.
			if len(order) > limit {
				oldest := order[0]
				delete(window, oldest)
				// Compact the slice (order is small; a simple copy suffices).
				order = append(order[:0], order[1:]...)
			}
		} else {
			st.Apply(rec)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	records := make([]stats.ThroughputRecord, 0, len(order))
	for _, key := range order {
		if rec, ok := window[key]; ok {
			if rec.GenPerSec > 0 || rec.GenTokens > 0 {
				records = append(records, *rec)
			}
		}
	}

	// Return newest first.
	out := make([]stats.ThroughputRecord, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		out = append(out, records[i])
	}
	_ = seenRecords
	return out, nil
}

// jsonRoundTrip is a small helper to keep the writeJSON path explicit in tests.
func jsonRoundTrip(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
