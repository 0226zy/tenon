package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// toolAdapter 把一件远端 MCP 工具适配为 port.Tool。
// 审批、spill、Hook 等既有管道对本工具同样生效（安全边界不旁路）。
type toolAdapter struct {
	client    *Client
	server    string // server 名（描述前缀与 spill 归类）
	remote    RemoteTool
	timeout   time.Duration
	maxOutput int
}

// 本地工具命名前缀：避免与内置工具及其他 server 的工具撞名。
const toolPrefix = "mcp__"

func (a *toolAdapter) fullName() string { return toolPrefix + a.server + "__" + a.remote.Name }

// Definition 实现 port.Tool：参数 schema 直接透传远端 inputSchema。
func (a *toolAdapter) Definition() entity.ToolDefinition {
	params := a.remote.InputSchema
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	desc := a.remote.Description
	if desc == "" {
		desc = "（远端工具未提供描述）"
	}
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name:        a.fullName(),
			Description: fmt.Sprintf("[MCP server: %s] %s", a.server, desc),
			Parameters:  params,
		},
	}
}

// ConcurrencySafe 实现 port.Tool：v1 客户端串行化 RPC，不可并行。
func (a *toolAdapter) ConcurrencySafe() bool { return false }

// Execute 实现 port.Tool：协议错误/超时/崩溃与远端 isError 一律回注为
// 普通工具错误结果（错误即输入），不终止会话。
func (a *toolAdapter) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	text, isErr, err := a.client.CallTool(ctx, a.remote.Name, argsJSON)
	if err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("MCP 工具 %s 调用失败: %v", a.fullName(), err)}
	}
	out := truncate(text, a.maxOutput, func() string { return tctx.Spill(a.fullName(), text) })
	if isErr {
		if out == "" {
			out = "（远端工具返回错误，无输出）"
		}
		return entity.ToolResult{IsError: true, Content: out}
	}
	if out == "" {
		out = "(MCP 工具执行成功，无输出)"
	}
	return entity.ToolResult{Content: out}
}

// truncate 与 tools 包同策略的超长截断（保留头尾，全文 spill 落盘）。
// 在本地复制一份，避免 adapter 之间的包依赖。
func truncate(s string, maxBytes int, spill func() string) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	head := maxBytes * 2 / 5
	tail := maxBytes * 2 / 5
	path := ""
	if spill != nil {
		path = spill()
	}
	marker := fmt.Sprintf("\n…… [中间省略 %d 字节；完整输出已保存到 %s，可用 read_file 分段查看] ……\n",
		len(s)-head-tail, path)
	return s[:head] + marker + s[len(s)-tail:]
}
