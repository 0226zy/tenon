//go:build linux || darwin

package preview

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/protoforge/internal/wsd/bus"
	"github.com/0226zy/protoforge/internal/wsd/cpclient"
	"github.com/0226zy/protoforge/internal/wsd/gate"
	"github.com/0226zy/protoforge/internal/wsd/proc"
	"github.com/0226zy/protoforge/pkg/contract"
)

type fakeBuildControl struct {
	server   *httptest.Server
	acquires atomic.Int32
	releases atomic.Int32
}

func newFakeBuildControl() *fakeBuildControl {
	fake := &fakeBuildControl{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cp-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/internal/build/acquire":
			fake.acquires.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"slot_id": "slot-1"})
		case "/internal/build/release":
			fake.releases.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return fake
}

func TestEnsureDynamicPortEnvironmentAndStop(t *testing.T) {
	root := writePreviewApp(t)
	supervisor, fake := newTestSupervisor(t, root, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	state, err := supervisor.Ensure(ctx, EnsureReq{Generation: 1, Commit: "abc", Wait: 5 * time.Second}, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "ready" || state.LocalPort == 0 || state.Probe.Code != 200 || state.Commit != "abc" {
		t.Fatalf("state = %+v", state)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(state.LocalPort)
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	resp, err = http.Get(base + "/environment")
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&environment); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, key := range []string{"LLM_PROXY_TOKEN", "WSD_TOKEN", "MAGENTS_AUTH_TOKEN", "OTHER_SECRET"} {
		if environment[key] != "" {
			t.Fatalf("preview received secret environment %s", key)
		}
	}
	if environment["PATH"] == "" || environment["DATA_DIR"] == "" || environment["PORT"] != strconv.Itoa(state.LocalPort) {
		t.Fatalf("environment = %+v", environment)
	}
	if fake.acquires.Load() != 1 || fake.releases.Load() != 1 {
		t.Fatalf("build slots acquire=%d release=%d", fake.acquires.Load(), fake.releases.Load())
	}
	if _, err := supervisor.Ensure(ctx, EnsureReq{Generation: 1, Wait: time.Second}, ""); err != nil {
		t.Fatalf("ready ensure should be idempotent: %v", err)
	}
	if fake.acquires.Load() != 1 {
		t.Fatal("idempotent ensure rebuilt the preview")
	}
	if _, err := supervisor.Ensure(ctx, EnsureReq{Generation: 0, Wait: time.Second}, ""); !errors.Is(err, gate.ErrStaleGeneration) {
		t.Fatalf("stale ensure = %v", err)
	}
	if err := supervisor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", base[len("http://"):], 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("preview port remained open after Stop")
	}
}

func TestEnsureInheritsOnlyCurrentRunGeneration(t *testing.T) {
	g := &gate.Gate{}
	if _, err := g.AcquireRun(context.Background(), "run-current", 7); err != nil {
		t.Fatal(err)
	}
	supervisor := New(Config{}, g, nil, nil, nil)
	supervisor.state = contract.PreviewState{Status: "ready", Generation: 7, Commit: "current"}

	state, err := supervisor.Ensure(context.Background(), EnsureReq{}, "run-current")
	if err != nil || state.Generation != 7 || state.Status != "ready" {
		t.Fatalf("zero generation should inherit the current Run: state=%+v err=%v", state, err)
	}
	if _, err := supervisor.Ensure(context.Background(), EnsureReq{Generation: 6}, "run-current"); !errors.Is(err, gate.ErrStaleGeneration) {
		t.Fatalf("explicit stale generation must still fail: %v", err)
	}
	if _, err := supervisor.Ensure(context.Background(), EnsureReq{}, "another-run"); !errors.Is(err, gate.ErrStaleGeneration) {
		t.Fatalf("another Run must not borrow the gate generation: %v", err)
	}
}

func TestEnsureBackfillsCheckpointCommitWithoutRebuild(t *testing.T) {
	supervisor := New(Config{}, &gate.Gate{}, nil, nil, nil)
	supervisor.state = contract.PreviewState{Status: "ready", Generation: 3}
	state, err := supervisor.Ensure(context.Background(), EnsureReq{Generation: 3, Commit: "checkpoint"}, "")
	if err != nil || state.Status != "ready" || state.Commit != "checkpoint" {
		t.Fatalf("checkpoint commit was not backfilled: state=%+v err=%v", state, err)
	}
	state, err = supervisor.Ensure(context.Background(), EnsureReq{Generation: 3, Commit: "different"}, "")
	if err != nil || state.Commit != "checkpoint" {
		t.Fatalf("an idempotent ensure relabelled a non-empty commit: state=%+v err=%v", state, err)
	}
}

func TestPortConflictDoesNotKillUnknownListener(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	supervisor, _ := newTestSupervisor(t, writePreviewApp(t), port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := supervisor.Ensure(ctx, EnsureReq{Generation: 1, Wait: 2 * time.Second}, "")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.ErrorCode != "PORT_CONFLICT" {
		t.Fatalf("state = %+v", state)
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		t.Fatalf("unknown listener was affected: %v", err)
	}
	conn.Close()
}

func newTestSupervisor(t *testing.T, root string, port int) (*Supervisor, *fakeBuildControl) {
	t.Helper()
	fake := newFakeBuildControl()
	cp := cpclient.New(fake.server.URL, "cp-token")
	supervisor := New(Config{
		AppRoot: root, DataDir: filepath.Join(t.TempDir(), "data"), Mode: "live",
		BuildTimeout: 3 * time.Second, MaxRestarts: 1, LogLines: 100,
		Local: true, Port: port, ProbeInterval: 20 * time.Millisecond,
		ProbeTimeout: 200 * time.Millisecond, StopGrace: 50 * time.Millisecond,
		Env: map[string]string{
			"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME"), "LANG": "C.UTF-8",
			"LLM_PROXY_TOKEN": "must-not-leak", "WSD_TOKEN": "must-not-leak",
			"MAGENTS_AUTH_TOKEN": "must-not-leak", "OTHER_SECRET": "must-not-leak",
		},
	}, &gate.Gate{}, proc.New(), cp, bus.New())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = supervisor.Stop(ctx)
		fake.server.Close()
	})
	return supervisor, fake
}

func writePreviewApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	python := `import json, os
from http.server import BaseHTTPRequestHandler, HTTPServer
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/":
            body = b"ok"
        elif self.path == "/environment":
            keys = ["PATH", "DATA_DIR", "PORT", "LLM_PROXY_TOKEN", "WSD_TOKEN", "MAGENTS_AUTH_TOKEN", "OTHER_SECRET"]
            body = json.dumps({key: os.environ.get(key, "") for key in keys}).encode()
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json" if self.path != "/" else "text/plain")
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
	script := "#!/usr/bin/env bash\nset -euo pipefail\necho starting preview\nexec python3 \"$PWD/server.py\"\n"
	if err := os.WriteFile(filepath.Join(root, "start.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
