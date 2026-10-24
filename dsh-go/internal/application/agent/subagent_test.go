package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// 子代理委派：主循环发起 task 调用 → 子循环在隔离上下文执行 → 结论回传主上下文。
func TestSubAgentDelegation(t *testing.T) {
	// 脚本顺序：① 主循环第 1 次调用 → 请求 task 工具；
	// ② 子循环第 1 次调用 → 请求 bash 工具；③ 子循环第 2 次 → 返回结论；
	// ④ 主循环第 2 次调用 → 最终回答。
	llm := &fakeLLM{
		scripts: []entity.Message{
			toolCallMsg("task", `{"task":"统计文件数量"}`),
			toolCallMsg("bash", `{"command":"find . -type f | wc -l"}`),
			{Role: "assistant", Content: "共 42 个文件"},
			{Role: "assistant", Content: "主任务完成"},
		},
		errs: []error{nil, nil, nil, nil},
	}
	registry := &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}}}
	limits := Limits{MaxIterations: 10, TokenBudget: 100000, LivelockRepeat: 3, MaxRetries: 1, CircuitBreakerFailures: 3}
	classify := func(error) (bool, time.Duration) { return false, 0 }

	// task 工具注册进主注册表（子代理拿到的是过滤视图）
	resolver := func(string) (port.LLMPort, error) { return llm, nil }
	subTool := NewSubAgentTool(resolver, registry, nil, nil, limits, classify, 30000)
	mainRegistry := &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}, subTool}}

	loop := NewLoop(resolver, mainRegistry, nil, nil, "系统提示词", limits, classify)
	sess := &fakeSession{}

	final, err := loop.RunTurn(context.Background(), sess, "统计一下")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if final.FinalText != "主任务完成" {
		t.Fatalf("final = %q", final.FinalText)
	}

	// ① 结论回传：主轨迹中 task 的工具结果包含子代理结论
	var taskResult string
	var subagentMarkers int
	for _, ev := range sess.events {
		if ev.Message != nil && ev.Message.Role == "tool" && ev.Message.Name == "task" {
			taskResult = ev.Message.Content
		}
		if ev.Marker != nil && (ev.Marker.Type == "subagent_start" || ev.Marker.Type == "subagent_done") {
			subagentMarkers++
		}
	}
	if !strings.Contains(taskResult, "共 42 个文件") {
		t.Fatalf("子代理结论未回传主上下文: %q", taskResult)
	}
	if !strings.Contains(taskResult, "完整轨迹") {
		t.Fatalf("工具结果应含轨迹路径: %q", taskResult)
	}
	if subagentMarkers != 2 {
		t.Fatalf("子代理生命周期 marker = %d, 期望 2", subagentMarkers)
	}

	// ② 隔离性：主轨迹中不出现子代理的中间消息（子代理的 user 任务消息 / bash 结果）
	for _, ev := range sess.events {
		if ev.Message == nil {
			continue
		}
		m := ev.Message
		if m.Role == "user" && m.Content == "统计文件数量" {
			t.Fatal("子代理的任务消息泄漏进主轨迹")
		}
		if m.Role == "tool" && m.Name == "bash" && strings.Contains(m.Content, "tool-output") {
			t.Fatal("子代理的中间工具结果泄漏进主轨迹")
		}
	}
}

// 防递归连锁：子代理上下文中 task 工具被拒。
func TestSubAgentNoNesting(t *testing.T) {
	llm := &fakeLLM{}
	resolver := func(string) (port.LLMPort, error) { return llm, nil }
	subTool := NewSubAgentTool(resolver, &fakeRegistry{}, nil, nil,
		Limits{MaxIterations: 5, TokenBudget: 1000, LivelockRepeat: 3, MaxRetries: 1, CircuitBreakerFailures: 3},
		func(error) (bool, time.Duration) { return false, 0 }, 30000)
	tctx := &port.ToolContext{
		SessionID: "s",
		Cwd:       func() string { return "/tmp" },
		Spill:     func(t, c string) string { return "/tmp/x.log" },
	}
	res := subTool.Execute(context.WithValue(context.Background(), depthKey{}, 1), `{"task":"再委派"}`, tctx)
	if !res.IsError || !strings.Contains(res.Content, "不允许再次委派") {
		t.Fatalf("嵌套委派应被拒: %+v", res)
	}
}

// 子代理看不到 task 工具（过滤视图双重防护）。
func TestFilteredRegistry(t *testing.T) {
	base := &fakeRegistry{tools: []port.Tool{&fakeTool{name: "bash"}, &fakeTool{name: "task"}}}
	filtered := excludeTool(base, "task")
	for _, d := range filtered.Definitions() {
		if d.Function.Name == "task" {
			t.Fatal("过滤视图仍含 task 工具定义")
		}
	}
	if _, ok := filtered.Lookup("task"); ok {
		t.Fatal("过滤视图仍可 Lookup 到 task")
	}
	if _, ok := filtered.Lookup("bash"); !ok {
		t.Fatal("过滤视图误伤 bash")
	}
	if len(filtered.Names()) != 1 || filtered.Names()[0] != "bash" {
		t.Fatalf("Names: %v", filtered.Names())
	}
}

// 子代理轨迹可序列化审计。
func TestSubSessionDump(t *testing.T) {
	s := &subSession{id: "s__sub1", cwd: func() string { return "/tmp" }}
	_, _ = s.AppendEvent(entity.Event{Kind: entity.EventMessage, Message: &entity.Message{Role: "user", Content: "任务"}})
	_, _ = s.AppendEvent(entity.Event{Kind: entity.EventMessage, Message: &entity.Message{Role: "assistant", Content: "结论"}})
	dump := s.dumpJSONL()
	if !strings.Contains(dump, "subagent_header") || !strings.Contains(dump, `"结论"`) {
		t.Fatalf("轨迹序列化不完整: %q", dump)
	}
	msgs := s.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages = %d", len(msgs))
	}
}
