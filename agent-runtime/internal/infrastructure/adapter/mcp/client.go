// Package mcp 是 MCP（Model Context Protocol）客户端适配器：
// 把外部 MCP server（stdio 传输）提供的工具翻译注册进工具注册表，
// domain 与 application 层零改动——这正是端口模式预留的替换点。
//
// 纪律（指引 §8）：只实现协议客户端（JSON-RPC 2.0 over stdio，换行分隔 JSON），
// 不引入外部 MCP SDK——抄设计，不抄依赖。
//
// 安全：MCP 工具的每次调用同样经过 Hook 与审批通道（server 声明 read_only 的
// 工具才进免审批名单）；协议错误、超时、server 崩溃一律以普通工具结果回注
// （错误即输入），不终止会话。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	toolenv "github.com/0226zy/proto-platform/agent-runtime/internal/infrastructure/adapter/tools"
)

// protocolVersion 是握手声明的 MCP 协议版本。
const protocolVersion = "2024-11-05"

// ServerConfig 是一个 MCP server 的接入配置（由组合根从 agent-runtime.yaml 转换而来）。
type ServerConfig struct {
	Name         string   // 服务名（工具名前缀 mcp__<name>__<tool>）
	Command      string   // 可执行文件
	Args         []string // 启动参数
	Env          []string // 追加的环境变量（KEY=VALUE）
	EnvAllowlist []string // 可继承的父进程环境白名单（空 = 安全默认值）
	// ReadOnly 声明该 server 的全部工具为只读（进入免审批名单）；
	// 只有确认 server 不修改环境时才应开启。
	ReadOnly       bool
	TimeoutSeconds int // 单次 RPC 超时（0 = 60）
}

// RemoteTool 是 MCP server 声明的一件工具（tools/list 响应条目）。
type RemoteTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// Client 是一个 MCP server 子进程的 JSON-RPC 连接（stdio 传输）。
// v1 串行化请求（互斥锁），server 崩溃后标记不可用。
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	rd     *bufio.Reader
	stderr *lockedBuffer

	mu     sync.Mutex // 串行化 rpc
	nextID int
	dead   atomic.Bool
}

// childEnv 同时约束继承项与显式配置项；显式 KEY 仅对该 server 额外放行。
// FilterEnv 始终剔除 Runtime 敏感令牌，即使它们出现在白名单或显式配置中。
func childEnv(cfg ServerConfig, inherited []string) []string {
	base := toolenv.FilterEnv(inherited, cfg.EnvAllowlist)
	explicit := make([]string, 0, len(cfg.Env))
	keys := make([]string, 0, len(cfg.Env))
	for _, item := range cfg.Env {
		i := strings.IndexByte(item, '=')
		if i <= 0 {
			continue
		}
		keys = append(keys, item[:i])
		explicit = append(explicit, item)
	}
	if len(keys) == 0 {
		return base
	}
	return append(base, toolenv.FilterEnv(explicit, keys)...)
}

// Start 启动 server 子进程并完成 initialize 握手。
func Start(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if cfg.Name == "" || cfg.Command == "" {
		return nil, fmt.Errorf("MCP server 配置不完整：name / command 均必填")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = childEnv(cfg, os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrBuf := &lockedBuffer{}
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 MCP server %q（%s）: %w", cfg.Name, cfg.Command, err)
	}
	c := &Client{name: cfg.Name, cmd: cmd, stdin: stdin, rd: bufio.NewReaderSize(stdout, 256*1024), stderr: stderrBuf}
	go func() { _ = cmd.Wait(); c.dead.Store(true) }()

	if _, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agent-runtime", "version": "0.1.0"},
	}); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("MCP server %q 初始化握手失败: %w%s", cfg.Name, err, c.stderrHint())
	}
	// initialized 通知：无 id，不等响应
	notify, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if _, err := c.stdin.Write(append(notify, '\n')); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("MCP server %q 发送 initialized 通知失败: %w", cfg.Name, err)
	}
	return c, nil
}

// ListTools 拉取 server 声明的全部工具。
func (c *Client) ListTools(ctx context.Context) ([]RemoteTool, error) {
	res, err := c.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []RemoteTool `json:"tools"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("解析 tools/list 响应失败: %w", err)
	}
	return out.Tools, nil
}

// CallTool 调用一件远端工具，返回拼接的文本内容与 isError 标记。
func (c *Client) CallTool(ctx context.Context, name, argsJSON string) (string, bool, error) {
	var args any
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", false, fmt.Errorf("参数不是合法 JSON: %w", err)
		}
	}
	res, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", false, err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", false, fmt.Errorf("解析 tools/call 响应失败: %w", err)
	}
	var sb strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(part.Text)
		}
	}
	return sb.String(), out.IsError, nil
}

// Close 终止 server 子进程。
func (c *Client) Close() error {
	c.dead.Store(true)
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return nil
}

// ── JSON-RPC 2.0（换行分隔）──────────────────────────────────────

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpc 串行执行一次请求-响应（跳过 notification 与乱序响应）。
// 超时/取消即杀掉 server 进程——响应流状态已不可信。
func (c *Client) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead.Load() {
		return nil, fmt.Errorf("MCP server %q 进程已终止%s", c.name, c.stderrHint())
	}

	c.nextID++
	id := c.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(raw, '\n')); err != nil {
		return nil, fmt.Errorf("写入请求失败: %w%s", err, c.stderrHint())
	}

	type resp struct {
		result json.RawMessage
		err    error
	}
	ch := make(chan resp, 1)
	go func() {
		for {
			line, err := c.rd.ReadBytes('\n')
			if err != nil {
				ch <- resp{err: fmt.Errorf("读取响应失败: %w%s", err, c.stderrHint())}
				return
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *rpcError       `json:"error"`
			}
			if err := json.Unmarshal(trimSpace(line), &msg); err != nil {
				continue // 无法解析的行跳过
			}
			if msg.Method != "" && len(msg.ID) == 0 {
				continue // server → client notification
			}
			var gotID int
			if err := json.Unmarshal(msg.ID, &gotID); err != nil || gotID != id {
				continue // 乱序/其他响应
			}
			if msg.Error != nil {
				ch <- resp{err: fmt.Errorf("RPC 错误 %d: %s", msg.Error.Code, msg.Error.Message)}
			} else {
				ch <- resp{result: msg.Result}
			}
			return
		}
	}()

	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		_ = c.Close() // 杀掉进程后读 goroutine 随 stdout 关闭退出
		return nil, fmt.Errorf("MCP 调用超时或被取消（server 已终止）: %w", ctx.Err())
	}
}

func (c *Client) stderrHint() string {
	s := strings.TrimSpace(c.stderr.String())
	if s == "" {
		return ""
	}
	const max = 300
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return "（stderr: " + s + "）"
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 {
		last := b[len(b)-1]
		if last == ' ' || last == '\t' || last == '\n' || last == '\r' {
			b = b[:len(b)-1]
		} else {
			break
		}
	}
	return b
}

// lockedBuffer 收集 server 的 stderr（限长），用于错误诊断。
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const max = 4096
	if b.buf.Len() < max {
		remain := max - b.buf.Len()
		if len(p) > remain {
			b.buf.Write(p[:remain])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// timeoutOf 返回配置超时（0/负 = 60 秒）。
func (cfg ServerConfig) timeoutOf() time.Duration {
	if cfg.TimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(cfg.TimeoutSeconds) * time.Second
}
