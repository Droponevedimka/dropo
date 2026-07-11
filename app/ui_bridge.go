package main

import (
	"sync"
	"sync/atomic"
	"time"
)

type AppEvent struct {
	ID        uint64      `json:"id"`
	Name      string      `json:"name"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
}

type EventHub struct {
	seq    atomic.Uint64
	limit  int
	mu     sync.RWMutex
	events []AppEvent
}

func NewEventHub(limit int) *EventHub {
	if limit <= 0 {
		limit = 256
	}
	return &EventHub{limit: limit, events: make([]AppEvent, 0, limit)}
}

func (h *EventHub) Emit(name string, payload interface{}) AppEvent {
	event := AppEvent{
		ID:        h.seq.Add(1),
		Name:      name,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	h.mu.Lock()
	h.events = append(h.events, event)
	if len(h.events) > h.limit {
		copy(h.events, h.events[len(h.events)-h.limit:])
		h.events = h.events[:h.limit]
	}
	h.mu.Unlock()
	return event
}

func (h *EventHub) Since(after uint64) []AppEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]AppEvent, 0, len(h.events))
	for _, event := range h.events {
		if event.ID > after {
			out = append(out, event)
		}
	}
	return out
}

func (a *App) emitEvent(name string, payload interface{}) {
	if a == nil || a.events == nil || name == "" {
		return
	}
	a.events.Emit(name, payload)
}

func (a *App) eventSnapshot(after uint64) []AppEvent {
	if a == nil || a.events == nil {
		return []AppEvent{}
	}
	return a.events.Since(after)
}
