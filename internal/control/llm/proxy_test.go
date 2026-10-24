package llm

import (
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

	"github.com/0226zy/protoforge/internal/control/sandbox"
	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/internal/kit/config"
)

type staticAuthorizer struct {
	runID string
	err   error
}

func (a staticAuthorizer) RequireActiveRun(context.Context, sandbox.Principal) (string, error) {
	return a.runID, a.err
}

func proxyTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func proxyCatalog(t *testing.T, local bool, model string) *Catalog {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(model), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(path, local)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func newTestProxy(catalog *Catalog, st *store.Store, runID string) *Proxy {
	usage := &UsageService{St: st}
	cfg := config.LLM{PerConvConcurrency: 4, PerUserQPS: 100, PerRunTokenLimit: 1000000, MaxTokensCap: 8192, UpstreamTimeout: config.Duration{Duration: 5 * time.Second}}
	return NewProxy(catalog, staticAuthorizer{runID: runID}, usage, NewLimiter(4, 100), cfg)
}

func proxyRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/llm/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return sandbox.WithPrincipal(req, sandbox.Principal{ConvID: "abcdefghij12", UserID: "alice", InstID: "inst"})
}

func TestUpstream429AndRetryAfterArePreservedAndMetered(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"busy"},"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	catalog := proxyCatalog(t, false, fmt.Sprintf("models:\n  - id: fixture\n    default: true\n    base_url: %s\n", upstream.URL))
	st := proxyTestStore(t)
	proxy := newTestProxy(catalog, st, "run-1")
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, proxyRequest(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`))
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "17" {
		t.Fatalf("status=%d retry=%q body=%s", response.StatusCode, response.Header.Get("Retry-After"), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "busy") {
		t.Fatalf("upstream body was replaced: %s", recorder.Body.String())
	}
	prompt, completion, err := st.SumUsageByRun(context.Background(), st.DB(), "run-1")
	if err != nil || prompt != 3 || completion != 2 {
		t.Fatalf("usage prompt=%d completion=%d err=%v", prompt, completion, err)
	}
	var status int
	if err := st.DB().QueryRow(`SELECT Fhttp_status FROM t_llm_usage WHERE Frun_id=?`, "run-1").Scan(&status); err != nil || status != 429 {
		t.Fatalf("recorded status=%d err=%v", status, err)
	}
}

func TestUpstream5xxBodyRetryAfterAndUsageArePreserved(t *testing.T) {
	for _, upstreamStatus := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(upstreamStatus), func(t *testing.T) {
			payload := fmt.Sprintf(`{"error":{"message":"upstream %d"},"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`, upstreamStatus)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "23")
				w.WriteHeader(upstreamStatus)
				_, _ = io.WriteString(w, payload)
			}))
			defer upstream.Close()
			catalog := proxyCatalog(t, false, fmt.Sprintf("models:\n  - id: fixture\n    default: true\n    base_url: %s\n", upstream.URL))
			st := proxyTestStore(t)
			proxy := newTestProxy(catalog, st, "run-5xx")
			recorder := httptest.NewRecorder()
			proxy.ServeHTTP(recorder, proxyRequest(`{"model":"fixture","messages":[{"role":"user","content":"hello"}]}`))
			if recorder.Code != upstreamStatus || recorder.Header().Get("Retry-After") != "23" || recorder.Body.String() != payload {
				t.Fatalf("status=%d retry=%q body=%q", recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
			}
			prompt, completion, err := st.SumUsageByRun(context.Background(), st.DB(), "run-5xx")
			if err != nil || prompt != 7 || completion != 5 {
				t.Fatalf("usage prompt=%d completion=%d err=%v", prompt, completion, err)
			}
			var recordedStatus int
			if err := st.DB().QueryRow(`SELECT Fhttp_status FROM t_llm_usage WHERE Frun_id=?`, "run-5xx").Scan(&recordedStatus); err != nil || recordedStatus != upstreamStatus {
				t.Fatalf("recorded status=%d err=%v", recordedStatus, err)
			}
		})
	}
}

type demoStreamResult struct {
	call  *toolCall
	text  string
	usage tokenUsage
}

func runDemoRequest(t *testing.T, proxy *Proxy, req chatRequest) demoStreamResult {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, proxyRequest(string(raw)))
	if recorder.Code != 200 || recorder.Header().Get("X-Proto-Demo") != "true" {
		t.Fatalf("demo status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	var result demoStreamResult
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string     `json:"content"`
					ToolCalls []toolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *tokenUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("invalid demo chunk %q: %v", line, err)
		}
		for _, choice := range chunk.Choices {
			result.text += choice.Delta.Content
			if len(choice.Delta.ToolCalls) > 0 {
				call := choice.Delta.ToolCalls[0]
				result.call = &call
			}
		}
		if chunk.Usage != nil {
			result.usage = *chunk.Usage
		}
	}
	if result.usage.TotalTokens == 0 {
		t.Fatal("demo stream did not include usage")
	}
	return result
}

