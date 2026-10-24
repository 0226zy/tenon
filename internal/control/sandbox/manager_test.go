package sandbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0226zy/protoforge/internal/control/quota"
	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/internal/kit/config"
	"github.com/0226zy/protoforge/internal/kit/idgen"
)

type replacementDriver struct {
	addr string
}

func (d replacementDriver) Create(context.Context, Spec) (Instance, error) {
	return Instance{ID: "replacement", Addr: d.addr}, nil
}
func (replacementDriver) Stop(context.Context, string) error         { return nil }
func (replacementDriver) Remove(context.Context, string, bool) error { return nil }
func (replacementDriver) Inspect(context.Context, string) (State, error) {
	return State{}, nil
}
func (replacementDriver) Close() error { return nil }

func TestEnsureReplacementPreservesPreviewGeneration(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 10}); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ConvID: "abcdefghij22", UserID: "alice", Model: "demo", PreviewKey: "abcdefghijklmnopqrstuvwxyz"}
	if err := st.CreateConversation(ctx, st.DB(), conv); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSandbox(ctx, st.DB(), &store.Sandbox{
		ConvID: conv.ConvID, InstID: "old-instance", Status: 3,
		PreviewStatus: store.PreviewStatusCode("ready"), PreviewGeneration: 6, PreviewCommit: "commit-6",
	}); err != nil {
		t.Fatal(err)
	}

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api":"wsd/test","ok":true}`))
	}))
	defer health.Close()
	cfg := config.Default()
	manager := NewManager(st, replacementDriver{addr: strings.TrimPrefix(health.URL, "http://")}, quota.New(cfg.Quota), cfg, idgen.New(), []byte("0123456789abcdef0123456789abcdef"))
	defer manager.Close()

	handle, err := manager.Ensure(ctx, conv.ConvID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if handle.InstID == "old-instance" {
		t.Fatal("Ensure did not replace the stopped instance")
	}
	got, err := st.Sandbox(ctx, st.DB(), conv.ConvID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != 2 || got.PreviewStatus != store.PreviewStatusCode("ready") || got.PreviewGeneration != 6 || got.PreviewCommit != "commit-6" {
		t.Fatalf("replacement regressed preview metadata: %+v", got)
	}
}
