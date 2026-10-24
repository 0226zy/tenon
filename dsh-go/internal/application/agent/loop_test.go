package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0226zy/magents/internal/domain/derr"
	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// ── 测试桩 ───────────────────────────────────────────────────────

// fakeLLM 按脚本逐次返回响应；scripts 用完后返回纯文本（无工具调用）。
type fakeLLM struct {
	mu      sync.Mutex
	scripts []entity.Message
	errs    []error // 与 scripts 对齐：非 nil 表示本次调用失败
	seen    [][]entity.Message
}

func (f *fakeLLM) Chat(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition) (*entity.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, messages)
	var resp entity.ChatResponse
	if len(f.scripts) > 0 {
		msg := f.scripts[0]
		err := f.errs[0]
		f.scripts = f.scripts[1:]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
		resp.Choices = append(resp.Choices, struct {
			Message      entity.Message `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{Message: msg, FinishReason: "stop"})
	} else {
		resp.Choices = append(resp.Choices, struct {
			Message      entity.Message `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{Message: entity.Message{Role: "assistant", Content: "完成"}, FinishReason: "stop"})
	}
	resp.Usage.TotalTokens = 100
	return &resp, nil
}

func (f *fakeLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	resp, err := f.Chat(ctx, messages, tools)
	if err == nil && onDelta != nil && len(resp.Choices) > 0 && resp.Choices[0].Message.Content != "" {
		onDelta(resp.Choices[0].Message.Content)
	}
	return resp, err
}

func (f *fakeLLM) Model() string { return "fake" }

// fakeTool 记录调用并返回固定结果。
type fakeTool struct {
	name string
	safe bool
}

func (t *fakeTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{Type: "function", Function: entity.FunctionSpec{
		Name: t.name, Description: "测试工具", Parameters: map[string]any{"type": "object"}}}
}
func (t *fakeTool) ConcurrencySafe() bool { return t.safe }
func (t *fakeTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	return entity.ToolResult{Content: "tool-output:" + t.name}
}

type fakeRegistry struct{ tools []port.Tool }

func (r *fakeRegistry) Definitions() []entity.ToolDefinition {
	var defs []entity.ToolDefinition
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}
func (r *fakeRegistry) Lookup(name string) (port.Tool, bool) {
	for _, t := range r.tools {
		if t.Definition().Function.Name == name {
			return t, true
		}
	}
	return nil, false
}
func (r *fakeRegistry) Names() []string {
	var names []string
	for _, t := range r.tools {
		names = append(names, t.Definition().Function.Name)
	}
	return names
}

// fakeSession 是内存版 port.SessionRuntime。
type fakeSession struct {
	mu     sync.Mutex
	events []entity.Event
	temp   []entity.Event
	tokens int
	prompt int
}

