// Package consumer reconciles at-least-once runtime events into transactional UI events.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/pkg/contract"
)

type Outcome struct {
	Status, ErrorCode, Assistant string
	RtSeqEnd, Iterations         int
	RunID                        string
}
type Consumer struct {
	ac        *agentclient.Client
	st        *store.Store
	ev        *event.Store
	hub       *event.Hub
	mapper    event.Mapper
	reconcile time.Duration
}

func New(ac *agentclient.Client, st *store.Store, ev *event.Store, hub *event.Hub, mapper event.Mapper, reconcile time.Duration) *Consumer {
	if reconcile <= 0 {
		reconcile = 5 * time.Second
	}
	return &Consumer{ac: ac, st: st, ev: ev, hub: hub, mapper: mapper, reconcile: reconcile}
}

type streamItem struct {
	ev  contract.RuntimeEvent
	err error
}

func readAsync(s *agentclient.RawStream) chan streamItem {
	ch := make(chan streamItem, 1)
	go func() { v, e := s.Next(); ch <- streamItem{ev: v, err: e} }()
	return ch
}
func (c *Consumer) Consume(ctx context.Context, h sandbox.Handle, r *store.Run) (Outcome, error) {
	conv, err := c.st.Conversation(ctx, c.st.DB(), r.ConvID)
	if err != nil {
		return Outcome{}, err
	}
	cursor := conv.RtNextSeq
	out := Outcome{Status: "failed", RtSeqEnd: cursor, RunID: r.RunID}
	var stream *agentclient.RawStream
	var ch chan streamItem
	closeStream := func() {
		if stream != nil {
			_ = stream.Close()
			stream = nil
			ch = nil
		}
	}
	defer closeStream()
	ticker := time.NewTicker(c.reconcile)
	defer ticker.Stop()
	reconnect := time.NewTicker(200 * time.Millisecond)
	defer reconnect.Stop()
	for {
		if stream == nil {
			stream, err = c.ac.Stream(ctx, h, conv.AgentSessionID, cursor)
			if err != nil {
				stream = nil
			}
		}
		if stream != nil && ch == nil {
			ch = readAsync(stream)
		}
		select {
		case <-ctx.Done():
			out.Status = "cancelled"
			return out, nil
		case item := <-ch:
			ch = nil
			if item.err != nil {
				closeStream()
				if !errors.Is(item.err, io.EOF) {
					time.Sleep(100 * time.Millisecond)
				}
				continue
			}
			if item.ev.Seq == nil {
				c.temp(conv.ConvID, r.RunID, item.ev)
				continue
			}
			seq := *item.ev.Seq
			if seq < cursor {
				continue
			}
			if seq > cursor {
				done, err := c.refill(ctx, h, conv, &cursor, seq-1, &out)
				if err != nil {
					return out, err
				}
				if done {
					return out, nil
				}
				if cursor < seq {
					closeStream()
					continue
				}
			}
			done, err := c.one(ctx, conv, &cursor, item.ev, &out)
			if err != nil {
				return out, err
			}
			out.RtSeqEnd = cursor
			if done {
				return out, nil
			}
		case <-ticker.C:
			state, err := c.ac.GetRun(ctx, h, conv.AgentSessionID, r.RunID)
			if err != nil {
				continue
			}
			if state.NextSeq > cursor {
				done, err := c.refill(ctx, h, conv, &cursor, state.NextSeq-1, &out)
				if err != nil {
					return out, err
				}
				if done {
					return out, nil
				}
			}
			out.Iterations = state.Iterations
			if state.Status == contract.RuntimeStopped && cursor >= state.NextSeq {
				return fromState(state, out), nil
			}
		case <-reconnect.C:
		}
	}
}
func fromState(s contract.RunState, out Outcome) Outcome {
	out.RtSeqEnd = s.NextSeq
	out.Iterations = s.Iterations
	out.ErrorCode = s.ErrorCode
	switch s.Outcome {
	case "done", "finished", "success":
		out.Status = "done"
	case "cancelled":
		out.Status = "cancelled"
	default:
		out.Status = "failed"
	}
	return out
}
func (c *Consumer) refill(ctx context.Context, h sandbox.Handle, conv *store.Conversation, cursor *int, to int, out *Outcome) (bool, error) {
	for *cursor <= to {
		before := *cursor
		events, next, err := c.ac.Events(ctx, h, conv.AgentSessionID, *cursor, min(200, to-*cursor+1))
		if err != nil {
			return false, err
		}
		if len(events) == 0 {
			return false, nil
		}
		for _, raw := range events {
			if raw.Seq == nil {
				c.temp(conv.ConvID, out.RunID, raw)
				continue
			}
			seq := *raw.Seq
			if seq < *cursor || seq > to {
				continue
			}
			done, err := c.one(ctx, conv, cursor, raw, out)
			if err != nil || done {
				return done, err
			}
		}
		if next <= *cursor || *cursor == before {
			break
		}
	}
	return false, nil
}
func (c *Consumer) one(ctx context.Context, conv *store.Conversation, cursor *int, raw contract.RuntimeEvent, out *Outcome) (bool, error) {
	if raw.Seq == nil {
		return false, nil
	}
	seq := *raw.Seq
	if raw.Message != nil && raw.Message.Role == "assistant" && len(raw.Message.ToolCalls) == 0 && raw.Message.Content != "" {
		out.Assistant = raw.Message.Content
	}
	progress := raw.Kind == "marker" && raw.Marker != nil && raw.Marker.Type == "progress"
	err := c.st.WithTx(ctx, func(tx *store.Tx) error {
		if progress {
			return c.st.AdvanceRuntimeCursor(ctx, tx, conv.ConvID, conv.RuntimeEpoch, seq+1)
		}
		payload, _ := json.Marshal(raw)
		rtSeq := seq
		runID := raw.RunID
		if runID == "" && out != nil {
			runID = out.RunID
		}
		e := &store.Event{ConvID: conv.ConvID, RunID: runID, Source: 0, Type: raw.Kind, RtEpoch: conv.RuntimeEpoch, RtSession: conv.AgentSessionID, RtSeq: &rtSeq, Payload: payload}
		_, _, err := c.ev.AppendSourceOnceTx(ctx, tx, e)
		if err != nil {
			return err
		}
		return c.st.AdvanceRuntimeCursor(ctx, tx, conv.ConvID, conv.RuntimeEpoch, seq+1)
	})
	if err != nil {
		return false, err
	}
	*cursor = seq + 1
	out.RtSeqEnd = *cursor
	if progress {
		c.temp(conv.ConvID, out.RunID, raw)
		return false, nil
	}
	if raw.Kind == "marker" && raw.Marker != nil {
		m := raw.Marker
		if m.Iterations > 0 {
			out.Iterations = m.Iterations
		}
		runID := raw.RunID
		if runID == "" {
			runID = m.RunID
		}
		if runID != "" && out.RunID != "" && runID != out.RunID {
			return false, nil
		}
		switch m.Type {
		case "turn_done":
			out.Status = "done"
			return true, nil
		case "turn_failed":
			out.Status = "failed"
			out.ErrorCode = m.ErrorCode
			return true, nil
		case "turn_cancelled":
			out.Status = "cancelled"
			return true, nil
		}
	}
	return false, nil
}
func (c *Consumer) temp(convID, runID string, raw contract.RuntimeEvent) {
	if raw.Kind == "delta" || raw.Delta != "" {
		if f, ok := c.mapper.TempToUI(runID, "delta", raw.Delta); ok {
			c.hub.PublishTemp(convID, f)
		}
		return
	}
	if raw.Kind == "marker" && raw.Marker != nil && raw.Marker.Type == "progress" {
		if f, ok := c.mapper.TempToUI(runID, "progress", raw.Marker.Detail); ok {
			c.hub.PublishTemp(convID, f)
		}
	}
}
