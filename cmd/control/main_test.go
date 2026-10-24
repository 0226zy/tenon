package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/internal/control/app"
	"github.com/0226zy/proto-platform/internal/kit/config"
)

func testConfigFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	models := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(models, []byte("models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.yaml")
	body := fmt.Sprintf(`mode: local
listen:
  api: 127.0.0.1:0
  preview: 127.0.0.1:0
  internal: 127.0.0.1:0
  metrics: 127.0.0.1:0
web:
  origin: http://127.0.0.1:8080
control:
  secret_file: %s
sqlite:
  path: %s
auth:
  cookie_secure: false
  argon2: {time: 1, memory: 1024, threads: 1}
catalog:
  file: %s
sandbox:
  driver: local
local:
  root: %s
  wsd_bin: %s
  template_dir: %s
  dshd_bin: %s
preview:
  domain: localhost
  scheme: http
  public_address: "{key}.preview.localhost:8081"
  cookie_secure: false
`, filepath.Join(dir, "secret.key"), filepath.Join(dir, "control.db"), models, filepath.Join(dir, "sandboxes"), filepath.Join(dir, "wsd"), filepath.Join(dir, "template"), filepath.Join(dir, "dshd"))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIUserUsageStatusAndServeLifecycle(t *testing.T) {
	configPath := testConfigFile(t)
	var stdout, stderr bytes.Buffer
	run := func(args ...string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		index := 1
		if args[0] == "user" {
			index = 2
		}
		argv := append([]string{}, args[:index]...)
		argv = append(argv, "-config", configPath)
		argv = append(argv, args[index:]...)
		if code := execute(context.Background(), argv, &stdout, &stderr); code != 0 {
			t.Fatalf("execute %v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	run("user", "add", "-user", "alice", "-name", "Alice", "-password", "correct-horse")
	if !strings.Contains(stdout.String(), "user alice created") {
		t.Fatalf("unexpected add output: %s", stdout.String())
	}
	run("status")
	var status map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status["mode"] != "local" {
		t.Fatalf("status=%s err=%v", stdout.String(), err)
	}
	run("usage", "-user", "alice")
	var usage struct{ Items []any }
	if err := json.Unmarshal(stdout.Bytes(), &usage); err != nil || usage.Items == nil {
		t.Fatalf("usage=%s err=%v", stdout.String(), err)
	}
	run("user", "passwd", "-user", "alice", "-password", "new-password")
	run("user", "disable", "-user", "alice")

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, io.Discard) }()
	select {
	case err := <-done:
		t.Fatalf("serve exited before cancellation: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestCLIPasswordChangeAndDisableRevokeLoginAndSessions(t *testing.T) {
	configPath := testConfigFile(t)
	var stdout, stderr bytes.Buffer
	runCLI := func(args ...string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		index := 1
		if args[0] == "user" {
			index = 2
		}
		argv := append([]string{}, args[:index]...)
		argv = append(argv, "-config", configPath)
		argv = append(argv, args[index:]...)
		if code := execute(context.Background(), argv, &stdout, &stderr); code != 0 {
			t.Fatalf("execute %v code=%d stderr=%s", args, code, stderr.String())
		}
	}
	runCLI("user", "add", "-user", "alice", "-name", "Alice", "-password", "correct-horse")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	login := func(password string) (*httptest.ResponseRecorder, *http.Cookie) {
		t.Helper()
		body := fmt.Sprintf(`{"username":"alice","password":%q}`, password)
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
		req.Header.Set("Origin", cfg.Web.Origin)
		recorder := httptest.NewRecorder()
		a.APIHandler.ServeHTTP(recorder, req)
		var session *http.Cookie
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == cfg.Auth.CookieName {
				session = cookie
			}
		}
		return recorder, session
	}
	me := func(session *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.AddCookie(session)
		recorder := httptest.NewRecorder()
		a.APIHandler.ServeHTTP(recorder, req)
		return recorder
	}

	initial, oldSession := login("correct-horse")
	if initial.Code != http.StatusNoContent || oldSession == nil {
		t.Fatalf("initial login=%d %s", initial.Code, initial.Body.String())
	}
	runCLI("user", "passwd", "-user", "alice", "-password", "new-password")
	if got, _ := login("correct-horse"); got.Code != http.StatusUnauthorized {
		t.Fatalf("old password after passwd=%d %s", got.Code, got.Body.String())
	}
	if got := me(oldSession); got.Code != http.StatusUnauthorized {
		t.Fatalf("pre-passwd session after passwd=%d %s", got.Code, got.Body.String())
	}
	changed, newSession := login("new-password")
	if changed.Code != http.StatusNoContent || newSession == nil {
		t.Fatalf("new password login=%d %s", changed.Code, changed.Body.String())
	}
	if got := me(newSession); got.Code != http.StatusOK {
		t.Fatalf("new session before disable=%d %s", got.Code, got.Body.String())
	}
	runCLI("user", "disable", "-user", "alice")
	if got, _ := login("new-password"); got.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user login=%d %s", got.Code, got.Body.String())
	}
	if got := me(newSession); got.Code != http.StatusUnauthorized {
		t.Fatalf("pre-disable session after disable=%d %s", got.Code, got.Body.String())
	}
}
