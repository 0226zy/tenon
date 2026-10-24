package event

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/0226zy/protoforge/internal/control/store"
)

func eventTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	conv := &store.Conversation{ConvID: "abcdefghij12", UserID: "alice", Model: "demo", PreviewKey: "abcdefghijklmnopqrstuvwxyz"}
	if err := st.CreateConversation(context.Background(), st.DB(), conv); err != nil {
		t.Fatal(err)
	}
	return st, conv.ConvID
}

func TestAppendNotifiesOnlyAfterCommit(t *testing.T) {
	st, convID := eventTestStore(t)
	hub := NewHub()
	events := NewStore(st, hub)
	sub := hub.Subscribe(convID)
	defer sub.Close()
	rollback := errors.New("rollback")
	err := st.WithTx(context.Background(), func(tx *store.Tx) error {
		_, err := events.AppendTx(context.Background(), tx, Control(convID, "run-1", "run_accepted", map[string]any{"status": "queued"}))
		if err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("rollback error=%v", err)
	}
	select {
	case <-sub.Notify:
		t.Fatal("rolled-back event was broadcast")
	default:
	}
	persisted, err := st.ListEventsAfter(context.Background(), convID, 0, 10)
	if err != nil || len(persisted) != 0 {
		t.Fatalf("rolled-back events=%d err=%v", len(persisted), err)
	}
	if err := st.WithTx(context.Background(), func(tx *store.Tx) error {
		_, err := events.AppendTx(context.Background(), tx, Control(convID, "run-1", "run_started", map[string]any{"status": "running"}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.Notify:
	case <-time.After(time.Second):
		t.Fatal("committed event was not broadcast")
	}
}

func TestSourceEventDeduplication(t *testing.T) {
	st, convID := eventTestStore(t)
	hub := NewHub()
	events := NewStore(st, hub)
	sub := hub.Subscribe(convID)
	defer sub.Close()
	seq := 0
	newEvent := func() *store.Event {
		return &store.Event{ConvID: convID, RunID: "run-1", Source: 0, Type: "message", RtEpoch: "epoch", RtSession: "session", RtSeq: &seq, Payload: []byte(`{"kind":"notice","note":"ok"}`)}
	}
	var firstSeq int64
	if err := st.WithTx(context.Background(), func(tx *store.Tx) error {
		var inserted bool
		var err error
		firstSeq, inserted, err = events.AppendSourceOnceTx(context.Background(), tx, newEvent())
		if err == nil && !inserted {
			t.Fatal("first source event was not inserted")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	<-sub.Notify
	if err := st.WithTx(context.Background(), func(tx *store.Tx) error {
		gotSeq, inserted, err := events.AppendSourceOnceTx(context.Background(), tx, newEvent())
		if err == nil && (inserted || gotSeq != firstSeq) {
			t.Fatalf("duplicate seq=%d inserted=%v first=%d", gotSeq, inserted, firstSeq)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.Notify:
		t.Fatal("duplicate source event was broadcast")
	default:
	}
	persisted, err := st.ListEventsAfter(context.Background(), convID, 0, 10)
	if err != nil || len(persisted) != 1 {
		t.Fatalf("events=%d err=%v", len(persisted), err)
	}
}
