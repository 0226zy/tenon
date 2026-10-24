package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/secret"
	"github.com/0226zy/tenon/pkg/contract"
)

func TestAppStartRecoversFinalizingRun(t *testing.T) {
	var commits, prepares, starts atomic.Int32
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/healthz":
			_ = json.NewEncoder(w).Encode(contract.Health{API: contract.WSDAPI, OK: true})
		case req.Method == http.MethodGet && req.URL.Path == "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-req.Context().Done()
		case req.Method == http.MethodPost && req.URL.Path == "/git/commit":
			commits.Add(1)
			_ = json.NewEncoder(w).Encode(contract.CommitResult{Commit: "commit-recovered", CheckpointStatus: "done"})
		case req.Method == http.MethodPost && req.URL.Path == "/gate/release":
			w.WriteHeader(http.StatusNoContent)
		case req.Method == http.MethodPost && req.URL.Path == "/preview/ensure":
			var in contract.PreviewEnsureRequest
			_ = json.NewDecoder(req.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(contract.PreviewState{Status: "ready", Generation: in.Generation, Commit: in.Commit})
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/runs"):
			prepares.Add(1)
			_ = json.NewEncoder(w).Encode(contract.RunState{RunID: "run-app-recovery", Status: contract.RuntimePrepared})
		case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/start"):
			starts.Add(1)
			_ = json.NewEncoder(w).Encode(contract.RunState{RunID: "run-app-recovery", Status: contract.RuntimeRunning})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(contract.ErrorResponse{Error: contract.ErrorBody{Code: "NOT_FOUND"}})
		}
	}))
	defer runtime.Close()

	dir := t.TempDir()
	catalog := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(catalog, []byte("models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = "local"
	cfg.SQLite.Path = filepath.Join(dir, "control.db")
	cfg.Control.SecretFile = filepath.Join(dir, "secret.key")
	cfg.Catalog.File = catalog
	cfg.Sandbox.Driver = "local"
	cfg.Sandbox.HealthInterval = config.Duration{Duration: time.Hour}
	cfg.Local.Root = filepath.Join(dir, "sandboxes")
	cfg.Local.WSDBin = filepath.Join(dir, "wsd")
	cfg.Local.TemplateDir = filepath.Join(dir, "template")
	cfg.Local.AgentRuntimeBin = filepath.Join(dir, "agent-runtime")
	cfg.AgentClient.Timeout = config.Duration{Duration: time.Second}
	cfg.AgentClient.PreviewTimeout = config.Duration{Duration: time.Second}

	a, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	if err := a.Store.CreateUser(ctx, a.Store.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}); err != nil {
		t.Fatal(err)
	}
	conversation := &store.Conversation{ConvID: "abcdefghij12", UserID: "alice", Model: "local-demo", PreviewKey: "abcdefghijklmnopqrstuvwxyz"}
	if err := a.Store.CreateConversation(ctx, a.Store.DB(), conversation); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC()
	if err := a.Store.PutSandbox(ctx, a.Store.DB(), &store.Sandbox{
		ConvID: conversation.ConvID, InstID: "inst-recovery", Status: 2,
		NetAddr: strings.TrimPrefix(runtime.URL, "http://"), TokenHash: secret.Hash("sandbox-token"), TokenExpiresAt: &expires,
	}); err != nil {
		t.Fatal(err)
	}
	r := &store.Run{
		RunID: "run-app-recovery", ConvID: conversation.ConvID, UserID: "alice", RequestID: "request-app-recovery",
		InputHash: "hash", Model: "local-demo", Status: contract.RunRunning, Generation: 4,
	}
	message := &store.Message{MsgID: "message-app-recovery", ConvID: conversation.ConvID, RunID: r.RunID, Role: 1, Content: "recover"}
	if err := a.Store.WithTx(ctx, func(tx *store.Tx) error {
		if err := a.Store.CreateRun(ctx, tx, r); err != nil {
			return err
		}
		return a.Store.CreateMessage(ctx, tx, message)
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.MarkRunning(ctx, a.Store.DB(), r.RunID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.WithTx(ctx, func(tx *store.Tx) error {
		ok, err := a.Store.TransitionRun(ctx, tx, r.RunID, nil, contract.RunFinalizing)
		if err == nil && !ok {
			t.Fatal("Run was not transitioned to finalizing")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		recovered, err := a.Store.Run(ctx, a.Store.DB(), r.RunID)
		if err == nil && recovered.Status == contract.RunFinished {
			if recovered.Active || recovered.Commit != "commit-recovered" {
				t.Fatalf("recovered Run=%+v", recovered)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("App.Start did not recover Run: run=%+v err=%v", recovered, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if prepares.Load() != 0 || starts.Load() != 0 || commits.Load() != 1 {
		t.Fatalf("prepare=%d start=%d commit=%d", prepares.Load(), starts.Load(), commits.Load())
	}
}
