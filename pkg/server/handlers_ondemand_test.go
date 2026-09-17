package server

// PR #252 test coverage: on-demand start serialization (startMu + re-check
// under lock), group quota check-then-act, and the concurrent double-spawn
// guard. White-box: calls ensureInstanceRunning directly.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llamactl/pkg/backends"
	"llamactl/pkg/config"
	"llamactl/pkg/instance"
	"llamactl/pkg/manager"
)

// --- fakes -----------------------------------------------------------------

// fakeInstanceManager implements manager.InstanceManager for handler tests.
// StartInstance simulates a successful backend start by flipping the
// instance status to Running (the real health endpoint is served by a
// per-test httptest server, so WaitForHealthy succeeds without a model).
type fakeInstanceManager struct {
	mu         sync.Mutex
	instances  map[string]*instance.Instance
	maxRunning int // 0 = unlimited

	startCalls int32 // atomic
	evictCalls int32 // atomic
	startDelay time.Duration
}

func newFakeInstanceManager() *fakeInstanceManager {
	return &fakeInstanceManager{instances: map[string]*instance.Instance{}}
}

func (f *fakeInstanceManager) add(inst *instance.Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[inst.Name] = inst
}

func (f *fakeInstanceManager) ListInstances() ([]*instance.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*instance.Instance, 0, len(f.instances))
	for _, inst := range f.instances {
		out = append(out, inst)
	}
	return out, nil
}

func (f *fakeInstanceManager) CreateInstance(name string, options *instance.Options) (*instance.Instance, error) {
	return nil, fmt.Errorf("not implemented: %s", name)
}

func (f *fakeInstanceManager) GetInstance(name string) (*instance.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.instances[name]
	if !ok {
		return nil, fmt.Errorf("instance %q not found", name)
	}
	return inst, nil
}

func (f *fakeInstanceManager) UpdateInstance(name string, options *instance.Options) (*instance.Instance, error) {
	return nil, fmt.Errorf("not implemented: %s", name)
}

func (f *fakeInstanceManager) DeleteInstance(name string) error {
	return fmt.Errorf("not implemented: %s", name)
}

func (f *fakeInstanceManager) StartInstance(name string) (*instance.Instance, error) {
	atomic.AddInt32(&f.startCalls, 1)
	if f.startDelay > 0 {
		time.Sleep(f.startDelay)
	}
	f.mu.Lock()
	inst := f.instances[name]
	f.mu.Unlock()
	if inst == nil {
		return nil, fmt.Errorf("unknown instance %q", name)
	}
	inst.SetStatus(instance.Running)
	inst.UpdateLastRequestTime()
	return inst, nil
}

func (f *fakeInstanceManager) AtMaxRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxRunning <= 0 {
		return false
	}
	n := 0
	for _, inst := range f.instances {
		if inst.IsRunning() {
			n++
		}
	}
	return n >= f.maxRunning
}

func (f *fakeInstanceManager) CountRunningInGroup(group string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, inst := range f.instances {
		if inst.GetOptions().Group == group && inst.IsRunning() {
			n++
		}
	}
	return n
}

func (f *fakeInstanceManager) StopInstance(name string) (*instance.Instance, error) {
	f.mu.Lock()
	inst := f.instances[name]
	f.mu.Unlock()
	if inst == nil {
		return nil, fmt.Errorf("unknown instance %q", name)
	}
	inst.SetStatus(instance.Stopped)
	return inst, nil
}

