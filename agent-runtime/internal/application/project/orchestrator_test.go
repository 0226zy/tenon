package project

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0226zy/protoforge/agent-runtime/internal/application/agent"
	"github.com/0226zy/protoforge/agent-runtime/internal/application/session"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/entity"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/port"
	"github.com/0226zy/protoforge/agent-runtime/internal/infrastructure/adapter/storejsonl"
)

// recordingLLM 记录每次调用的消息序列，返回固定回答（无工具调用 → 回合一轮结束）。
type recordingLLM struct {
	mu    sync.Mutex
	calls [][]entity.Message
}

func (l *recordingLLM) Chat(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition) (*entity.ChatResponse, error) {
	l.mu.Lock()
	l.calls = append(l.calls, append([]entity.Message(nil), messages...))
	l.mu.Unlock()
	var resp entity.ChatResponse
	resp.Choices = append(resp.Choices, struct {
		Message      entity.Message `json:"message"`
		FinishReason string         `json:"finish_reason"`
	}{Message: entity.Message{Role: "assistant", Content: "已完成。"}, FinishReason: "stop"})
	resp.Usage.TotalTokens = 10
	return &resp, nil
}
func (l *recordingLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	resp, err := l.Chat(ctx, messages, tools)
	if err == nil && onDelta != nil {
		onDelta(resp.Choices[0].Message.Content)
	}
	return resp, err
}
func (l *recordingLLM) Model() string { return "rec" }

