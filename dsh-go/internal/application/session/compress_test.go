package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/magents/internal/application/agent"
	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
	"github.com/0226zy/magents/internal/infrastructure/adapter/storejsonl"
)

// 带已知 spill 目录的测试管理器（压缩断言要数 spill 文件）。
func newCompressTestManager(t *testing.T, spillDir string) *Manager {
	t.Helper()
	store, err := storejsonl.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(string) (port.LLMPort, error) { return echoLLM{}, nil }
	loop := agent.NewLoop(resolver, emptyTools{}, nil, nil, "系统提示词",
		agent.Limits{MaxIterations: 5, TokenBudget: 10000, LivelockRepeat: 3, MaxRetries: 1, CircuitBreakerFailures: 3},
		func(error) (bool, time.Duration) { return false, 0 })
	return NewManager(store, loop, nil, spillDir, time.Second)
}

// 造一段轨迹：user → assistant(tool_call c1) → tool 长输出 → assistant 收尾。
// 返回 runtime 与长输出原文。
func seedCompressibleTrace(t *testing.T, m *Manager) (*Runtime, string) {
	t.Helper()
	rt, err := m.Create(t.TempDir(), "auto-edit", "", "")
	if err != nil {
		t.Fatal(err)
	}
	longOut := strings.Repeat("编译日志行。", 60) // 远超 240 字符阈值
	for _, ev := range []entity.Event{
		{Kind: entity.EventMessage, Message: &entity.Message{Role: "user", Content: "帮我修这个 bug"}},
		{Kind: entity.EventMessage, Message: &entity.Message{Role: "assistant", ToolCalls: []entity.ToolCall{
			{ID: "c1", Type: "function", Function: entity.FunctionCall{Name: "bash", Arguments: `{"command":"go test ./..."}`}},
		}}},
		{Kind: entity.EventMessage, Message: &entity.Message{Role: "tool", ToolCallID: "c1", Name: "bash", Content: longOut}},
		{Kind: entity.EventMessage, Message: &entity.Message{Role: "assistant", Content: "看到失败原因了，继续修。"}},
		{Kind: entity.EventMessage, Message: &entity.Message{Role: "assistant", Content: "修复完成。"}},
	} {
		if _, err := rt.AppendEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	return rt, longOut
}

func countNotices(t *testing.T, rt *Runtime) int {
	t.Helper()
	n := 0
	for _, ev := range mustEvents(t, rt) {
		if ev.Kind == entity.EventNotice {
			n++
		}
	}
	return n
}

func mustEvents(t *testing.T, rt *Runtime) []entity.Event {
	t.Helper()
	events, err := rt.manager.Events(rt.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// 未达阈值：投影原样保留，不产生 spill。
func TestCompressionBelowThreshold(t *testing.T) {
	spillDir := t.TempDir()
	m := newCompressTestManager(t, spillDir)
	m.SetCompression(1000, 2)
	rt, longOut := seedCompressibleTrace(t, m)
	rt.ObservePrompt(500) // < 1000

	msgs := rt.Messages()
	if msgs[2].Content != longOut {
		t.Fatal("未达阈值时工具输出不应被压缩")
	}
	if countNotices(t, rt) != 0 {
		t.Fatal("未达阈值不应注入压缩 notice")
	}
}

// 达阈值：旧长输出被替换为「摘录 + spill 路径」，原文可取回；
// 首条 user 与最近 keepRecent 条逐字保留；tool_call 配对结构不变。
func TestCompressionTrigger(t *testing.T) {
	spillDir := t.TempDir()
	m := newCompressTestManager(t, spillDir)
	m.SetCompression(100, 2)
	rt, longOut := seedCompressibleTrace(t, m)
	rt.ObservePrompt(500) // >= 100

	msgs := rt.Messages()
	if len(msgs) != 5 {
		t.Fatalf("消息数 = %d, 期望 5（压缩不改结构）", len(msgs))
	}
	if msgs[0].Content != "帮我修这个 bug" {
		t.Fatal("首条 user 消息应逐字保留")
	}
	stub := msgs[2].Content
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" || msgs[2].Name != "bash" {
		t.Fatalf("压缩后 tool 消息的配对结构被破坏: %+v", msgs[2])
	}
	if !strings.Contains(stub, "上下文压缩") || strings.Contains(stub, strings.Repeat("编译日志行。", 10)) {
		t.Fatalf("旧工具输出应被替换为压缩替身: %q", stub)
	}
	// spill 路径存在且内容完整
	path := strings.TrimSuffix(stub[strings.LastIndex(stub, " ")+1:], "]")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("spill 全文不可读: %v", err)
	}
	if string(raw) != longOut {
		t.Fatal("spill 全文与原文不一致")
	}
	// 最近 2 条逐字保留
	if msgs[3].Content != "看到失败原因了，继续修。" || msgs[4].Content != "修复完成。" {
		t.Fatal("最近消息应逐字保留")
	}
	// 压缩说明 notice 已注入轨迹
	if countNotices(t, rt) != 1 {
		t.Fatal("首次压缩应注入一条说明 notice")
	}
}

// 幂等：重复投影复用缓存，不重复 spill、不重复 notice。
func TestCompressionIdempotent(t *testing.T) {
	spillDir := t.TempDir()
	m := newCompressTestManager(t, spillDir)
	m.SetCompression(100, 2)
	rt, _ := seedCompressibleTrace(t, m)
	rt.ObservePrompt(500)

	first := rt.Messages()[2].Content
	spillCount := func() int {
		files, _ := filepath.Glob(filepath.Join(spillDir, rt.ID(), "*"))
		return len(files)
	}
	before := spillCount()

	second := rt.Messages()[2].Content
	if first != second {
		t.Fatal("重复投影的压缩替身应一致（按 seq 缓存）")
	}
	if spillCount() != before {
		t.Fatalf("重复投影不应产生新 spill 文件: %d → %d", before, spillCount())
	}
	if countNotices(t, rt) != 1 {
		t.Fatal("压缩 notice 只应注入一次")
	}
}

// 禁用（at=0）：任何 token 量级都不压缩。
func TestCompressionDisabled(t *testing.T) {
	m := newCompressTestManager(t, t.TempDir())
	// 不调用 SetCompression
	rt, longOut := seedCompressibleTrace(t, m)
	rt.ObservePrompt(999999)
	if rt.Messages()[2].Content != longOut {
		t.Fatal("未配置压缩策略时不应压缩")
	}
}
