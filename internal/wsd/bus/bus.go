// Package bus implements bounded, best-effort in-process notifications.
package bus

import (
	"encoding/json"
	"sync"
)

// Msg is an immutable SSE-ready message. Data is always valid JSON.
type Msg struct {
	Type string
	Data json.RawMessage
}

type Bus struct {
	mu     sync.Mutex
	subs   map[uint64]chan Msg
	nextID uint64
	closed bool
}

func New() *Bus { return &Bus{subs: make(map[uint64]chan Msg)} }

// Publish fans out without blocking. A slow subscriber loses its oldest queued
// message, never the publisher's execution budget.
func (b *Bus) Publish(typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		raw, _ = json.Marshal(map[string]string{"notice": "event payload could not be encoded"})
		typ = "notice"
	}
	msg := Msg{Type: typ, Data: raw}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, ch := range b.subs {
		select {
		case ch <- msg:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- msg:
			default:
			}
		}
	}
}

// Subscribe returns a 64-message channel and an idempotent cancel function.
func (b *Bus) Subscribe() (<-chan Msg, func()) {
	ch := make(chan Msg, 64)
	b.mu.Lock()
	if b.closed {
		close(ch)
		b.mu.Unlock()
		return ch, func() {}
	}
	b.nextID++
	id := b.nextID
	if b.subs == nil {
		b.subs = make(map[uint64]chan Msg)
	}
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			if _, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(ch)
			}
			b.mu.Unlock()
		})
	}
	return ch, cancel
}

// Close wakes all subscribers. Publishing after Close is a no-op.
func (b *Bus) Close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		for id, ch := range b.subs {
			close(ch)
			delete(b.subs, id)
		}
	}
	b.mu.Unlock()
}
