package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/internal/control/agentclient"
	"github.com/0226zy/proto-platform/internal/control/event"
	"github.com/0226zy/proto-platform/internal/control/sandbox"
	"github.com/0226zy/proto-platform/internal/control/store"
	"github.com/0226zy/proto-platform/internal/kit/config"
	"github.com/0226zy/proto-platform/pkg/contract"
)

type consumerFixture struct {
	st       *store.Store
	conv     *store.Conversation
	consumer *Consumer
	handle   sandbox.Handle
	run      *store.Run
}

func newConsumerFixture(t *testing.T, handler http.Handler) *consumerFixture {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ConvID: "conv-1", UserID: "alice", Model: "local-demo", AgentSessionID: "session-1", RuntimeEpoch: "epoch-1"}
	if err := st.CreateConversation(ctx, st.DB(), conv); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.AgentClient.Timeout = config.Duration{Duration: time.Second}
	ac := agentclient.New(cfg.AgentClient)
	hub := event.NewHub()
	events := event.NewStore(st, hub)
	c := New(ac, st, events, hub, event.Mapper{}, 20*time.Millisecond)
	return &consumerFixture{
		st: st, conv: conv, consumer: c,
		handle: sandbox.Handle{ConvID: conv.ConvID, Addr: strings.TrimPrefix(server.URL, "http://"), WSDToken: "token"},
		run:    &store.Run{RunID: "run-1", ConvID: conv.ConvID, UserID: "alice"},
	}
}

func seq(n int) *int { return &n }

func writeRuntimeEvent(w http.ResponseWriter, raw contract.RuntimeEvent) {
	data, _ := json.Marshal(raw)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func openEventStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (f *consumerFixture) consume(t *testing.T) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := f.consumer.Consume(ctx, f.handle, f.run)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *consumerFixture) persistedEvents(t *testing.T) []store.Event {
	t.Helper()
	events, err := f.st.ListEventsAfter(context.Background(), f.conv.ConvID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestStreamGapRefillsMissingEventsInOrder(t *testing.T) {
	var eventCalls atomic.Int32
	f := newConsumerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/agent/sessions/session-1/stream":
			openEventStream(w)
			writeRuntimeEvent(w, contract.RuntimeEvent{Seq: seq(2), Kind: "marker", RunID: "run-1", Marker: &contract.RuntimeMarker{Type: "turn_done", RunID: "run-1", Iterations: 2}})
			<-req.Context().Done()
		case "/agent/sessions/session-1/events":
			eventCalls.Add(1)
			if got := req.URL.Query().Get("from_seq"); got != "0" {
				t.Errorf("refill from_seq=%s", got)
			}
			_ = json.NewEncoder(w).Encode(contract.EventsPage{NextSeq: 2, Events: []contract.RuntimeEvent{
				{Seq: seq(0), Kind: "message", RunID: "run-1", Message: &contract.RuntimeMessage{Role: "assistant", Content: "hello"}},
				{Seq: seq(1), Kind: "notice", RunID: "run-1", Note: "middle"},
			}})
		default:
			http.NotFound(w, req)
		}
	}))
	out := f.consume(t)
	if out.Status != "done" || out.Assistant != "hello" || out.RtSeqEnd != 3 || out.Iterations != 2 {
		t.Fatalf("outcome=%+v", out)
	}
	if eventCalls.Load() != 1 {
		t.Fatalf("refill calls=%d", eventCalls.Load())
	}
	events := f.persistedEvents(t)
	if len(events) != 3 {
		t.Fatalf("persisted events=%d", len(events))
	}
	for i, want := range []string{"message", "notice", "marker"} {
		if events[i].Type != want || events[i].RtSeq == nil || *events[i].RtSeq != i {
			t.Errorf("event %d=%+v", i, events[i])
		}
	}
	conv, err := f.st.Conversation(context.Background(), f.st.DB(), f.conv.ConvID)
	if err != nil || conv.RtNextSeq != 3 {
		t.Fatalf("conversation cursor=%+v err=%v", conv, err)
	}
}

func TestStoppedHighWaterRefillsBeforeReturning(t *testing.T) {
	var eventCalls atomic.Int32
	f := newConsumerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/agent/sessions/session-1/stream":
			openEventStream(w)
			<-req.Context().Done()
		case "/agent/sessions/session-1/runs/run-1":
			_ = json.NewEncoder(w).Encode(contract.RunState{RunID: "run-1", Status: contract.RuntimeStopped, Outcome: "done", NextSeq: 2, Iterations: 4})
		case "/agent/sessions/session-1/events":
			eventCalls.Add(1)
			_ = json.NewEncoder(w).Encode(contract.EventsPage{NextSeq: 2, Events: []contract.RuntimeEvent{
				{Seq: seq(0), Kind: "message", RunID: "run-1", Message: &contract.RuntimeMessage{Role: "assistant", Content: "from refill"}},
				{Seq: seq(1), Kind: "notice", RunID: "run-1", Note: "complete"},
			}})
		default:
			http.NotFound(w, req)
		}
	}))
	out := f.consume(t)
	if out.Status != "done" || out.Assistant != "from refill" || out.RtSeqEnd != 2 || out.Iterations != 4 {
		t.Fatalf("outcome=%+v", out)
	}
	if eventCalls.Load() == 0 || len(f.persistedEvents(t)) != 2 {
		t.Fatalf("refill calls=%d events=%d", eventCalls.Load(), len(f.persistedEvents(t)))
	}
	conv, err := f.st.Conversation(context.Background(), f.st.DB(), f.conv.ConvID)
	if err != nil || conv.RtNextSeq != 2 {
		t.Fatalf("conversation cursor=%+v err=%v", conv, err)
	}
}

func TestStreamReconnectsFromCommittedCursor(t *testing.T) {
	var streams atomic.Int32
	var lastFrom atomic.Value
	lastFrom.Store("")
	f := newConsumerFixture(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/agent/sessions/session-1/stream" {
			http.NotFound(w, req)
			return
		}
		attempt := streams.Add(1)
		lastFrom.Store(req.URL.Query().Get("from_seq"))
		openEventStream(w)
		if attempt == 1 {
			writeRuntimeEvent(w, contract.RuntimeEvent{Seq: seq(0), Kind: "notice", RunID: "run-1", Note: "before disconnect"})
			return
		}
		writeRuntimeEvent(w, contract.RuntimeEvent{Seq: seq(1), Kind: "marker", RunID: "run-1", Marker: &contract.RuntimeMarker{Type: "turn_done", RunID: "run-1"}})
		<-req.Context().Done()
	}))
	out := f.consume(t)
	if out.Status != "done" || out.RtSeqEnd != 2 {
		t.Fatalf("outcome=%+v", out)
	}
	if streams.Load() != 2 || lastFrom.Load() != strconv.Itoa(1) {
		t.Fatalf("streams=%d last from_seq=%q", streams.Load(), lastFrom.Load())
	}
	if len(f.persistedEvents(t)) != 2 {
		t.Fatalf("persisted events=%d", len(f.persistedEvents(t)))
	}
}