func (s *fakeSession) ID() string    { return "test" }
func (s *fakeSession) RunID() string { return "run-test" }
func (s *fakeSession) Cwd() string   { return "/tmp" }
func (s *fakeSession) Model() string { return "fake-model" }
func (s *fakeSession) AppendEvent(ev entity.Event) (entity.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev.Seq = len(s.events)
	s.events = append(s.events, ev)
	return ev, nil
}
func (s *fakeSession) EmitTemp(ev entity.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.temp = append(s.temp, ev)
}
func (s *fakeSession) ObservePrompt(prompt int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompt = prompt
}
func (s *fakeSession) Messages() []entity.Message {
	s.mu.Lock()
	events := append([]entity.Event(nil), s.events...)
	s.mu.Unlock()
	var msgs []entity.Message
	for _, ev := range events {
		if ev.Message != nil {
			msgs = append(msgs, *ev.Message)
		}
	}
	return msgs
}
func (s *fakeSession) AddTokens(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens += n
	return s.tokens
}
func (s *fakeSession) Approve(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision {
	return entity.ApprovalDecision{Approved: true, By: "test"}
}
func (s *fakeSession) Spill(toolName, content string) string { return "/tmp/spill.log" }

// ── 辅助 ─────────────────────────────────────────────────────────

func toolCallMsg(name, args string) entity.Message {
	return entity.Message{Role: "assistant", ToolCalls: []entity.ToolCall{{
		ID: "call_1", Type: "function",
		Function: entity.FunctionCall{Name: name, Arguments: args},
	}}}
}

func retryableClassifier(err error) (bool, time.Duration) {
	if errors.Is(err, errFakeAPI) {
		return true, 0
	}
	return false, 0
}

var errFakeAPI = errors.New("fake 429")

func newLoop(llm port.LLMPort, tools port.ToolRegistryPort, limits Limits) *Loop {
	if limits.MaxIterations == 0 {
		limits.MaxIterations = 10
	}
	if limits.TokenBudget == 0 {
		limits.TokenBudget = 100000
	}
	if limits.LivelockRepeat == 0 {
		limits.LivelockRepeat = 3
	}
	if limits.MaxRetries == 0 {
		limits.MaxRetries = 3
	}
	if limits.CircuitBreakerFailures == 0 {
		limits.CircuitBreakerFailures = 5
	}
	resolver := func(string) (port.LLMPort, error) { return llm, nil }
	return NewLoop(resolver, tools, nil, nil, "系统提示词", limits, retryableClassifier)
}

// ── 测试 ─────────────────────────────────────────────────────────

// FR-1/FR-4：ReAct 循环——模型先发起工具调用，结果回注后模型给出最终回答。
func TestRunTurnReActLoop(t *testing.T) {
	llm := &fakeLLM{
		scripts: []entity.Message{toolCallMsg("bash", `{"command":"ls"}`)},
		errs:    []error{nil},
	}
	loop := newLoop(llm, &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}}}, Limits{})
	sess := &fakeSession{}

	final, err := loop.RunTurn(context.Background(), sess, "看一下目录")
	if err != nil {
		t.Fatalf("RunTurn 失败: %v", err)
	}
	if final.FinalText != "完成" {
		t.Fatalf("最终回答 = %q, 期望 \"完成\"", final.FinalText)
	}
	// 第二次模型调用必须看到工具结果（FR-4 工具结果回注）
	if len(llm.seen) != 2 {
		t.Fatalf("模型被调用 %d 次, 期望 2 次", len(llm.seen))
	}
	second := llm.seen[1]
	last := second[len(second)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" {
		t.Fatalf("第二次调用的最后一条消息应为 tool 结果，实际: %+v", last)
	}
	if !strings.Contains(last.Content, "tool-output:bash") {
		t.Fatalf("工具结果未回注: %q", last.Content)
	}
	// 轨迹：user → assistant(tool_call) → tool → assistant(final)
	var roles []string
	for _, ev := range sess.events {
		if ev.Message != nil {
			roles = append(roles, ev.Message.Role)
		}
	}
	want := []string{"user", "assistant", "tool", "assistant"}
	if fmt.Sprint(roles) != fmt.Sprint(want) {
		t.Fatalf("轨迹角色序列 = %v, 期望 %v", roles, want)
	}
}

// 幻觉调用：工具不存在时以普通工具错误回注，模型下一轮自行纠正。
func TestUnknownToolBecomesModelInput(t *testing.T) {
	llm := &fakeLLM{
		scripts: []entity.Message{toolCallMsg("nonexistent", `{}`)},
		errs:    []error{nil},
	}
	loop := newLoop(llm, &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}}}, Limits{})
	sess := &fakeSession{}

	if _, err := loop.RunTurn(context.Background(), sess, "试试"); err != nil {
		t.Fatalf("RunTurn 失败: %v", err)
	}
	var found bool
	for _, ev := range sess.events {
		if ev.Message != nil && ev.Message.Role == "tool" && strings.Contains(ev.Message.Content, "不存在") {
			found = true
		}
	}
	if !found {
		t.Fatal("幻觉调用未以工具错误结果回注")
	}
}

// §4.5 活性检测：相同指纹反复出现 → 警告注入 → 强制打断。
func TestLivelockDetection(t *testing.T) {
	var scripts []entity.Message
	var errs []error
	for i := 0; i < 10; i++ {
		scripts = append(scripts, toolCallMsg("bash", `{"command":"ls"}`))
		errs = append(errs, nil)
	}
	llm := &fakeLLM{scripts: scripts, errs: errs}
	loop := newLoop(llm, &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}}}, Limits{LivelockRepeat: 3})
	sess := &fakeSession{}

	_, err := loop.RunTurn(context.Background(), sess, "循环吧")
	if !errors.Is(err, derr.ErrLivelock) {
		t.Fatalf("err = %v, 期望 ErrLivelock", err)
	}
	var warned bool
	for _, ev := range sess.events {
		if ev.Kind == entity.EventNotice && strings.Contains(ev.Note, "无进展循环") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("触阈时未注入 Harness 警告")
	}
}

