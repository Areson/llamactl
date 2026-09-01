package manager

import (
	"testing"
	"time"

	"llamactl/pkg/instance"
)

func TestEventBus_SubscribeAndBroadcast(t *testing.T) {
	b := newEventBus()

	sub := b.Subscribe()
	defer sub.Unsubscribe()

	// Broadcast an event and verify it arrives on the subscriber channel.
	b.Broadcast(InstanceEvent{
		Name:      "test-instance",
		OldStatus: instance.Stopped,
		NewStatus: instance.Running,
	})

	select {
	case ev := <-sub.Ch:
		if ev.Name != "test-instance" {
			t.Errorf("expected name test-instance, got %q", ev.Name)
		}
		if ev.OldStatus != instance.Stopped {
			t.Errorf("expected old status stopped, got %v", ev.OldStatus)
		}
		if ev.NewStatus != instance.Running {
			t.Errorf("expected new status running, got %v", ev.NewStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	b := newEventBus()

	sub1 := b.Subscribe()
	sub2 := b.Subscribe()
	defer sub1.Unsubscribe()
	defer sub2.Unsubscribe()

	b.Broadcast(InstanceEvent{
		Name:      "inst",
		OldStatus: instance.Stopped,
		NewStatus: instance.Failed,
	})

	for i, sub := range []EventSubscriber{sub1, sub2} {
		select {
		case ev := <-sub.Ch:
			if ev.Name != "inst" {
				t.Errorf("subscriber %d: expected name inst, got %q", i, ev.Name)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestEventBus_UnsubscribeStopsDelivery(t *testing.T) {
	b := newEventBus()

	sub := b.Subscribe()
	sub.Unsubscribe() // channel is now closed

	// After unsubscribe, the channel should be closed (receive returns zero value, ok=false).
	ev, ok := <-sub.Ch
	if ok {
		t.Errorf("expected channel to be closed after unsubscribe, got event: %+v", ev)
	}
}
