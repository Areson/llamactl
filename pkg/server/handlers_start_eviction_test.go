package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"llamactl/pkg/instance"

	"github.com/go-chi/chi/v5"
)

func postInstanceAction(t *testing.T, h *Handler, action, name string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/api/v1/instances/{name}", func(r chi.Router) {
		r.Post("/start", h.StartInstance())
		r.Post("/restart", h.RestartInstance())
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/"+name+"/"+action, nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestManualStart_EvictGateOff_NoEviction(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Instances.EnableLRUEviction = true
	cfg.Instances.EvictOnManualStart = false
	cfg.Instances.GroupLimits = map[string]int{"g": 1}

	fake := newFakeInstanceManager()
	host, port := healthServer(t)
	running := newOnDemandInstance(t, &cfg, "victim", "g", host, port)
	running.SetStatus(instance.Running)
	running.UpdateLastRequestTime()
	newcomer := newOnDemandInstance(t, &cfg, "newcomer", "g", host, port)
	fake.add(running)
	fake.add(newcomer)

	h := &Handler{InstanceManager: fake, cfg: cfg}
	rr := postInstanceAction(t, h, "start", "newcomer")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(&fake.evictCalls); got != 0 {
		t.Errorf("evictCalls=%d; want 0 when evict_on_manual_start is false", got)
	}
	if !newcomer.IsRunning() {
		t.Error("newcomer should be running")
	}
	if !running.IsRunning() {
		t.Error("victim should still be running (no eviction)")
	}
}

func TestManualStart_EvictGateOn_EvictsFromGroup(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Instances.EnableLRUEviction = true
	cfg.Instances.EvictOnManualStart = true
	cfg.Instances.GroupLimits = map[string]int{"g": 1}

	fake := newFakeInstanceManager()
	host, port := healthServer(t)
	victim := newOnDemandInstance(t, &cfg, "victim", "g", host, port)
	victim.SetStatus(instance.Running)
	victim.UpdateLastRequestTime()
	time.Sleep(2 * time.Millisecond) // ensure newer last-request on newcomer path
	newcomer := newOnDemandInstance(t, &cfg, "newcomer", "g", host, port)
	fake.add(victim)
	fake.add(newcomer)

	h := &Handler{InstanceManager: fake, cfg: cfg}
	rr := postInstanceAction(t, h, "start", "newcomer")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(&fake.evictCalls); got != 1 {
		t.Errorf("evictCalls=%d; want 1 when evict_on_manual_start is true", got)
	}
	if !newcomer.IsRunning() {
		t.Error("newcomer should be running")
	}
	if victim.IsRunning() {
		t.Error("victim should have been evicted")
	}
}

func TestPrepareCapacity_SyncGroupEvictionWaitsForFullStop(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Instances.EnableLRUEviction = true
	cfg.Instances.SynchronousGroupEviction = true
	cfg.Instances.SynchronousGroupEvictionTimeoutSec = 5
	cfg.Instances.GroupLimits = map[string]int{"g": 1}

	fake := newFakeInstanceManager()
	fake.stopDelay = 150 * time.Millisecond
	host, port := healthServer(t)
	victim := newOnDemandInstance(t, &cfg, "victim", "g", host, port)
	victim.SetStatus(instance.Running)
	victim.UpdateLastRequestTime()
	newcomer := newOnDemandInstance(t, &cfg, "newcomer", "g", host, port)
	fake.add(victim)
	fake.add(newcomer)

	var evictDoneBeforeStart atomic.Bool
	fake.onEvictDone = func() {
		// Mark that full stop finished; onStart checks this.
	}
	fake.onStart = func() {
		// Victim must be fully Stopped (not just ShuttingDown) when sync waits.
		if victim.GetStatus() == instance.Stopped {
			evictDoneBeforeStart.Store(true)
		}
	}

	h := &Handler{InstanceManager: fake, cfg: cfg}
	h.startMu.Lock()
	if err := h.prepareCapacityForStart(newcomer); err != nil {
		h.startMu.Unlock()
		t.Fatalf("prepareCapacityForStart: %v", err)
	}
	if _, err := h.InstanceManager.StartInstance(newcomer.Name); err != nil {
		h.startMu.Unlock()
		t.Fatalf("StartInstance: %v", err)
	}
	h.startMu.Unlock()

	if !evictDoneBeforeStart.Load() {
		t.Fatal("expected Start to run only after victim fully Stopped under sync group eviction")
	}
}

