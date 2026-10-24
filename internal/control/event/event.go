// Package event provides transactional UI sequencing, mapping and post-commit fanout.
package event

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/pkg/contract"
)

type Subscription struct {
	Notify <-chan struct{}
	Temp   <-chan contract.UIFrame
	Close  func()
}
type Hub struct {
	mu   sync.Mutex
	next int64
	subs map[string]map[int64]*subscriber
}
type subscriber struct {
	notify chan struct{}
	temp   chan contract.UIFrame
}

func NewHub() *Hub { return &Hub{subs: map[string]map[int64]*subscriber{}} }
func (h *Hub) Subscribe(convID string) Subscription {
	h.mu.Lock()
	h.next++
	id := h.next
	s := &subscriber{notify: make(chan struct{}, 1), temp: make(chan contract.UIFrame, 64)}
	if h.subs[convID] == nil {
		h.subs[convID] = map[int64]*subscriber{}
	}
	h.subs[convID][id] = s
	h.mu.Unlock()
	var once sync.Once
	return Subscription{Notify: s.notify, Temp: s.temp, Close: func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs[convID], id)
			if len(h.subs[convID]) == 0 {
				delete(h.subs, convID)
			}
			h.mu.Unlock()
		})
	}}
}
func (h *Hub) Notify(convID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs[convID] {
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
}
func (h *Hub) PublishTemp(convID string, f contract.UIFrame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs[convID] {
		select {
		case s.temp <- f:
		default:
		}
	}
}

type Store struct {
	St  *store.Store
	Hub *Hub
}

func NewStore(st *store.Store, hub *Hub) *Store { return &Store{St: st, Hub: hub} }
func (s *Store) AppendTx(ctx context.Context, tx *store.Tx, e *store.Event) (int64, error) {
	seq, err := s.St.AppendEventTx(ctx, tx, e)
	if err == nil {
		tx.AfterCommit(func() { s.Hub.Notify(e.ConvID) })
	}
	return seq, err
}
func (s *Store) AppendSourceOnceTx(ctx context.Context, tx *store.Tx, e *store.Event) (int64, bool, error) {
	seq, inserted, err := s.St.AppendSourceOnceTx(ctx, tx, e)
	if err == nil && inserted {
		tx.AfterCommit(func() { s.Hub.Notify(e.ConvID) })
	}
	return seq, inserted, err
}
func Control(convID, runID, typ string, data any) *store.Event {
	raw, _ := json.Marshal(data)
	return &store.Event{ConvID: convID, RunID: runID, Source: 1, Type: typ, Payload: raw}
}

type Mapper struct{ ToolArgsMax, ToolResultMax int }

func (m Mapper) ToUI(e store.Event) (contract.UIFrame, bool) {
	seq := e.Seq
	f := contract.UIFrame{Seq: &seq, RunID: e.RunID, TS: e.CreatedAt.UTC().Format(time.RFC3339Nano), Items: []contract.UIItem{}}
	if e.Source == 1 {
		var data map[string]any
		if json.Unmarshal(e.Payload, &data) != nil {
			data = map[string]any{}
		}
		f.Items = append(f.Items, contract.UIItem{Key: fmt.Sprintf("%s:%d", e.Type, e.Seq), Type: e.Type, Data: data})
		return f, true
	}
	var raw contract.RuntimeEvent
	if json.Unmarshal(e.Payload, &raw) != nil {
		return f, false
	}
	switch raw.Kind {
	case "message":
		if raw.Message == nil {
			return f, false
		}
		msg := raw.Message
		switch msg.Role {
		case "assistant":
			if msg.Content != "" {
				f.Items = append(f.Items, contract.UIItem{Key: fmt.Sprintf("text:%d", e.Seq), Type: "text", Data: map[string]any{"content": msg.Content}})
			}
			for _, call := range msg.ToolCalls {
				args := truncate(call.Function.Arguments, m.limitArgs())
				f.Items = append(f.Items, contract.UIItem{Key: call.ID, Type: "tool_call", Data: map[string]any{"call_id": call.ID, "name": call.Function.Name, "summary": summary(call.Function.Name, call.Function.Arguments), "args": args}})
			}
		case "tool":
			f.Items = append(f.Items, contract.UIItem{Key: msg.ToolCallID, Type: "tool_result", Data: map[string]any{"call_id": msg.ToolCallID, "name": msg.Name, "content": truncate(msg.Content, m.limitResult()), "is_error": msg.IsError, "truncated": len(msg.Content) > m.limitResult()}})
		}
	case "notice":
		f.Items = append(f.Items, contract.UIItem{Key: fmt.Sprintf("notice:%d", e.Seq), Type: "notice", Data: map[string]any{"text": raw.Note}})
	}
	return f, len(f.Items) > 0
}
func (m Mapper) TempToUI(runID, kind string, data any) (contract.UIFrame, bool) {
	f := contract.UIFrame{RunID: runID, TS: time.Now().UTC().Format(time.RFC3339Nano), Items: []contract.UIItem{}}
	switch kind {
	case "delta", "text_delta":
		f.Items = append(f.Items, contract.UIItem{Key: "text", Type: "text_delta", Data: map[string]any{"delta": data}})
	case "progress":
		f.Items = append(f.Items, contract.UIItem{Key: "progress", Type: "progress", Data: map[string]any{"text": data}})
	case "files_changed":
		f.Items = append(f.Items, contract.UIItem{Key: "files", Type: "files_changed", Data: map[string]any{"paths": data}})
	case "preview_status":
		if v, ok := data.(contract.PreviewState); ok {
			f.Items = append(f.Items, contract.UIItem{Key: "preview", Type: "preview_status", Data: map[string]any{"status": v.Status, "generation": v.Generation, "commit": v.Commit}})
		}
	default:
		return f, false
	}
	return f, true
}
func (m Mapper) limitArgs() int {
	if m.ToolArgsMax > 0 {
		return m.ToolArgsMax
	}
	return 2048
}
func (m Mapper) limitResult() int {
	if m.ToolResultMax > 0 {
		return m.ToolResultMax
	}
	return 4096
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func summary(name, args string) string {
	var v map[string]any
	if json.Unmarshal([]byte(args), &v) == nil {
		for _, k := range []string{"command", "path"} {
			if s, ok := v[k].(string); ok {
				return s
			}
		}
	}
	return name
}
