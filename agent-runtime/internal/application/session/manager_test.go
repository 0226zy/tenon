package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/protoforge/agent-runtime/internal/application/agent"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/derr"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/entity"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/port"
	"github.com/0226zy/protoforge/agent-runtime/internal/infrastructure/adapter/storejsonl"
)

// echoLLM 直接返回固定文本（无工具调用），回合应一轮即正常终止。
type echoLLM struct{}

func (echoLLM) Chat(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition) (*entity.ChatResponse, error) {
	var resp entity.ChatResponse
	resp.Choices = append(resp.Choices, struct {
		Message      entity.Message `json:"message"`
		FinishReason string         `json:"finish_reason"`
	}{Message: entity.Message{Role: "assistant", Content: "好的，已完成。"}, FinishReason: "stop"})
	resp.Usage.TotalTokens = 10
	return &resp, nil
}
func (e echoLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	resp, err := e.Chat(ctx, messages, tools)
	if err == nil && onDelta != nil {
		onDelta(resp.Choices[0].Message.Content)
	}
	return resp, err
}
func (echoLLM) Model() string { return "echo" }

type emptyTools struct{}

func (emptyTools) Definitions() []entity.ToolDefinition { return nil }
func (emptyTools) Lookup(name string) (port.Tool, bool) { return nil, false }
func (emptyTools) Names() []string                      { return nil }

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := storejsonl.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(string) (port.LLMPort, error) { return echoLLM{}, nil }
	loop := agent.NewLoop(resolver, emptyTools{}, nil, nil, "系统提示词",
		agent.Limits{MaxIterations: 5, TokenBudget: 10000, LivelockRepeat: 3, MaxRetries: 1, CircuitBreakerFailures: 3},
		func(error) (bool, time.Duration) { return false, 0 })
	return NewManager(store, loop, nil, t.TempDir(), time.Second)
}

