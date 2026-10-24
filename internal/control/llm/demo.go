package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

type demoDecision struct {
	content string
	call    *toolCall
	finish  string
}

func demoDecide(req chatRequest) demoDecision {
	seen := map[string]bool{}
	failed := []string{}
	writeCount := 0
	requestText := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			// A dsh session spans multiple Runs. Tool results before the latest
			// user message belong to earlier turns and must not suppress this turn.
			requestText = m.Content
			seen = map[string]bool{}
			failed = nil
			writeCount = 0
			continue
		}
		if m.Role == "tool" {
			seen[m.Name] = true
			if m.Name == "write_file" {
				writeCount++
			}
			if m.IsError {
				failed = append(failed, m.Name)
			}
		}
	}
	previewName := demoPreviewName(req)
	switch strings.TrimSpace(requestText) {
	case "[e2e:markdown]":
		return demoDecision{content: demoMarkdown, finish: "stop"}
	case "[e2e:failure]":
		return demoFailureDecide(req, seen, failed, writeCount, previewName)
	case "[e2e:recover]":
		return demoRecoveryDecide(req, seen, failed, writeCount, previewName)
	}
	if !seen["write_file"] {
		call := newDemoCall(req, "write", "write_file", map[string]any{"path": "web/src/App.tsx", "content": demoApp(requestText)})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if seen["write_file"] && containsString(failed, "write_file") {
		return demoDecision{content: demoFailureText(failed, "文件写入失败，未继续执行测试或预览。"), finish: "stop"}
	}
	if !seen["run_tests"] {
		call := newDemoCall(req, "test", "run_tests", map[string]any{"command": "go test ./...", "timeout_seconds": 300})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if previewName != "" && !seen[previewName] {
		call := newDemoCall(req, "preview", previewName, map[string]any{})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if len(failed) > 0 {
		return demoDecision{content: demoFailureText(failed, "请展开对应工具卡片查看错误详情；本次结果不能按全部成功验收。"), finish: "stop"}
	}
	text := "[本地 demo] 已通过 dsh-go 工具链写入 web/src/App.tsx，go test ./... 执行成功，preview 已就绪。首页已发生可查看的实际改动。此回复由确定性本地 demo 生成，不是真实模型输出。"
	if previewName == "" {
		text = "[本地 demo] 已通过 dsh-go 工具链写入 web/src/App.tsx，go test ./... 执行成功。当前 Runtime 未提供 preview 工具，因此未启动预览。此回复由确定性本地 demo 生成，不是真实模型输出。"
	}
	return demoDecision{content: text, finish: "stop"}
}

func demoPreviewName(req chatRequest) string {
	for _, raw := range req.Tools {
		var v struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &v) == nil && strings.Contains(strings.ToLower(v.Function.Name), "preview") {
			return v.Function.Name
		}
	}
	return ""
}

const demoMarkdown = `# E2E Markdown 安全渲染

**E2E_BOLD_SENTINEL**

[E2E 安全链接](https://example.com/)

<script>window.__E2E_MARKDOWN_XSS__ = true</script>

<img src="x" onerror="window.__E2E_MARKDOWN_XSS__ = true">

此回复由确定性本地 demo 生成，不是真实模型输出。`

const demoFailureTestCommand = `(for ((i=1; i<=600; i++)); do printf 'E2E_TEST_FAILURE line=%03d payload=xxxxxxxxxxxxxxxx\n' "$i"; done; exit 17)`

const demoBrokenStartScript = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' 'E2E_PREVIEW_BUILD_FAILURE'
exit 42
`

var demoRecoveryFiles = []struct {
	path    string
	content string
}{
	{path: "start.sh", content: `#!/usr/bin/env bash
# Platform preview entrypoint. Keep the service in this process group.
set -euo pipefail
export GOWORK=off
cd "$(dirname "$0")"
export PORT="${PORT:-3000}"
export DATA_DIR="${DATA_DIR:-$PWD/data}"
mkdir -p "$DATA_DIR"
( cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build )
CGO_ENABLED=0 go build -o bin/server ./cmd/server
exec ./bin/server
`},
	{path: "e2e-tree/2.txt", content: "E2E fixture: e2e-tree/2.txt\nE2E_READONLY_SENTINEL=two\n"},
	{path: "e2e-tree/10.txt", content: "E2E fixture: e2e-tree/10.txt\n"},
	{path: "e2e-tree/alpha.txt", content: "E2E fixture: e2e-tree/alpha.txt\n"},
	{path: "e2e-tree/z-dir/nested.txt", content: "E2E fixture: e2e-tree/z-dir/nested.txt\n"},
	{path: ".e2e-hidden/secret.txt", content: "E2E_HIDDEN_FILE_PRESENT\n"},
}

func demoFailureDecide(req chatRequest, seen map[string]bool, failed []string, writeCount int, previewName string) demoDecision {
	if !seen["run_tests"] {
		call := newDemoCall(req, "e2e_failure_test", "run_tests", map[string]any{"command": demoFailureTestCommand, "timeout_seconds": 30})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if containsString(failed, "write_file") {
		return demoDecision{content: demoFailureText(failed, "故障夹具文件写入失败，未继续调用预览。"), finish: "stop"}
	}
	if writeCount == 0 {
		call := newDemoCall(req, "e2e_failure_write", "write_file", map[string]any{"path": "start.sh", "content": demoBrokenStartScript})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if previewName != "" && !seen[previewName] {
		call := newDemoCall(req, "e2e_failure_preview", previewName, map[string]any{})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if len(failed) > 0 {
		return demoDecision{content: demoFailureText(failed, "故障夹具已执行；请展开对应工具卡片查看错误详情，不得按成功处理。"), finish: "stop"}
	}
	return demoDecision{content: "[本地 demo] 故障夹具未产生实际工具错误，无法按故障场景验收；请检查 run_tests 与 preview 的原始结果。", finish: "stop"}
}

func demoRecoveryDecide(req chatRequest, seen map[string]bool, failed []string, writeCount int, previewName string) demoDecision {
	if len(failed) > 0 {
		return demoDecision{content: demoFailureText(failed, "恢复夹具未完整执行，不能宣称恢复成功。"), finish: "stop"}
	}
	if writeCount < len(demoRecoveryFiles) {
		fixture := demoRecoveryFiles[writeCount]
		call := newDemoCall(req, fmt.Sprintf("e2e_recover_write_%d", writeCount), "write_file", map[string]any{"path": fixture.path, "content": fixture.content})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if previewName != "" && !seen[previewName] {
		call := newDemoCall(req, "e2e_recover_preview", previewName, map[string]any{})
		return demoDecision{call: &call, finish: "tool_calls"}
	}
	if previewName == "" {
		return demoDecision{content: "[本地 demo] 已通过 6 次真实 write_file 写入恢复夹具，但当前 Runtime 未提供 preview 工具，恢复场景未完整验收。", finish: "stop"}
	}
	return demoDecision{content: "[本地 demo] 已通过 6 次真实 write_file 写入恢复夹具，并由真实 preview 恢复预览。此回复由确定性本地 demo 生成，不是真实模型输出。", finish: "stop"}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func demoFailureText(failed []string, detail string) string {
	return "[本地 demo] 工具执行失败：" + strings.Join(failed, "、") + "。" + detail + "此回复由确定性本地 demo 生成，不是真实模型输出。"
}
func newDemoCall(req chatRequest, stage, name string, args any) toolCall {
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", stage, name, len(req.Messages))))
	var c toolCall
	c.ID = "call_demo_" + stage + "_" + hex.EncodeToString(sum[:6])
	c.Type = "function"
	c.Function.Name = name
	c.Function.Arguments = string(raw)
	return c
}
func demoApp(request string) string {
	quoted, _ := json.Marshal(request)
	return fmt.Sprintf(`const request = %s;

export default function App() {
  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 880, margin: "48px auto", padding: "0 24px" }}>
      <div style={{ display: "inline-block", padding: "6px 10px", borderRadius: 8, background: "#fff2cc", color: "#6b4e00", fontSize: 13 }}>
        LOCAL DEMO · 本地确定性演示
      </div>
      <h1 style={{ fontSize: 42, marginBottom: 12 }}>原型已真实修改</h1>
      <p style={{ fontSize: 18, lineHeight: 1.7 }}>
        这个页面由 control 的 local demo 通过 dsh-go write_file 工具写入，随后执行测试并请求预览。
      </p>
      <section style={{ marginTop: 28, padding: 20, border: "1px solid #ddd", borderRadius: 12 }}>
        <h2 style={{ marginTop: 0, fontSize: 18 }}>本次需求</h2>
        <p style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>{request}</p>
      </section>
    </main>
  );
}
`, quoted)
}
func demoUsage(req chatRequest, d demoDecision) tokenUsage {
	raw, _ := json.Marshal(req)
	completion := len(d.content)
	if d.call != nil {
		completion = len(d.call.Function.Arguments)
	}
	return tokenUsage{PromptTokens: max(1, len(raw)/4), CompletionTokens: max(1, completion/4)}
}
func (p *Proxy) serveDemo(w http.ResponseWriter, r *http.Request, req chatRequest) tokenUsage {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		if strings.TrimSpace(req.Messages[i].Content) == "[e2e:slow]" {
			time.Sleep(2500 * time.Millisecond)
		}
		break
	}
	d := demoDecide(req)
	u := demoUsage(req, d)
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	if req.Stream {
		p.demoStream(w, r, req.Model, d, u)
		return u
	}
	message := map[string]any{"role": "assistant", "content": d.content}
	if d.call != nil {
		message["tool_calls"] = []toolCall{*d.call}
	}
	response := map[string]any{"id": "chatcmpl-local-demo", "object": "chat.completion", "created": 0, "model": req.Model, "demo": true, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": d.finish}}, "usage": u}
	raw, _ := json.Marshal(response)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
	return u
}
func (p *Proxy) demoStream(w http.ResponseWriter, r *http.Request, model string, d demoDecision, u tokenUsage) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	id := "chatcmpl-local-demo"
	send := func(delta map[string]any, finish any) {
		chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": 0, "model": model, "demo": true, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send(map[string]any{"role": "assistant", "content": ""}, nil)
	if d.call != nil {
		send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": d.call.ID, "type": "function", "function": map[string]any{"name": d.call.Function.Name, "arguments": d.call.Function.Arguments}}}}, nil)
		send(map[string]any{}, d.finish)
	} else {
		for _, part := range splitDemo(d.content, 12) {
			send(map[string]any{"content": part}, nil)
		}
		send(map[string]any{}, d.finish)
	}
	raw, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": 0, "model": model, "demo": true, "choices": []any{}, "usage": u})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
func splitDemo(s string, n int) []string {
	out := []string{}
	for len(s) > 0 {
		size := 0
		for size < len(s) {
			_, w := utf8.DecodeRuneInString(s[size:])
			if size+w > n {
				break
			}
			size += w
		}
		if size == 0 {
			break
		}
		out = append(out, s[:size])
		s = s[size:]
	}
	return out
}
