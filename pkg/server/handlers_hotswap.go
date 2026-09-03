package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"llamactl/pkg/hotswap"
	"llamactl/pkg/manager"
)

// HotSwapHandler handles POST /api/v1/hot-swap
// Request: {"binary_path": "C:\\path\\to\\llamactl.new.exe"}
func (h *Handler) HotSwapHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Platform gate: Windows only.
		if runtime.GOOS != "windows" {
			writeError(w, http.StatusBadRequest, "hot-swap-unsupported", "hot-swap is not supported on this platform")
			return
		}

		var req struct {
			BinaryPath string `json:"binary_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid-request", err.Error())
			return
		}
		if req.BinaryPath == "" {
			writeError(w, http.StatusBadRequest, "missing-field", "binary_path is required")
			return
		}

		hm, ok := h.InstanceManager.(manager.HotSwapManager)
		if !ok {
			writeError(w, http.StatusNotImplemented, "not-implemented", "hot-swap not implemented")
			return
		}

		if err := hm.HotSwap(req.BinaryPath); err != nil {
			writeError(w, http.StatusInternalServerError, "hot-swap-failed", err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{
			"status": "complete",
		})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// After the response is on the wire, tell main to exit without
		// killing model children.
		hm.SignalHotSwapExit()
	}
}

// HotSwapStatusHandler handles GET /api/v1/hot-swap/status
func (h *Handler) HotSwapStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtime.GOOS != "windows" {
			writeJSON(w, http.StatusOK, map[string]any{"phase": "unsupported", "supported": false})
			return
		}

		dataDir := h.cfg.DataDir
		state, err := hotswap.ReadHandoffState(dataDir)
		if err != nil || state == nil {
			writeJSON(w, http.StatusOK, map[string]any{"phase": "idle", "supported": true})
			return
		}

		writeJSON(w, http.StatusOK, state)
	}
}

// HotSwapCandidatesHandler handles GET /api/v1/hot-swap/candidates
// Lists .exe files in the llamactl directory that could be hot-swapped in.
func (h *Handler) HotSwapCandidatesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if runtime.GOOS != "windows" {
			writeJSON(w, http.StatusOK, []string{})
			return
		}

		exePath, err := os.Executable()
		if err != nil {
			writeJSON(w, http.StatusOK, []string{})
			return
		}
		dir := filepath.Dir(exePath)
		entries, err := os.ReadDir(dir)
		if err != nil {
			writeJSON(w, http.StatusOK, []string{})
			return
		}

		var candidates []string
		self := strings.ToLower(exePath)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".exe") {
				continue
			}
			full := filepath.Join(dir, name)
			if strings.ToLower(full) == self {
				continue
			}
			candidates = append(candidates, full)
		}

		writeJSON(w, http.StatusOK, candidates)
	}
}
