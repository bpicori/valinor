package event

import (
	"sync"

	"valinor/internal/logger"
)

// defaultBufSize is the channel buffer size for all subscriptions.
const defaultBufSize = 64

// Subscription receives events from the bus.
type Subscription struct {
	ch     chan Event
	filter map[EventType]bool // nil means accept all
}

// Ch returns the receive-only channel for this subscription.
func (s *Subscription) Ch() <-chan Event { return s.ch }

// Bus is a publish/subscribe event bus.
type Bus struct {
	mu   sync.RWMutex
	subs []*Subscription
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{}
}

// Subscribe creates a subscription. Events are filtered to the given types.
// If no types are given, all events are delivered.
func (b *Bus) Subscribe(types ...EventType) *Subscription {
	sub := &Subscription{
		ch: make(chan Event, defaultBufSize),
	}
	if len(types) > 0 {
		sub.filter = make(map[EventType]bool, len(types))
		for _, t := range types {
			sub.filter[t] = true
		}
	}

	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()

	return sub
}

// Unsubscribe removes a subscription and closes its channel.
func (b *Bus) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, s := range b.subs {
		if s == sub {
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			close(s.ch)
			return
		}
	}
}

// Publish sends an event to all matching subscribers.
// Non-blocking: if a subscriber's buffer is full, the event is dropped
// (prevents a slow consumer from blocking the whole system).
func (b *Bus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, sub := range b.subs {
		if sub.filter != nil && !sub.filter[ev.Type] {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			logger.Warning("bus event dropped", logger.BuildMetadata("event_type", ev.Type, "reason", "channel full"))
		}
	}
}

// Package-level default bus for application-wide event distribution.
var (
	defaultBus     *Bus
	defaultBusOnce sync.Once
)

func defaultBusInstance() *Bus {
	defaultBusOnce.Do(func() {
		defaultBus = NewBus()
	})
	return defaultBus
}

// Subscribe creates a subscription on the default bus.
// Events are filtered to the given types. If no types are given, all events are delivered.
func Subscribe(types ...EventType) *Subscription {
	return defaultBusInstance().Subscribe(types...)
}

// Unsubscribe removes a subscription from the default bus and closes its channel.
func Unsubscribe(sub *Subscription) {
	defaultBusInstance().Unsubscribe(sub)
}

// Publish sends an event to all matching subscribers on the default bus.
// Non-blocking: if a subscriber's buffer is full, the event is dropped.
func Publish(ev Event) {
	defaultBusInstance().Publish(ev)
}