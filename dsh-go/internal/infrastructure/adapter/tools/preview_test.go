package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type previewSeenRequest struct {
	method  string
	path    string
	wait    string
	auth    string
	runID   string
	payload map[string]any
}

func TestPreviewToolReadyAndFailed(t *testing.T) {
	tests := []struct {
		name       string
		args       string
		wantWait   string
		response   map[string]any
		wantError  bool
		wantStatus string
		wantGen    float64
		wantCommit string
		wantLog    string
	}{
		{
			name:     "ready 返回探活与日志并校验请求头",
			args:     `{"wait_seconds":17,"generation":7,"commit":"abc123"}`,
			wantWait: "17",
			response: map[string]any{
				"status": "ready", "generation": 7, "commit": "abc123",
				"probe": map[string]any{"code": 200, "ms": 12}, "log_tail": "preview ready",
			},
			wantStatus: "ready", wantGen: 7, wantCommit: "abc123", wantLog: "preview ready",
		},
		{
			name:     "failed 保留响应并标记错误",
			args:     `{"generation":8,"commit":"def456"}`,
			wantWait: "90",
			response: map[string]any{
				"status": "failed", "generation": 8, "commit": "def456",
				"probe": map[string]any{"code": 500, "ms": 20}, "log_tail": "build failed",
			},
			wantError: true, wantStatus: "failed", wantGen: 8, wantCommit: "def456", wantLog: "build failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(chan previewSeenRequest, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := previewSeenRequest{
					method: r.Method, path: r.URL.Path, wait: r.URL.Query().Get("wait"),
					auth: r.Header.Get("Authorization"), runID: r.Header.Get("X-Run-Id"),
					payload: map[string]any{},
				}
				_ = json.NewDecoder(r.Body).Decode(&request.payload)
				seen <- request
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer srv.Close()

			tool := NewPreviewTool(srv.URL, "wsd-secret")
			tctx := testCtx(t.TempDir())
			tctx.RunID = "run-preview-42"
			result := tool.Execute(context.Background(), tt.args, tctx)
			if result.IsError != tt.wantError {
				t.Fatalf("IsError = %v，期望 %v；content=%s", result.IsError, tt.wantError, result.Content)
			}
			request := <-seen
			if request.method != http.MethodPost || request.path != "/preview/ensure" || request.wait != tt.wantWait {
				t.Fatalf("请求 = %s %s?wait=%s", request.method, request.path, request.wait)
			}
			if request.auth != "Bearer wsd-secret" || request.runID != "run-preview-42" {
				t.Fatalf("鉴权或 RunID 错误：auth=%q, run_id=%q", request.auth, request.runID)
			}
			if request.payload["generation"] != tt.wantGen || request.payload["commit"] != tt.wantCommit {
				t.Fatalf("请求载荷错误：%+v", request.payload)
			}

			var state struct {
				Status     string  `json:"status"`
				Generation float64 `json:"generation"`
				Commit     string  `json:"commit"`
				Probe      struct {
					Code int `json:"code"`
				} `json:"probe"`
				LogTail string `json:"log_tail"`
			}
			if err := json.Unmarshal([]byte(result.Content), &state); err != nil {
				t.Fatalf("工具结果不是 JSON：%v", err)
			}
			if state.Status != tt.wantStatus || state.Generation != tt.wantGen || state.Commit != tt.wantCommit || state.LogTail != tt.wantLog {
				t.Fatalf("preview 返回状态不完整：%+v", state)
			}
			if state.Probe.Code != tt.response["probe"].(map[string]any)["code"] {
				t.Fatalf("probe 未透传：%+v", state.Probe)
			}
		})
	}
}

func TestPreviewToolRejectsInvalidWait(t *testing.T) {
	tool := NewPreviewTool("http://127.0.0.1", "secret")
	result := tool.Execute(context.Background(), `{"wait_seconds":91}`, testCtx(t.TempDir()))
	if !result.IsError {
		t.Fatalf("wait_seconds=91 应失败：%+v", result)
	}
}
