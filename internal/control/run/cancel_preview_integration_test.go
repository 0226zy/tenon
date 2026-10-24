//go:build linux || darwin

package run

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/0226zy/protoforge/internal/control/agentclient"
	"github.com/0226zy/protoforge/internal/control/consumer"
	"github.com/0226zy/protoforge/internal/control/event"
	"github.com/0226zy/protoforge/internal/control/quota"
	"github.com/0226zy/protoforge/internal/control/sandbox"
	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/internal/kit/config"
	"github.com/0226zy/protoforge/internal/kit/idgen"
	wsdapi "github.com/0226zy/protoforge/internal/wsd/api"
	"github.com/0226zy/protoforge/internal/wsd/bus"
	"github.com/0226zy/protoforge/internal/wsd/cpclient"
	"github.com/0226zy/protoforge/internal/wsd/gate"
	wsdpreview "github.com/0226zy/protoforge/internal/wsd/preview"
	"github.com/0226zy/protoforge/internal/wsd/proc"
	"github.com/0226zy/protoforge/pkg/contract"
)

func TestCancelRunReapsOrphansWithoutKillingRegisteredPreview(t *testing.T) {
	buildControl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer cp-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/internal/build/acquire":
			_ = json.NewEncoder(w).Encode(contract.BuildAcquireResult{SlotID: "slot-cancel-integration"})
		case "/internal/build/release":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))

	registry := proc.New()
	workspaceGate := &gate.Gate{}
	eventBus := bus.New()
	root := t.TempDir()
	python := `from http.server import BaseHTTPRequestHandler, HTTPServer
import os
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"preview-alive"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass
HTTPServer(("0.0.0.0", int(os.environ["PORT"])), Handler).serve_forever()
`
	if err := os.WriteFile(filepath.Join(root, "server.py"), []byte(python), 0o600); err != nil {
		t.Fatal(err)
	}
	start := "#!/usr/bin/env bash\nset -euo pipefail\nexec python3 \"$PWD/server.py\"\n"
	if err := os.WriteFile(filepath.Join(root, "start.sh"), []byte(start), 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor := wsdpreview.New(wsdpreview.Config{
		AppRoot: root, DataDir: filepath.Join(t.TempDir(), "data"), Mode: "live",
		BuildTimeout: 3 * time.Second, MaxRestarts: 1, LogLines: 100,
		Local: true, ProbeInterval: 20 * time.Millisecond, ProbeTimeout: 200 * time.Millisecond, StopGrace: 50 * time.Millisecond,
		Env: map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME"), "LANG": "C.UTF-8"},
	}, workspaceGate, registry, cpclient.New(buildControl.URL, "cp-token"), eventBus)

	runtime := &fakeRuntime{}
	runtime.startStatus.Store(http.StatusOK)
	wsdServer := wsdapi.NewServer(wsdapi.Deps{
		Token: "wsd-token", SandboxID: "cancel-integration", Agent: http.HandlerFunc(runtime.serveHTTP),
		Gate: workspaceGate, Proc: registry, Preview: supervisor,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The runtime fake supplies checkpoint data; all cancellation, gate and
		// cleanup requests still traverse the real wsd HTTP server.
		if req.URL.Path == "/git/commit" {
			runtime.serveHTTP(w, req)
			return
		}
		wsdServer.Handler.ServeHTTP(w, req)
	}))

	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = "local"
	cfg.Run.MaxDuration = config.Duration{Duration: 5 * time.Second}
	cfg.Run.StopConfirmTimeout = config.Duration{Duration: time.Second}
	cfg.Run.PrepareRetry = 1
	cfg.Quota.Queue.Timeout = config.Duration{Duration: time.Second}
	cfg.AgentClient.Timeout = config.Duration{Duration: time.Second}
	cfg.AgentClient.PreviewTimeout = config.Duration{Duration: 8 * time.Second}
	ac := agentclient.New(cfg.AgentClient)
	handle := sandbox.Handle{ConvID: "conv-1", InstID: "inst-1", Addr: strings.TrimPrefix(server.URL, "http://"), WSDToken: "wsd-token"}
	sb := &fakeSandbox{handle: handle}
	quotas := quota.New(cfg.Quota)
	hub := event.NewHub()
	events := event.NewStore(st, hub)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	fc := &fakeConsumer{consume: func(ctx context.Context, _ sandbox.Handle, _ *store.Run) (consumer.Outcome, error) {
		enteredOnce.Do(func() { close(entered) })
		<-ctx.Done()
		return consumer.Outcome{Status: "cancelled"}, nil
	}}
	service := NewService(st, events, hub, idgen.New(), testCatalog{}, sb, ac, fc, quotas, cfg)
	var orphan *exec.Cmd
	var orphanPGID int
	t.Cleanup(func() {
		service.Close()
		if orphan != nil && orphanPGID > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = proc.StopGroup(ctx, orphanPGID, 50*time.Millisecond)
			cancel()
			_ = orphan.Wait()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = supervisor.Stop(ctx)
		cancel()
		server.Close()
		buildControl.Close()
		eventBus.Close()
		_ = st.Close()
	})

	ctx := context.Background()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}); err != nil {
		t.Fatal(err)
	}
	conversation := &store.Conversation{ConvID: "conv-1", UserID: "alice", Model: "local-demo", PreviewKey: "preview-key"}
	if err := st.CreateConversation(ctx, st.DB(), conversation); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.Accept(ctx, "alice", conversation.ConvID, contract.AcceptRequest{ClientRequestID: "request-cancel-preview", Content: "long work"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Run consumer was not entered")
	}

	previewRequest, err := http.NewRequest(http.MethodPost, server.URL+"/preview/ensure?wait=5", strings.NewReader(`{"generation":0}`))
	if err != nil {
		t.Fatal(err)
	}
	previewRequest.Header.Set("Authorization", "Bearer wsd-token")
	previewRequest.Header.Set("Content-Type", "application/json")
	previewRequest.Header.Set("X-Run-Id", accepted.RunID)
	previewResponse, err := (&http.Client{Timeout: 8 * time.Second}).Do(previewRequest)
	if err != nil {
		t.Fatal(err)
	}
	var previewState contract.PreviewState
	decodeErr := json.NewDecoder(previewResponse.Body).Decode(&previewState)
	previewResponse.Body.Close()
	if decodeErr != nil || previewResponse.StatusCode != http.StatusOK || previewState.Status != "ready" || previewState.LocalPort == 0 {
		t.Fatalf("preview ensure status=%d state=%+v err=%v", previewResponse.StatusCode, previewState, decodeErr)
	}
	previewURL := "http://127.0.0.1:" + strconv.Itoa(previewState.LocalPort)

	// This detached child stands in for an untracked Runtime descendant. The
	// preview above is a real registered supervisor process in the same registry.
	orphan = exec.Command("sleep", "30")
	orphanPGID, err = proc.StartProcess(orphan)
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.Cancel(ctx, "alice", conversation.ConvID, accepted.RunID)
	if err != nil || view.Status != contract.RunCancelling {
		t.Fatalf("cancel view=%+v err=%v", view, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, runErr := st.Run(ctx, st.DB(), accepted.RunID)
		if runErr == nil && r.Status == contract.RunCancelled {
			if r.Active || !r.StopConfirmed {
				t.Fatalf("cancelled Run=%+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run did not cancel: run=%+v err=%v", r, runErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	for {
		err := syscall.Kill(-orphanPGID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphan process group %d remains after Run cleanup: %v", orphanPGID, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	response, err := (&http.Client{Timeout: time.Second}).Get(previewURL)
	if err != nil {
		t.Fatalf("registered preview was killed by Run cleanup: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || supervisor.Status().Status != "ready" {
		t.Fatalf("preview response=%d state=%+v", response.StatusCode, supervisor.Status())
	}
}
