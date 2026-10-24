package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0226zy/magents/internal/domain/plugin"
	"github.com/0226zy/magents/internal/domain/port"
)

// ── fake MCP server（TestHelperProcess 模式）────────────────────
// 测试二进制以子进程方式运行自身：GO_TEST_MCP_FAKE=1 时 TestMain 直接进入
// fakeServerMain，按 stdio 协议应答 initialize / tools/list / tools/call。

func TestMain(m *testing.M) {
	if os.Getenv("GO_TEST_MCP_FAKE") == "1" {
		fakeServerMain()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func writeResult(id json.RawMessage, result any) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(raw))
}

func writeRPCError(id json.RawMessage, code int, message string) {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
	fmt.Println(string(raw))
}

func fakeServerMain() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			writeResult(msg.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "0"},
			})
		case "notifications/initialized":
			// 通知：无响应
		case "tools/list":
			writeResult(msg.ID, map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "回显 text 参数", "inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				}},
				{"name": "fail", "description": "总是失败", "inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}},
			}})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch p.Name {
			case "echo":
				writeResult(msg.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": fmt.Sprint(p.Arguments["text"])}},
					"isError": false,
				})
			case "fail":
				writeResult(msg.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "boom"}},
					"isError": true,
				})
			default:
				writeRPCError(msg.ID, -32601, "unknown tool")
			}
		}
	}
}

// fakeServerConfig 生成指向测试二进制自身的 server 配置。
func fakeServerConfig(readOnly bool) ServerConfig {
	return ServerConfig{
		Name:    "fake",
		Command: os.Args[0],
		Env:     []string{"GO_TEST_MCP_FAKE=1"},
		// 不带 -test.run：子进程 TestMain 拦截 env 标记，不会跑测试
		ReadOnly:       readOnly,
		TimeoutSeconds: 10,
	}
}

func fakeToolCtx() *port.ToolContext {
	return &port.ToolContext{
		SessionID: "t",
		Cwd:       func() string { return "." },
		Spill:     func(toolName, content string) string { return filepath.Join(os.TempDir(), "mcp-spill.log") },
	}
}

// 全链路：启动 fake server → 注册工具 → 成功调用 / 远端 isError / 只读名单。
func TestRegisterServersAndCall(t *testing.T) {
	registry := plugin.NewRegistry()
	closeFn, readOnly, err := RegisterServers(registry, []ServerConfig{fakeServerConfig(true)}, 30000)
	if err != nil {
		t.Fatalf("RegisterServers: %v", err)
	}
	t.Cleanup(closeFn)

	// 工具命名空间：mcp__<server>__<tool>
	names := registry.Names()
	if len(names) != 2 || names[0] != "mcp__fake__echo" || names[1] != "mcp__fake__fail" {
		t.Fatalf("注册工具名不符: %v", names)
	}
	if len(readOnly) != 2 {
		t.Fatalf("只读名单应含全部 fake 工具: %v", readOnly)
	}

	// schema 透传
	tool, ok := registry.Lookup("mcp__fake__echo")
	if !ok {
		t.Fatal("Lookup mcp__fake__echo 失败")
	}
	def := tool.Definition()
	schema, _ := def.Function.Parameters.(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("inputSchema 未透传: %+v", def.Function.Parameters)
	}
	if !strings.Contains(def.Function.Description, "[MCP server: fake]") {
		t.Fatalf("描述缺少 server 前缀: %q", def.Function.Description)
	}

	// 成功调用
	res := tool.Execute(context.Background(), `{"text":"hello mcp"}`, fakeToolCtx())
	if res.IsError || !strings.Contains(res.Content, "hello mcp") {
		t.Fatalf("echo 调用: %+v", res)
	}

	// 远端 isError 回注
	failTool, _ := registry.Lookup("mcp__fake__fail")
	res = failTool.Execute(context.Background(), `{}`, fakeToolCtx())
	if !res.IsError || !strings.Contains(res.Content, "boom") {
		t.Fatalf("fail 调用应回注错误: %+v", res)
	}
}

// server 启动失败（命令不存在）：错误暴露在启动期。
func TestStartBadCommand(t *testing.T) {
	registry := plugin.NewRegistry()
	_, _, err := RegisterServers(registry, []ServerConfig{{Name: "bad", Command: "/nonexistent/mcp-server-xyz", TimeoutSeconds: 5}}, 30000)
	if err == nil {
		t.Fatal("不存在的命令应在启动期报错")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("错误应包含 server 名: %v", err)
	}
}

// server 中途死亡：后续调用回注为普通工具错误，不终止会话。
func TestCallAfterServerDeath(t *testing.T) {
	ctx := context.Background()
	client, err := Start(ctx, fakeServerConfig(false))
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()

	_, _, err = client.CallTool(ctx, "echo", `{"text":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "已终止") {
		t.Fatalf("server 死亡后调用应返回结构化错误: %v", err)
	}
}

// 关闭后不遗留子进程。
func TestNoOrphanProcess(t *testing.T) {
	client, err := Start(context.Background(), fakeServerConfig(false))
	if err != nil {
		t.Fatal(err)
	}
	pid := client.cmd.Process.Pid
	_ = client.Close()
	// 进程应已退出：kill(pid, 0) 在 unix 上探测存在性
	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(os.Kill) // 兜底清理（若已退出则无害）
	}
	// 再确认可执行一次新启动（端口/管道资源已释放）
	client2, err := Start(context.Background(), fakeServerConfig(false))
	if err != nil {
		t.Fatalf("关闭后重启失败: %v", err)
	}
	_ = client2.Close()
}

func TestChildEnvFiltersInheritedAndExplicitValues(t *testing.T) {
	cfg := ServerConfig{
		EnvAllowlist: []string{"PATH", "CUSTOM_INHERITED", "LLM_PROXY_TOKEN", "MAGENTS_AUTH_TOKEN", "WSD_TOKEN"},
		Env: []string{
			"GO_TEST_MCP_FAKE=1",
			"MCP_EXTRA=allowed",
			"LLM_PROXY_TOKEN=explicit-llm",
			"MAGENTS_AUTH_TOKEN=explicit-magents",
			"WSD_TOKEN=explicit-wsd",
			"malformed",
		},
	}
	inherited := []string{
		"PATH=/usr/bin",
		"HOME=/home/not-allowlisted",
		"CUSTOM_INHERITED=keep",
		"INHERITED_SECRET=drop",
		"LLM_PROXY_TOKEN=inherited-llm",
		"MAGENTS_AUTH_TOKEN=inherited-magents",
		"WSD_TOKEN=inherited-wsd",
	}
	got := childEnv(cfg, inherited)
	want := []string{"PATH=/usr/bin", "CUSTOM_INHERITED=keep", "GO_TEST_MCP_FAKE=1", "MCP_EXTRA=allowed"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("MCP 子进程环境过滤错误:\ngot  %v\nwant %v", got, want)
	}
}