func TestPrepareCapacity_SyncGroupEvictionTimeoutFallsBackToAsync(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Instances.EnableLRUEviction = true
	cfg.Instances.SynchronousGroupEviction = true
	cfg.Instances.SynchronousGroupEvictionTimeoutSec = 1
	cfg.Instances.GroupLimits = map[string]int{"g": 1}

	fake := newFakeInstanceManager()
	fake.stopDelay = 3 * time.Second // longer than sync timeout
	host, port := healthServer(t)
	victim := newOnDemandInstance(t, &cfg, "victim", "g", host, port)
	victim.SetStatus(instance.Running)
	victim.UpdateLastRequestTime()
	newcomer := newOnDemandInstance(t, &cfg, "newcomer", "g", host, port)
	fake.add(victim)
	fake.add(newcomer)

	var startWhileStillTeardown atomic.Bool
	fake.onStart = func() {
		// After timeout fallback, start proceeds before EvictLRU finishes
		// (victim still ShuttingDown during stopDelay).
		if victim.GetStatus() == instance.ShuttingDown {
			startWhileStillTeardown.Store(true)
		}
	}

	h := &Handler{InstanceManager: fake, cfg: cfg}
	start := time.Now()
	h.startMu.Lock()
	if err := h.prepareCapacityForStart(newcomer); err != nil {
		h.startMu.Unlock()
		t.Fatalf("prepareCapacityForStart: %v", err)
	}
	if _, err := h.InstanceManager.StartInstance(newcomer.Name); err != nil {
		h.startMu.Unlock()
		t.Fatalf("StartInstance: %v", err)
	}
	h.startMu.Unlock()
	elapsed := time.Since(start)

	if elapsed >= 2500*time.Millisecond {
		t.Fatalf("prepare+start took %v; expected timeout fallback ~1s, not full 3s stop", elapsed)
	}
	if !startWhileStillTeardown.Load() {
		t.Fatal("expected Start during victim ShuttingDown after sync timeout fallback")
	}
	// Let background eviction finish so the test doesn't leak goroutines racing SetStatus.
	time.Sleep(3500 * time.Millisecond)
}

func TestRestart_NoPeerEviction(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Instances.EnableLRUEviction = true
	cfg.Instances.EvictOnManualStart = true // even with this on, Restart must not peer-evict
	cfg.Instances.GroupLimits = map[string]int{"g": 1}

	fake := newFakeInstanceManager()
	host, port := healthServer(t)
	peer := newOnDemandInstance(t, &cfg, "peer", "g", host, port)
	peer.SetStatus(instance.Running)
	peer.UpdateLastRequestTime()
	self := newOnDemandInstance(t, &cfg, "self", "g", host, port)
	self.SetStatus(instance.Running)
	self.UpdateLastRequestTime()
	fake.add(peer)
	fake.add(self)

	h := &Handler{InstanceManager: fake, cfg: cfg}
	rr := postInstanceAction(t, h, "restart", "self")
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(&fake.evictCalls); got != 0 {
		t.Errorf("evictCalls=%d; want 0 (Restart must not peer-evict)", got)
	}
	if !self.IsRunning() {
		t.Error("self should be running after restart")
	}
	if !peer.IsRunning() {
		t.Error("peer must remain running (no LRU on Restart)")
	}
	if got := atomic.LoadInt32(&fake.stopCalls); got < 1 {
		t.Errorf("stopCalls=%d; want >= 1 (stop-self)", got)
	}
	if got := atomic.LoadInt32(&fake.startCalls); got < 1 {
		t.Errorf("startCalls=%d; want >= 1 (start-self)", got)
	}
}
