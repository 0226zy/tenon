package deps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0226zy/protoforge/agent-runtime/internal/domain/plugin"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/port"
)

func TestRegisterPreviewFromEnv(t *testing.T) {
	type seenRequest struct {
		auth  string
		runID string
	}
	seen := make(chan seenRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- seenRequest{auth: r.Header.Get("Authorization"), runID: r.Header.Get("X-Run-Id")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready","generation":3,"commit":"abc","url_path":"/preview/","probe":{"code":200,"ms":1},"log_tail":"ok"}`))
	}))
	defer srv.Close()
	t.Setenv("WSD_URL", srv.URL)
	t.Setenv("WSD_TOKEN", "env-wsd-secret")

	registry := plugin.NewRegistry()
	if err := registerPreviewFromEnv(registry); err != nil {
		t.Fatalf("注册 preview: %v", err)
	}
	tool, ok := registry.Lookup("preview")
	if !ok {
		t.Fatal("配置 WSD_URL 后应注册 preview")
	}
	result := tool.Execute(context.Background(), `{}`, &port.ToolContext{RunID: "run-deps-preview"})
	if result.IsError {
		t.Fatalf("preview 调用失败: %s", result.Content)
	}
	request := <-seen
	if request.auth != "Bearer env-wsd-secret" || request.runID != "run-deps-preview" {
		t.Fatalf("环境装配或 RunID 错误: auth=%q, run_id=%q", request.auth, request.runID)
	}
}
