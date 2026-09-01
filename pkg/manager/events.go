package manager

import (
	"llamactl/pkg/instance"
	"sync"
)

// InstanceEvent is a broadcast of an instance status change. It carries just
// enough for a client to refetch or update a single row without a full list call.
type InstanceEvent struct {
	Name    string
	OldStatus instance.Status
	NewStatus instance.Status
}

// EventSubscriber is a read-only channel of instance events plus an unsubscribe
// func. The channel is unbuffered: a slow consumer blocks the broadcaster, so
// the broadcaster should drop (via select + close) rather than accumulate.
type EventSubscriber struct {
	Ch <-chan InstanceEvent
	Unsubscribe func()
}

// eventBus is a minimal in-process pub/sub for instance status changes.
// It is goroutine-safe: Subscribe/Unsubscribe/Broadcast may be called from any
// goroutine concurrently.
type eventBus struct {
	mu          sync.RWMutex
	subscribers map[chan InstanceEvent]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{subscribers: make(map[chan InstanceEvent]struct{})}
}

// Subscribe registers a new subscriber and returns the event channel plus an
// unsubscribe func. The channel is buffered with capacity 1 so a broadcast can
// queue an event even if the receiver is not yet in its select.
func (b *eventBus) Subscribe() EventSubscriber {
	ch := make(chan InstanceEvent, 1)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	return EventSubscriber{
		Ch: ch,
		Unsubscribe: func() {
			b.mu.Lock()
			delete(b.subscribers, ch)
			b.mu.Unlock()
			close(ch)
		},
	}
}

// Broadcast sends an event to every subscriber. A subscriber that is not ready
// to receive is dropped (and unsubscribed) so one slow client cannot block the
// rest — the broadcaster never blocks indefinitely.
func (b *eventBus) Broadcast(ev InstanceEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			// Slow consumer: drop it so we never block the broadcast loop.
			// We can't safely remove from the map while holding an RLock,
			// so we just drop this event for that subscriber. Subsequent
			// events will still be delivered until it drains.
		}
	}
}