// EvictLRUInstance stops the oldest-running instance in the group.
func (f *fakeInstanceManager) EvictLRUInstance(group string) error {
	atomic.AddInt32(&f.evictCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	var victim *instance.Instance
	for _, inst := range f.instances {
		if group != "" && inst.GetOptions().Group != group {
			continue
		}
		if !inst.IsRunning() {
			continue
		}
		if victim == nil || inst.LastRequestTime() < victim.LastRequestTime() {
			victim = inst
		}
	}
	if victim == nil {
		return fmt.Errorf("no running instance to evict in group %q", group)
	}
	victim.SetStatus(instance.Stopped)
	return nil
}

func (f *fakeInstanceManager) RestartInstance(name string) (*instance.Instance, error) {
	return nil, fmt.Errorf("not implemented: %s", name)
}

func (f *fakeInstanceManager) GetInstanceLogs(name string, numLines int) (string, error) {
	return "", nil
}

func (f *fakeInstanceManager) GetInstanceLogPath(name string) (string, error) {
	return "test.log", nil
}

func (f *fakeInstanceManager) Shutdown() {}

func (f *fakeInstanceManager) Subscribe() manager.EventSubscriber {
	return manager.EventSubscriber{}
}

// --- helpers -----------------------------------------------------------------

// healthServer returns an httptest server answering 200 for every request
// (enough for waitForHealthy's /health poll) plus its host/port.
func healthServer(t *testing.T) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	h, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	portN, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}
	return h, portN
}

func baseTestConfig() config.AppConfig {
	logsDir, err := os.MkdirTemp("", "llamactl-handler-test")
	if err != nil {
		logsDir = "/tmp/llamactl-handler-test"
	}
	return config.AppConfig{
		Backends: config.BackendConfig{
			LlamaCpp: config.BackendSettings{Command: "llama-server"},
		},
		Instances: config.InstancesConfig{
			LogsDir:              logsDir,
			OnDemandStartTimeout: 10,
		},
		Nodes:     map[string]config.NodeConfig{},
		LocalNode: "main",
	}
}

func newOnDemandInstance(t *testing.T, cfg *config.AppConfig, name, group, host string, port int) *instance.Instance {
	t.Helper()
	onDemand := true
	opts := &instance.Options{
		OnDemandStart: &onDemand,
		Group:         group,
		BackendOptions: backends.Options{
			BackendType: backends.BackendTypeLlamaCpp,
			LlamaServerOptions: &backends.LlamaServerOptions{
				Model: "small-test-model.gguf",
				Host:  host,
				Port:  port,
			},
		},
	}
	return instance.New(name, cfg, opts, func(oldStatus, newStatus instance.Status) {})
}

// --- tests -------------------------------------------------------------------

func TestEnsureInstanceRunning_CanStartFalse_ReturnsNotRunning(t *testing.T) {
	cfg := baseTestConfig()
	fake := newFakeInstanceManager()
	host, port := healthServer(t)
	inst := newOnDemandInstance(t, &cfg, "od-canstart", "", host, port)
	fake.add(inst)

	h := &Handler{InstanceManager: fake, cfg: cfg}
	err := h.ensureInstanceRunning(inst, false, true)
	if !errors.Is(err, ErrInstanceNotRunning) {
		t.Fatalf("expected ErrInstanceNotRunning, got %v", err)
	}
	if got := atomic.LoadInt32(&fake.startCalls); got != 0 {
		t.Errorf("StartInstance called %d times; want 0", got)
	}
}

func TestEnsureInstanceRunning_OnDemandDisabled_ReturnsError(t *testing.T) {
	cfg := baseTestConfig()
	fake := newFakeInstanceManager()
	host, port := healthServer(t)

	// Same builder but OnDemandStart=false.
	noOnDemand := false
	opts := &instance.Options{
		OnDemandStart: &noOnDemand,
		BackendOptions: backends.Options{
			BackendType: backends.BackendTypeLlamaCpp,
			LlamaServerOptions: &backends.LlamaServerOptions{
				Model: "small-test-model.gguf",
				Host:  host,
				Port:  port,
			},
		},
	}
	inst := instance.New("od-disabled", &cfg, opts, func(oldStatus, newStatus instance.Status) {})
	fake.add(inst)

	h := &Handler{InstanceManager: fake, cfg: cfg}
	err := h.ensureInstanceRunning(inst, true, true)
	if err == nil {
		t.Fatal("expected error when on-demand start is disabled, got nil")
	}
	if errors.Is(err, ErrInstanceNotRunning) || errors.Is(err, ErrMaxInstancesReached) {
		t.Errorf("expected a generic on-demand error, got sentinel %v", err)
	}
	if got := atomic.LoadInt32(&fake.startCalls); got != 0 {
		t.Errorf("StartInstance called %d times; want 0", got)
	}
}

