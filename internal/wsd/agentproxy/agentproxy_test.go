package agentproxy

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWhitelistAndCredentialReplacement(t *testing.T) {
	var calls atomic.Int32
	var gotPath, gotQuery, gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler := New(target, "dshd-secret", nil)
	req := httptest.NewRequest(http.MethodPost, "/agent/sessions?x=1", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wsd-secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated || gotPath != "/v1/sessions" || gotQuery != "x=1" || gotAuth != "Bearer dshd-secret" {
		t.Fatalf("proxy = %d %s?%s %q", resp.Code, gotPath, gotQuery, gotAuth)
	}
	for _, bad := range []struct{ method, path string }{
		{http.MethodGet, "/agent/models"},
		{http.MethodPost, "/agent/sessions/s1/fork"},
		{http.MethodDelete, "/agent/sessions/s1"},
		{http.MethodGet, "/agent/sessions/s1/runs/r1/start"},
	} {
		resp = httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(bad.method, bad.path, nil))
		if resp.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d", bad.method, bad.path, resp.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("disallowed calls reached dshd: %d", calls.Load())
	}
}

func TestSSEFlushesBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: two\n\n")
		w.(http.Flusher).Flush()
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	proxy := httptest.NewServer(New(target, "secret", nil))
	defer proxy.Close()
	resp, err := http.Get(proxy.URL + "/agent/sessions/s1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line := make(chan string, 1)
	go func() {
		value, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- value
	}()
	select {
	case value := <-line:
		if value != "data: one\n" {
			t.Fatalf("first frame = %q", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first SSE frame was buffered")
	}
	unblock()
	_, _ = io.Copy(io.Discard, resp.Body)
}