// §4.5 全局终止：最大迭代轮数。
func TestMaxIterations(t *testing.T) {
	llm := &fakeLLM{} // 脚本为空时返回纯文本……这里用永无止境的不同工具调用
	// 构造每次参数不同的调用，绕开活性检测
	var scripts []entity.Message
	var errs []error
	for i := 0; i < 20; i++ {
		scripts = append(scripts, toolCallMsg("bash", fmt.Sprintf(`{"command":"echo %d"}`, i)))
		errs = append(errs, nil)
	}
	llm.scripts, llm.errs = scripts, errs
	loop := newLoop(llm, &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}}}, Limits{MaxIterations: 5})
	sess := &fakeSession{}

	_, err := loop.RunTurn(context.Background(), sess, "跑")
	if !errors.Is(err, derr.ErrMaxIterations) {
		t.Fatalf("err = %v, 期望 ErrMaxIterations", err)
	}
}

// §4.3 静默重试：可重试错误指数退避后成功；不可重试错误直接失败。
func TestRetryThenSuccess(t *testing.T) {
	llm := &fakeLLM{
		scripts: []entity.Message{{}, {}},
		errs:    []error{errFakeAPI, errFakeAPI},
	}
	loop := newLoop(llm, &fakeRegistry{}, Limits{MaxRetries: 3})
	// 缩小退避基数以加速测试：直接调 chatWithRecovery 不可行（未导出），
	// 这里用真实 RunTurn，前两次失败后第三次走默认脚本外响应（纯文本 → 正常终止）。
	sess := &fakeSession{}
	start := time.Now()
	if _, err := loop.RunTurn(context.Background(), sess, "hi"); err != nil {
		t.Fatalf("RunTurn 失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("静默重试应有指数退避（>=2s），实际 %s", elapsed)
	}
}

func TestNonRetryableFailsFast(t *testing.T) {
	errNo := errors.New("401 unauthorized")
	llm := &fakeLLM{scripts: []entity.Message{{}}, errs: []error{errNo}}
	loop := newLoop(llm, &fakeRegistry{}, Limits{})
	sess := &fakeSession{}
	_, err := loop.RunTurn(context.Background(), sess, "hi")
	if !errors.Is(err, derr.ErrNotRetryable) {
		t.Fatalf("err = %v, 期望 ErrNotRetryable", err)
	}
}

// §2.4 并行工具调用：全部并发安全时并行执行且结果按序回注。
func TestParallelToolCalls(t *testing.T) {
	msg := entity.Message{Role: "assistant", ToolCalls: []entity.ToolCall{
		{ID: "c1", Type: "function", Function: entity.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
		{ID: "c2", Type: "function", Function: entity.FunctionCall{Name: "read_file", Arguments: `{"path":"b"}`}},
	}}
	llm := &fakeLLM{scripts: []entity.Message{msg}, errs: []error{nil}}
	loop := newLoop(llm, &fakeRegistry{tools: []port.Tool{&fakeTool{name: "read_file", safe: true}}}, Limits{})
	sess := &fakeSession{}

	if _, err := loop.RunTurn(context.Background(), sess, "读两个文件"); err != nil {
		t.Fatalf("RunTurn 失败: %v", err)
	}
	var toolMsgs []entity.Message
	for _, ev := range sess.events {
		if ev.Message != nil && ev.Message.Role == "tool" {
			toolMsgs = append(toolMsgs, *ev.Message)
		}
	}
	if len(toolMsgs) != 2 || toolMsgs[0].ToolCallID != "c1" || toolMsgs[1].ToolCallID != "c2" {
		t.Fatalf("工具结果未按序回注: %+v", toolMsgs)
	}
}

// 预算终止：token 累计超预算即保护性终止。
func TestTokenBudget(t *testing.T) {
	var scripts []entity.Message
	var errs []error
	for i := 0; i < 5; i++ {
		scripts = append(scripts, toolCallMsg("bash", fmt.Sprintf(`{"command":"echo %d"}`, i)))
		errs = append(errs, nil)
	}
	llm := &fakeLLM{scripts: scripts, errs: errs}
	loop := newLoop(llm, &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}}}, Limits{TokenBudget: 150})
	sess := &fakeSession{}
	_, err := loop.RunTurn(context.Background(), sess, "跑")
	if !errors.Is(err, derr.ErrBudgetExhausted) {
		t.Fatalf("err = %v, 期望 ErrBudgetExhausted", err)
	}
}