func TestEnsureInstanceRunning_StartsWhenStopped(t *testing.T) {
	cfg := baseTestConfig()
	fake := newFakeInstanceManager()
	host, port := healthServer(t)
	inst := newOnDemandInstance(t, &cfg, "od-happy", "", host, port)
	fake.add(inst)

	h := &Handler{InstanceManager: fake, cfg: cfg}
	if err := h.ensureInstanceRunning(inst, true, false); err != nil {
		t.Fatalf("ensureInstanceRunning: %v", err)
	}
	if got := atomic.LoadInt32(&fake.startCalls); got != 1 {
		t.Errorf("StartInstance called %d times; want 1", got)
	}
	if !inst.IsRunning() {
		t.Error("instance should be running after ensure")
	}
}

// TestEnsureInstanceRunning_ConcurrentStartsOnlyOnce is the core PR #252
// double-spawn guard: N concurrent requests for the same stopped on-demand
// instance must result in exactly one StartInstance call. Without startMu the
// check-then-act races and StartInstance is called N times.
func TestEnsureInstanceRunning_ConcurrentStartsOnlyOnce(t *testing.T) {
	cfg := baseTestConfig()
	fake := newFakeInstanceManager()
	fake.startDelay = 100 * time.Millisecond // widen the race window
	host, port := healthServer(t)
	inst := newOnDemandInstance(t, &cfg, "od-race", "", host, port)
	fake.add(inst)

	h := &Handler{InstanceManager: fake, cfg: cfg}

	const n = 8
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = h.ensureInstanceRunning(inst, true, false)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&fake.startCalls); got != 1 {
		t.Errorf("StartInstance called %d times; want exactly 1 (double-spawn guard)", got)
	}
	if !inst.IsRunning() {
		t.Error("instance should be running after concurrent ensure")
	}
}

// TestEnsureInstanceRunning_GroupQuotaNeverExceeded runs two concurrent
// on-demand starts in the same group with a group limit of 1. The invariant
// that must hold no matter the interleaving: at most one instance in the
// group is running afterwards. (One of the two requests may legitimately lose
// the race and be evicted; both starting would be the pre-#252 bug.)
func TestEnsureInstanceRunning_GroupQuotaNeverExceeded(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Instances.EnableLRUEviction = true
	cfg.Instances.GroupLimits = map[string]int{"testgroup": 1}

	fake := newFakeInstanceManager()
	fake.startDelay = 50 * time.Millisecond
	host, port := healthServer(t)
	instA := newOnDemandInstance(t, &cfg, "od-group-a", "testgroup", host, port)
	instB := newOnDemandInstance(t, &cfg, "od-group-b", "testgroup", host, port)
	fake.add(instA)
	fake.add(instB)

	h := &Handler{InstanceManager: fake, cfg: cfg}

	insts := []*instance.Instance{instA, instB}
	errs := make([]error, len(insts))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, inst := range insts {
		wg.Add(1)
		go func(i int, inst *instance.Instance) {
			defer wg.Done()
			<-start
			errs[i] = h.ensureInstanceRunning(inst, true, true)
		}(i, inst)
	}
	close(start)
	wg.Wait()

	running := 0
	for _, inst := range insts {
		if inst.IsRunning() {
			running++
		}
	}
	if running > 1 {
		t.Errorf("%d instances running in group with limit 1; want <= 1", running)
	}
	if running < 1 {
		t.Errorf("no instance running after concurrent starts; want >= 1 (errors: %v)", errs)
	}
}