// 等回合结束（轮询 turn_done/turn_failed marker）。
func waitTurnDone(t *testing.T, m *Manager, id string) []entity.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := m.Events(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			if ev.Marker != nil && (ev.Marker.Type == "turn_done" || ev.Marker.Type == "turn_failed") {
				return events
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("回合未在 5 秒内结束")
	return nil
}

// 端到端：创建会话 → 跑一个回合 → 事件日志完整（user → assistant → turn_done）。
func TestManagerRunTurn(t *testing.T) {
	m := newTestManager(t)
	rt, err := m.Create(t.TempDir(), "auto-edit", "测试", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RunTurn(rt.ID(), "你好"); err != nil {
		t.Fatal(err)
	}
	events := waitTurnDone(t, m, rt.ID())

	var hasUser, hasAssistant, hasDone bool
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == "user" {
			hasUser = true
		}
		if ev.Message != nil && ev.Message.Role == "assistant" && ev.Message.Content == "好的，已完成。" {
			hasAssistant = true
		}
		if ev.Marker != nil && ev.Marker.Type == "turn_done" {
			hasDone = true
		}
	}
	if !hasUser || !hasAssistant || !hasDone {
		t.Fatalf("事件日志不完整: user=%v assistant=%v done=%v", hasUser, hasAssistant, hasDone)
	}

	// 忙时重入应被拒
	rt2, _ := m.Get(rt.ID())
	rt2.mu.Lock()
	rt2.running = true
	rt2.mu.Unlock()
	if err := m.RunTurn(rt.ID(), "再来"); !errors.Is(err, derr.ErrSessionBusy) {
		t.Fatalf("err = %v, 期望 ErrSessionBusy", err)
	}
	rt2.mu.Lock()
	rt2.running = false
	rt2.mu.Unlock()
}

// resume：新 Manager 从磁盘日志恢复会话；search 命中消息内容。
func TestResumeAndSearch(t *testing.T) {
	m := newTestManager(t)
	rt, _ := m.Create(t.TempDir(), "auto-edit", "", "")
	if err := m.RunTurn(rt.ID(), "记住关键词pineapple"); err != nil {
		t.Fatal(err)
	}
	waitTurnDone(t, m, rt.ID())

	// 模拟重启：同一 store、新 Manager（内存运行时清空）
	m2 := newTestManager(t)
	// 把 m2 的 store 指向 m 的数据目录做不到（TempDir 不同），改为直接用同一 store：
	m2.store = m.store
	rt2, err := m2.Get(rt.ID())
	if err != nil {
		t.Fatalf("resume 失败: %v", err)
	}
	msgs := rt2.Messages()
	if len(msgs) < 2 {
		t.Fatalf("恢复的消息数 = %d", len(msgs))
	}
	hits, err := m2.Search(rt.ID(), "PINEAPPLE")
	if err != nil || len(hits) == 0 {
		t.Fatalf("search 未命中: %v", err)
	}
}

// 轨迹完整性修复（指引 §4.2）：assistant 有 tool_call 但缺配对 tool 结果时，补合成消息。
func TestMessagesRepair(t *testing.T) {
	m := newTestManager(t)
	rt, _ := m.Create(t.TempDir(), "auto-edit", "", "")
	_, _ = rt.AppendEvent(entity.Event{Kind: entity.EventMessage, Message: &entity.Message{
		Role: "assistant",
		ToolCalls: []entity.ToolCall{
			{ID: "c1", Type: "function", Function: entity.FunctionCall{Name: "bash", Arguments: "{}"}},
			{ID: "c2", Type: "function", Function: entity.FunctionCall{Name: "grep", Arguments: "{}"}},
		},
	}})
	// 只回了 c1 的结果（回合在此中断）
	_, _ = rt.AppendEvent(entity.Event{Kind: entity.EventMessage, Message: &entity.Message{
		Role: "tool", ToolCallID: "c1", Content: "ok",
	}})

	msgs := rt.Messages()
	if len(msgs) != 3 {
		t.Fatalf("修复后消息数 = %d, 期望 3", len(msgs))
	}
	last := msgs[2]
	if last.Role != "tool" || last.ToolCallID != "c2" {
		t.Fatalf("合成补齐消息不正确: %+v", last)
	}
}

// fork 后经管理器拿到子会话。
func TestManagerFork(t *testing.T) {
	m := newTestManager(t)
	rt, _ := m.Create(t.TempDir(), "suggest", "父", "")
	_ = m.RunTurn(rt.ID(), "hi")
	waitTurnDone(t, m, rt.ID())

	child, err := m.Fork(rt.ID(), -1)
	if err != nil {
		t.Fatal(err)
	}
	h := child.Header()
	if h.ParentID != rt.ID() || h.Mode != "suggest" {
		t.Fatalf("fork header = %+v", h)
	}
	// 子会话消息与父一致
	if len(child.Messages()) != len(rt.Messages()) {
		t.Fatalf("fork 后消息数不一致: child=%d parent=%d", len(child.Messages()), len(rt.Messages()))
	}
}

// cwd 白名单 guard（评审 P0-4）：白名单外的工作区在创建期被拒绝，且不落盘目录。
func TestManagerCwdGuard(t *testing.T) {
	m := newTestManager(t)
	root := t.TempDir()
	m.SetCwdGuard(func(cwd string) error {
		if cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			return nil
		}
		return fmt.Errorf("工作区 %s 不在允许范围内", cwd)
	})

	// 白名单内（含子目录）放行
	if _, err := m.Create(filepath.Join(root, "proj-a"), "suggest", "", ""); err != nil {
		t.Fatalf("白名单内创建被拒绝: %v", err)
	}
	// 白名单外拒绝，且不会 MkdirAll 落盘
	outside := filepath.Join(t.TempDir(), "evil")
	if _, err := m.Create(outside, "suggest", "", ""); err == nil {
		t.Fatal("白名单外创建应被拒绝")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的工作区不应落盘: %v", err)
	}
}
