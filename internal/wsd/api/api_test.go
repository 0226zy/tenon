package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspacefs "github.com/0226zy/tenon/internal/wsd/fs"
	"github.com/0226zy/tenon/internal/wsd/gate"
	"github.com/0226zy/tenon/pkg/contract"
)

func TestRecovererPreservesStreamAbortSentinel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := recoverer(logger, "sandbox", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if recovered := recover(); recovered != http.ErrAbortHandler {
			t.Fatalf("recoverer swallowed or replaced stream abort: %v", recovered)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/agent/stream", nil))
}

func TestStrictBearerHealthGateAndETag(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := workspacefs.New(root, nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	server := NewServer(Deps{
		Token: "wsd-secret", Gate: &gate.Gate{}, FS: files,
		Health: func() contract.Health { return contract.Health{API: contract.WSDAPI, OK: true, Agent: "ok"} },
		Agent:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	})
	client := server.Handler
	request := func(method, target, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp := httptest.NewRecorder()
		client.ServeHTTP(resp, req)
		return resp
	}
	if resp := request(http.MethodGet, "/healthz", "", nil, nil); resp.Code != http.StatusOK {
		t.Fatalf("health = %d", resp.Code)
	}
	for _, token := range []string{"", "Bearer wrong", "bearer wsd-secret", "Bearer  wsd-secret"} {
		resp := request(http.MethodGet, "/gate", token, nil, nil)
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("token %q = %d", token, resp.Code)
		}
		var body contract.ErrorResponse
		_ = json.Unmarshal(resp.Body.Bytes(), &body)
		if body.Error.Code != "UNAUTHENTICATED" {
			t.Fatalf("error = %+v", body)
		}
	}
	bearer := "Bearer wsd-secret"
	if resp := request(http.MethodGet, "/agent/sessions/s1", bearer, nil, nil); resp.Code != http.StatusNoContent {
		t.Fatalf("authorized agent = %d", resp.Code)
	}
	resp := request(http.MethodPost, "/gate/acquire", bearer, []byte(`{"holder":"run","run_id":"run-1","generation":2}`), nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("acquire = %d %s", resp.Code, resp.Body.String())
	}
	resp = request(http.MethodGet, "/fs/file?path=main.go", bearer, nil, nil)
	if resp.Code != http.StatusOK || resp.Header().Get("ETag") == "" {
		t.Fatalf("file = %d, ETag=%q", resp.Code, resp.Header().Get("ETag"))
	}
	etag := resp.Header().Get("ETag")
	resp = request(http.MethodGet, "/fs/file?path=main.go", bearer, nil, map[string]string{"If-None-Match": etag})
	if resp.Code != http.StatusNotModified || resp.Body.Len() != 0 {
		t.Fatalf("conditional file = %d %q", resp.Code, resp.Body.String())
	}
	for _, target := range []string{"/fs/file?path=main.go%00", "/fs/tree?path=%00"} {
		if resp := request(http.MethodGet, target, bearer, nil, nil); resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "BAD_PATH") {
			t.Errorf("NUL path %s = %d %s", target, resp.Code, resp.Body.String())
		}
	}
	if resp := request(http.MethodPost, "/git/push", bearer, []byte(`{}`), nil); resp.Code != http.StatusNotImplemented {
		t.Fatalf("P1 push = %d", resp.Code)
	}
}
