package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0226zy/protoforge/agent-runtime/internal/domain/entity"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/port"
)

const previewMaxResponseBytes = 4 << 20

// PreviewTool 通过 wsd 启动或刷新预览；构建与工作区门控均由 wsd 负责。
type PreviewTool struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewPreviewTool(baseURL, token string) *PreviewTool {
	return &PreviewTool{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), token: token, client: &http.Client{}}
}

func (t *PreviewTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{Type: "function", Function: entity.FunctionSpec{
		Name: "preview",
		Description: `通过 wsd 启动或刷新当前工作区预览，并等待探活结果。
返回 status、generation、commit、probe、log_tail 等 wsd 原始状态；failed 或非 ready 状态按工具错误回注。
参数：wait_seconds 可选（1..90，默认 90）；generation、commit 可选，用于请求指定工作区版本。
Run 内调用会携带当前 X-Run-Id，由 wsd 复用该 Run 的执行门控，本工具不会重复申请锁。`,
		Parameters: schema(map[string]any{
			"wait_seconds": intProp("等待秒数，1..90，默认 90"),
			"generation":   intProp("可选的工作区 generation；省略时传 0"),
			"commit":       strProp("可选的代码 commit"),
		}),
	}}
}

func (t *PreviewTool) ConcurrencySafe() bool { return false }

type previewArgs struct {
	WaitSeconds int    `json:"wait_seconds"`
	Generation  int64  `json:"generation"`
	Commit      string `json:"commit"`
}

func (t *PreviewTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args previewArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return previewError("参数解析失败（应为 JSON 对象）：%v", err)
	}
	wait := args.WaitSeconds
	if wait == 0 {
		wait = 90
	}
	if wait < 1 || wait > 90 {
		return previewError("wait_seconds 应在 1..90 之间，当前为 %d", wait)
	}
	if args.Generation < 0 {
		return previewError("generation 不能为负数")
	}
	if t.baseURL == "" {
		return previewError("未配置 WSD_URL，无法调用 preview")
	}
	if t.token == "" {
		return previewError("未配置 WSD_TOKEN，无法调用 preview")
	}

	payload, err := json.Marshal(map[string]any{"generation": args.Generation, "commit": args.Commit})
	if err != nil {
		return previewError("编码 preview 请求失败：%v", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(wait+10)*time.Second)
	defer cancel()
	endpoint := t.baseURL + "/preview/ensure?wait=" + strconv.Itoa(wait)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return previewError("构造 preview 请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Content-Type", "application/json")
	if tctx != nil && tctx.RunID != "" {
		req.Header.Set("X-Run-Id", tctx.RunID)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return previewError("调用 wsd preview 失败：%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, previewMaxResponseBytes+1))
	if err != nil {
		return previewError("读取 preview 响应失败：%v", err)
	}
	if len(raw) > previewMaxResponseBytes {
		return previewError("preview 响应超过 %d 字节", previewMaxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(raw))
		if len(message) > 4096 {
			message = message[:4096] + "…"
		}
		return previewError("wsd preview 返回 %s：%s", resp.Status, message)
	}

	var state struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return previewError("解析 preview 响应失败：%v", err)
	}
	if state.Status == "" {
		return previewError("preview 响应缺少 status")
	}
	return entity.ToolResult{Content: string(raw), IsError: state.Status != "ready"}
}

func previewError(format string, args ...any) entity.ToolResult {
	return entity.ToolResult{IsError: true, Content: fmt.Sprintf(format, args...)}
}