func TestDemoStreamToolSequenceAndUsage(t *testing.T) {
	catalog := proxyCatalog(t, true, "models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n")
	st := proxyTestStore(t)
	proxy := newTestProxy(catalog, st, "run-demo")
	previewTool, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": "preview"}})
	req := chatRequest{Model: "local-demo", Stream: true, Messages: []chatMessage{{Role: "user", Content: "make an app"}}, Tools: []json.RawMessage{previewTool}}
	wantNames := []string{"write_file", "run_tests", "preview"}
	for _, want := range wantNames {
		result := runDemoRequest(t, proxy, req)
		if result.call == nil || result.call.Function.Name != want {
			t.Fatalf("tool=%+v want=%s", result.call, want)
		}
		if want == "write_file" {
			var args map[string]any
			if err := json.Unmarshal([]byte(result.call.Function.Arguments), &args); err != nil || args["path"] != "web/src/App.tsx" {
				t.Fatalf("write_file args=%s err=%v", result.call.Function.Arguments, err)
			}
		}
		req.Messages = append(req.Messages,
			chatMessage{Role: "assistant", ToolCalls: []toolCall{*result.call}},
			chatMessage{Role: "tool", Name: result.call.Function.Name, ToolCallID: result.call.ID, Content: "ok"},
		)
	}
	final := runDemoRequest(t, proxy, req)
	if final.call != nil || !strings.Contains(final.text, "本地 demo") || !strings.Contains(final.text, "preview") {
		t.Fatalf("final result=%+v", final)
	}
	rows, err := st.ReportUsage(context.Background(), "alice", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || len(rows) != 1 || rows[0].Requests != 4 || rows[0].PromptTokens == 0 || rows[0].ComplTokens == 0 {
		t.Fatalf("usage rows=%+v err=%v", rows, err)
	}
}

func TestDemoStartsFreshToolChainForEachUserTurn(t *testing.T) {
	catalog := proxyCatalog(t, true, "models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n")
	proxy := newTestProxy(catalog, proxyTestStore(t), "run-demo")
	req := chatRequest{Model: "local-demo", Stream: true, Messages: []chatMessage{
		{Role: "user", Content: "first turn"},
		{Role: "tool", Name: "write_file", Content: "ok"},
		{Role: "tool", Name: "run_tests", Content: "ok"},
		{Role: "tool", Name: "preview", Content: "ready"},
		{Role: "assistant", Content: "first turn complete"},
		{Role: "user", Content: "second turn"},
	}}
	result := runDemoRequest(t, proxy, req)
	if result.call == nil || result.call.Function.Name != "write_file" {
		t.Fatalf("previous Run tools suppressed the new user turn: %+v", result)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(result.call.Function.Arguments), &args); err != nil || !strings.Contains(args["content"].(string), "second turn") {
		t.Fatalf("new turn did not use the latest request: args=%s err=%v", result.call.Function.Arguments, err)
	}
}

func TestDemoDisclosesToolFailures(t *testing.T) {
	catalog := proxyCatalog(t, true, "models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n")
	proxy := newTestProxy(catalog, proxyTestStore(t), "run-demo")
	previewTool, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]any{"name": "preview"}})
	req := chatRequest{Model: "local-demo", Stream: true, Tools: []json.RawMessage{previewTool}, Messages: []chatMessage{
		{Role: "user", Content: "make an app"},
		{Role: "tool", Name: "write_file", Content: "ok"},
		{Role: "tool", Name: "run_tests", Content: "exit 1", IsError: true},
	}}
	result := runDemoRequest(t, proxy, req)
	if result.call == nil || result.call.Function.Name != "preview" {
		t.Fatalf("demo should still allow preview inspection after a test failure: %+v", result)
	}
	req.Messages = append(req.Messages, chatMessage{Role: "tool", Name: "preview", Content: "ready"})
	final := runDemoRequest(t, proxy, req)
	if final.call != nil || !strings.Contains(final.text, "工具执行失败：run_tests") || !strings.Contains(final.text, "不能按全部成功验收") || strings.Contains(final.text, "执行成功") {
		t.Fatalf("tool failure was not disclosed accurately: %+v", final)
	}

	writeFailure := chatRequest{Model: "local-demo", Stream: true, Messages: []chatMessage{
		{Role: "user", Content: "make an app"},
		{Role: "tool", Name: "write_file", Content: "denied", IsError: true},
	}}
	stopped := runDemoRequest(t, proxy, writeFailure)
	if stopped.call != nil || !strings.Contains(stopped.text, "未继续执行测试或预览") {
		t.Fatalf("write failure must stop the demo chain: %+v", stopped)
	}
}