func (l *recordingLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

// failingLLM 总是失败（不可重试分类 → 回合失败）。
type failingLLM struct{}

func (failingLLM) Chat(context.Context, []entity.Message, []entity.ToolDefinition) (*entity.ChatResponse, error) {
	return nil, errors.New("llm boom")
}
func (f failingLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, _ func(string)) (*entity.ChatResponse, error) {
	return f.Chat(ctx, messages, tools)
}
func (failingLLM) Model() string { return "fail" }

type emptyTools struct{}

func (emptyTools) Definitions() []entity.ToolDefinition { return nil }
func (emptyTools) Lookup(name string) (port.Tool, bool) { return nil, false }
func (emptyTools) Names() []string                      { return nil }

func newTestManager(t *testing.T, llm port.LLMPort) *session.Manager {
	t.Helper()
	store, err := storejsonl.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(string) (port.LLMPort, error) { return llm, nil }
	loop := agent.NewLoop(resolver, emptyTools{}, nil, nil, "系统提示词",
		agent.Limits{MaxIterations: 5, TokenBudget: 10000, LivelockRepeat: 3, MaxRetries: 1, CircuitBreakerFailures: 3},
		func(error) (bool, time.Duration) { return false, 0 })
	return session.NewManager(store, loop, nil, t.TempDir(), time.Second)
}

func newTestOrchestrator(t *testing.T, llm port.LLMPort) (*Orchestrator, *session.Manager) {
	t.Helper()
	m := newTestManager(t, llm)
	o := NewOrchestrator(m, 10*time.Second)
	o.pollInterval = 10 * time.Millisecond // 测试提速
	return o, m
}

// 等编排到达终态（done / planned / *_failed）。
func waitPhase(t *testing.T, o *Orchestrator, planID string) *Plan {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := o.Get(planID)
		if !ok {
			t.Fatal("编排丢失")
		}
		switch p.Phase {
		case PhasePlanned, PhaseDone, PhasePlanFailed, PhaseCodeFailed:
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待编排终态超时")
	return nil
}

// 找会话中第一条 content 含 sub 的 user 消息。
func hasUserMessage(t *testing.T, m *session.Manager, id, sub string) bool {
	t.Helper()
	events, err := m.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Message != nil && ev.Message.Role == "user" && strings.Contains(ev.Message.Content, sub) {
			return true
		}
	}
	return false
}

// 全链路：规划回合 → fork 编码会话（换模型）→ 编码回合 → done。
func TestPlanAndAutoImplement(t *testing.T) {
	llm := &recordingLLM{}
	o, m := newTestOrchestrator(t, llm)

	plan, err := o.Start(Request{
		Cwd:           t.TempDir(),
		Requirement:   "做一个 todo CLI",
		AutoImplement: true,
		CoderModel:    "kimi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Phase != PhasePlanning {
		t.Fatalf("初始阶段: %q", plan.Phase)
	}

	final := waitPhase(t, o, plan.PlanID)
	if final.Phase != PhaseDone {
		t.Fatalf("终态 = %q（err: %s）", final.Phase, final.Err)
	}
	if final.CodeSessionID == "" {
		t.Fatal("缺少编码会话 ID")
	}

	// 血缘：编码会话 fork 自规划会话
	header, err := m.HeaderOf(final.CodeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if header.ParentID != plan.PlanID {
		t.Fatalf("编码会话血缘 = %q，期望 %q", header.ParentID, plan.PlanID)
	}
	if header.InheritedEventCount == 0 {
		t.Fatal("编码会话应继承规划会话的事件前缀")
	}
	// 多模型分工：编码会话已切换模型
	if header.Model != "kimi" {
		t.Fatalf("编码会话模型 = %q，期望 kimi", header.Model)
	}

	// 角色提示词：规划会话首条消息含规划师角色与需求；编码会话含实施纪律
	if !hasUserMessage(t, m, plan.PlanID, "规划师") || !hasUserMessage(t, m, plan.PlanID, "做一个 todo CLI") {
		t.Fatal("规划会话缺少角色提示词或需求")
	}
	if !hasUserMessage(t, m, final.CodeSessionID, "请严格按以上规划实施") {
		t.Fatal("编码会话缺少实施提示词")
	}
	if llm.callCount() < 2 {
		t.Fatalf("至少两次模型调用（规划+编码），实际 %d", llm.callCount())
	}
}

// 只规划不实施：终态 planned，无编码会话。
func TestPlanOnly(t *testing.T) {
	o, _ := newTestOrchestrator(t, &recordingLLM{})
	plan, err := o.Start(Request{Cwd: t.TempDir(), Requirement: "评估重构方案"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitPhase(t, o, plan.PlanID)
	if final.Phase != PhasePlanned {
		t.Fatalf("终态 = %q，期望 planned（err: %s）", final.Phase, final.Err)
	}
	if final.CodeSessionID != "" {
		t.Fatalf("未请求自动实施不应有编码会话: %q", final.CodeSessionID)
	}
}

// 规划回合失败：终态 plan_failed，错误透出。
func TestPlanFailure(t *testing.T) {
	o, _ := newTestOrchestrator(t, failingLLM{})
	plan, err := o.Start(Request{Cwd: t.TempDir(), Requirement: "x"})
	if err != nil {
		t.Fatal(err)
	}
	final := waitPhase(t, o, plan.PlanID)
	if final.Phase != PhasePlanFailed {
		t.Fatalf("终态 = %q，期望 plan_failed", final.Phase)
	}
	if !strings.Contains(final.Err, "llm boom") {
		t.Fatalf("错误应透出底层原因: %q", final.Err)
	}
}

// 空需求：启动即拒绝。
func TestEmptyRequirement(t *testing.T) {
	o, _ := newTestOrchestrator(t, &recordingLLM{})
	if _, err := o.Start(Request{Cwd: t.TempDir(), Requirement: "  "}); err == nil {
		t.Fatal("空需求应报错")
	}
}

// Get 的防御性拷贝：调用方改返回值不影响内部状态。
func TestGetReturnsCopy(t *testing.T) {
	o, _ := newTestOrchestrator(t, &recordingLLM{})
	plan, err := o.Start(Request{Cwd: t.TempDir(), Requirement: "x"})
	if err != nil {
		t.Fatal(err)
	}
	p1, _ := o.Get(plan.PlanID)
	p1.Phase = "corrupted"
	p2, _ := o.Get(plan.PlanID)
	if p2.Phase == "corrupted" {
		t.Fatal("Get 应返回副本")
	}
	waitPhase(t, o, plan.PlanID)
}
