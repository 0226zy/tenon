package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/0226zy/tenon/pkg/contract"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestSQLiteSchemaAndTransactionalEvents(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u := &User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}
	if err := s.CreateUser(ctx, s.DB(), u); err != nil {
		t.Fatal(err)
	}
	c := &Conversation{ConvID: "abcdefghij12", UserID: "alice", Title: "", Model: "demo", Template: "react-go-sqlite", PreviewKey: "abcdefghijklmnopqrstuvwxyz"}
	if err := s.CreateConversation(ctx, s.DB(), c); err != nil {
		t.Fatal(err)
	}
	r := &Run{RunID: "01JTEST0000000000000000001", ConvID: c.ConvID, UserID: "alice", RequestID: "req-1", InputHash: "hash", Model: "demo", Status: contract.RunQueued}
	m := &Message{MsgID: "01JTEST0000000000000000002", ConvID: c.ConvID, RunID: r.RunID, Role: 1, Content: "hello"}
	if err := s.WithTx(ctx, func(tx *Tx) error {
		if err := s.CreateRun(ctx, tx, r); err != nil {
			return err
		}
		if err := s.CreateMessage(ctx, tx, m); err != nil {
			return err
		}
		_, err := s.AppendEventTx(ctx, tx, &Event{ConvID: c.ConvID, RunID: r.RunID, Source: 1, Type: "run_accepted", Payload: []byte(`{"status":"queued"}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(ctx, s.DB(), r.RunID)
	if err != nil || got.Status != contract.RunQueued || !got.Active {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	if err := s.WithTx(ctx, func(tx *Tx) error { return s.MarkRunning(ctx, tx, r.RunID, time.Now()) }); err != nil {
		t.Fatal(err)
	}
	got, err = s.Run(ctx, s.DB(), r.RunID)
	if err != nil || got.StartedAt == nil {
		t.Fatalf("start time: %+v %v", got, err)
	}
	b := &Sandbox{ConvID: c.ConvID, InstID: "inst", Status: 1, NetAddr: "127.0.0.1:7000", TokenHash: "token"}
	if err := s.PutSandbox(ctx, s.DB(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SandboxByToken(ctx, "token"); err != nil {
		t.Fatalf("creating sandbox token must authenticate for boot config: %v", err)
	}
	b.Status = 2
	b.NetAddr = "127.0.0.1:7001"
	if err := s.PutSandbox(ctx, s.DB(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sandbox(ctx, s.DB(), c.ConvID); err != nil {
		t.Fatal(err)
	}
	called := false
	want := errors.New("rollback")
	if err := s.WithTx(ctx, func(tx *Tx) error { tx.AfterCommit(func() { called = true }); return want }); !errors.Is(err, want) || called {
		t.Fatalf("rollback callback err=%v called=%v", err, called)
	}
}
func TestActiveRunUnique(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	c := &Conversation{ConvID: "abcdefghij12", UserID: "alice", Model: "demo", PreviewKey: "abcdefghijklmnopqrstuvwxyz"}
	if err := s.CreateConversation(ctx, s.DB(), c); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"01JTEST0000000000000000001", "01JTEST0000000000000000002"} {
		r := &Run{RunID: id, ConvID: c.ConvID, UserID: "alice", RequestID: id, InputHash: id, Model: "demo", Status: contract.RunQueued}
		err := s.CreateRun(ctx, s.DB(), r)
		if id[len(id)-1] == '1' && err != nil {
			t.Fatal(err)
		}
		if id[len(id)-1] == '2' && !errors.Is(err, ErrDuplicate) {
			t.Fatalf("expected duplicate, got %v", err)
		}
	}
}
