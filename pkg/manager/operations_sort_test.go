package manager

import (
	"testing"

	"llamactl/pkg/config"
	"llamactl/pkg/instance"
)

// newTestManagerForList builds a minimal *instanceManager with just enough
// wiring for ListInstances to run without a database, real processes, or the
// lifecycle/timeout goroutines. ListInstances reads the registry and (for
// remote instances) the remote manager; with an empty node map every
// instance is local, so nothing is fetched over the wire.
func newTestManagerForList(t *testing.T) *instanceManager {
	t.Helper()

	cfg := &config.AppConfig{
		Instances: config.InstancesConfig{
			PortRange: [2]int{8000, 9000},
			// Large enough that instance.New() can't fail on port defaults.
			MaxInstances:         100,
			MaxRunningInstances:  100,
			TimeoutCheckInterval: 5,
		},
		LocalNode: "main",
		Nodes:     map[string]config.NodeConfig{},
	}

	registry := newInstanceRegistry()
	return &instanceManager{
		registry:     registry,
		ports:        newPortAllocator(8000, 9000),
		remote:       newRemoteManager(map[string]config.NodeConfig{}, 0),
		globalConfig: cfg,
		events:       newEventBus(),
	}
}

// TestListInstancesSortOrder verifies the running-first, then-name ordering
// that ListInstances applies (the fix for the UI card-shuffle).
func TestListInstancesSortOrder(t *testing.T) {
	m := newTestManagerForList(t)

	// Add instances with mixed running/stopped state and names that are NOT
	// already in alphabetical order, so the test would fail if unsorted.
	add := func(name string, running bool) {
		// Port 0 (no backend selected) keeps instance.New() from needing the
		// port allocator; ListInstances only reads status and name.
		opts := &instance.Options{}
		inst := instance.New(name, m.globalConfig, opts, nil)
		if running {
			inst.SetStatus(instance.Running)
		} else {
			inst.SetStatus(instance.Stopped)
		}
		if err := m.registry.add(inst); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		if running {
			m.registry.markRunning(name)
		}
	}

	// Insert in non-alphabetical order.
	add("zeta", false)   // stopped
	add("alpha", true)   // running
	add("mid", false)    // stopped
	add("beta", true)    // running
	add("omega", false)  // stopped

	list, err := m.ListInstances()
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}

	var got []string
	for _, i := range list {
		got = append(got, i.Name)
	}

	want := []string{"alpha", "beta", "mid", "omega", "zeta"}
	// running first (alpha, beta), then stopped alphabetical (mid, omega, zeta)
	if len(got) != len(want) {
		t.Fatalf("length: got %d want %d (got=%v)", len(got), len(want), got)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Fatalf("order: got %v want %v", got, want)
		}
	}
}
