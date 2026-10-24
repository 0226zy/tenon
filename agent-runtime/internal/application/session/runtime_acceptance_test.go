package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/application/agent"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/derr"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
	"github.com/0226zy/proto-platform/agent-runtime/internal/infrastructure/adapter/storejsonl"
)

func acceptanceResponse(msg entity.Message, prompt, completion int) *entity.ChatResponse {
	var resp entity.ChatResponse
	resp.Choices = append(resp.Choices, struct {
		Message      entity.Message `json:"message"`
		FinishReason string         `json:"finish_reason"`
	}{Message: msg, FinishReason: "stop"})
	resp.Usage.PromptTokens = prompt
	resp.Usage.CompletionTokens = completion
	resp.Usage.TotalTokens = prompt + completion
	return &resp
}

type acceptanceLLM struct {
	model  string
	chat   func(context.Context, []entity.Message, []entity.ToolDefinition) (*entity.ChatResponse, error)
	stream func(context.Context, []entity.Message, []entity.ToolDefinition, func(string)) (*entity.ChatResponse, error)
}

func (l *acceptanceLLM) Chat(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition) (*entity.ChatResponse, error) {
	if l.chat == nil {
		return nil, errors.New("unexpected non-streaming chat call")
	}
	return l.chat(ctx, messages, tools)
}

func (l *acceptanceLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	if l.stream != nil {
		return l.stream(ctx, messages, tools, onDelta)
	}
	resp, err := l.Chat(ctx, messages, tools)
	if err == nil && onDelta != nil && len(resp.Choices) > 0 {
		onDelta(resp.Choices[0].Message.Content)
	}
	return resp, err
}

func (l *acceptanceLLM) Model() string { return l.model }

type acceptanceHarness struct {
	manager *Manager
	runs    *RunService
	loop    *agent.Loop
	sid     string
}

func acceptanceLimits() agent.Limits {
	return agent.Limits{MaxIterations: 6, TokenBudget: 1000, LivelockRepeat: 3, MaxRetries: 0, CircuitBreakerFailures: 3}
}

func newAcceptanceHarness(t *testing.T, resolver port.LLMResolver, tools port.ToolRegistryPort, limits agent.Limits) *acceptanceHarness {
	t.Helper()
	if tools == nil {
		tools = emptyTools{}
	}
	dataDir := t.TempDir()
	store, err := storejsonl.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	runStore, err := storejsonl.NewRunStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	loop := agent.NewLoop(resolver, tools, nil, nil, "acceptance system prompt", limits,
		func(error) (bool, time.Duration) { return false, 0 })
	manager := NewManager(store, loop, nil, t.TempDir(), time.Second)
	runs := NewRunService(manager, runStore, nil, RunOptions{StopGrace: time.Millisecond, StopConfirmTimeout: time.Second})
	rt, err := manager.Create(t.TempDir(), "full-auto", "Runtime acceptance", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	return &acceptanceHarness{manager: manager, runs: runs, loop: loop, sid: rt.ID()}
}

func acceptancePrepare(runID, model string) entity.PrepareRun {
	return entity.PrepareRun{RunID: runID, Content: "content for " + runID, Model: model,
		PayloadHash: strings.Repeat("a", 64), Generation: 1}
}

func executeAcceptanceRun(t *testing.T, h *acceptanceHarness, runID, model string) entity.RunState {
	t.Helper()
	req := acceptancePrepare(runID, model)
	if _, _, err := h.runs.Prepare(h.sid, req); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runs.Start(h.sid, runID); err != nil {
		t.Fatal(err)
	}
	return waitForRunState(t, h.runs, h.sid, runID, func(state entity.RunState) bool {
		return state.Status == entity.RunStopped
	})
}

func TestRunModelSwitchAfterTerminalDrivesNextRun(t *testing.T) {
	requested := make(chan string, 4)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var firstStartOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer release()
	var calls atomic.Int32
	resolver := func(name string) (port.LLMPort, error) {
		requested <- name
		return &acceptanceLLM{model: name, chat: func(ctx context.Context, _ []entity.Message, _ []entity.ToolDefinition) (*entity.ChatResponse, error) {
			if calls.Add(1) == 1 {
				firstStartOnce.Do(func() { close(firstStarted) })
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return acceptanceResponse(entity.Message{Role: "assistant", Content: "response:" + name}, 3, 2), nil
		}}, nil
	}
	h := newAcceptanceHarness(t, resolver, nil, acceptanceLimits())
	first := acceptancePrepare("run-model-old", "model-a")
	if _, _, err := h.runs.Prepare(h.sid, first); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runs.Start(h.sid, first.RunID); err != nil {
		t.Fatal(err)
	}
	waitForLLMStart(t, firstStarted)
	if _, err := h.runs.SetModel(h.sid, "model-b"); !errors.Is(err, derr.ErrSessionBusy) {
		t.Fatalf("active Run model switch error = %v, want ErrSessionBusy (HTTP 409)", err)
	}
	release()
	oldState := waitForRunState(t, h.runs, h.sid, first.RunID, func(state entity.RunState) bool {
		return state.Status == entity.RunStopped
	})
	if oldState.Outcome != "done" || oldState.Model != "model-a" {
		t.Fatalf("old Run state = %+v", oldState)
	}
	if name, err := h.runs.SetModel(h.sid, "model-b"); err != nil || name != "model-b" {
		t.Fatalf("terminal model switch = %q, %v", name, err)
	}
	newState := executeAcceptanceRun(t, h, "run-model-new", "model-b")
	if newState.Outcome != "done" || newState.Model != "model-b" {
		t.Fatalf("new Run state = %+v", newState)
	}
	if got := []string{<-requested, <-requested}; got[0] != "model-a" || got[1] != "model-b" {
		t.Fatalf("resolver calls = %v, want [model-a model-b]", got)
	}
	select {
	case extra := <-requested:
		t.Fatalf("unexpected extra resolver call for %q", extra)
	default:
	}
	events, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	var finalText string
	for _, ev := range events {
		if ev.RunID == "run-model-new" && ev.Message != nil && ev.Message.Role == "assistant" {
			finalText = ev.Message.Content
		}
	}
	if finalText != "response:model-b" {
		t.Fatalf("next Run final assistant = %q, want response from model-b", finalText)
	}
}

type acceptanceScriptStep struct {
	message            entity.Message
	err                error
	prompt, completion int
}

type acceptanceScriptLLM struct {
	model string
	mu    sync.Mutex
	steps []acceptanceScriptStep
}

func (l *acceptanceScriptLLM) Chat(context.Context, []entity.Message, []entity.ToolDefinition) (*entity.ChatResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.steps) == 0 {
		return nil, errors.New("unexpected extra scripted chat call")
	}
	step := l.steps[0]
	l.steps = l.steps[1:]
	if step.err != nil {
		return nil, step.err
	}
	return acceptanceResponse(step.message, step.prompt, step.completion), nil
}

func (l *acceptanceScriptLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	resp, err := l.Chat(ctx, messages, tools)
	if err == nil && onDelta != nil && len(resp.Choices) > 0 {
		onDelta(resp.Choices[0].Message.Content)
	}
	return resp, err
}

func (l *acceptanceScriptLLM) Model() string { return l.model }

type acceptanceTool struct {
	name   string
	result entity.ToolResult
}

func (t *acceptanceTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{Type: "function", Function: entity.FunctionSpec{Name: t.name, Parameters: map[string]any{"type": "object"}}}
}
func (t *acceptanceTool) ConcurrencySafe() bool { return true }
func (t *acceptanceTool) Execute(context.Context, string, *port.ToolContext) entity.ToolResult {
	return t.result
}

type acceptanceRegistry struct{ tools []port.Tool }

func (r acceptanceRegistry) Definitions() []entity.ToolDefinition {
	out := make([]entity.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, tool.Definition())
	}
	return out
}
func (r acceptanceRegistry) Lookup(name string) (port.Tool, bool) {
	for _, tool := range r.tools {
		if tool.Definition().Function.Name == name {
			return tool, true
		}
	}
	return nil, false
}
func (r acceptanceRegistry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for _, tool := range r.tools {
		out = append(out, tool.Definition().Function.Name)
	}
	return out
}

func acceptanceToolCall(id, name string) entity.ToolCall {
	return entity.ToolCall{ID: id, Type: "function", Function: entity.FunctionCall{Name: name, Arguments: "{}"}}
}

func assertAcceptanceTerminalMarker(t *testing.T, h *acceptanceHarness, runID, markerType, model, errorCode string, iterations, prompt, completion int) {
	t.Helper()
	events, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	var terminal *entity.Event
	for i := range events {
		ev := &events[i]
		if ev.RunID == runID && ev.Marker != nil && (ev.Marker.Type == "turn_done" || ev.Marker.Type == "turn_failed") {
			terminal = ev
		}
	}
	if terminal == nil || terminal.Marker.Type != markerType {
		t.Fatalf("terminal marker for %s = %+v, want %s", runID, terminal, markerType)
	}
	m := terminal.Marker
	if terminal.RunID != runID || m.RunID != runID || m.Iterations != iterations ||
		m.PromptTokens != prompt || m.CompletionTokens != completion || m.Model != model || m.ErrorCode != errorCode {
		t.Fatalf("terminal fields: event.run_id=%q marker=%+v", terminal.RunID, m)
	}
	raw, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		RunID  string         `json:"run_id"`
		Marker map[string]any `json:"marker"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.RunID != runID {
		t.Fatalf("wire event run_id = %q", wire.RunID)
	}
	for _, key := range []string{"run_id", "iterations", "prompt_tokens", "completion_tokens", "model"} {
		if _, ok := wire.Marker[key]; !ok {
			t.Errorf("terminal marker JSON missing %q: %s", key, raw)
		}
	}
	if errorCode != "" && wire.Marker["error_code"] != errorCode {
		t.Errorf("terminal marker error_code = %v, want %q", wire.Marker["error_code"], errorCode)
	}
}

func TestToolEventsAndTerminalMarkersCarryRuntimeMetadata(t *testing.T) {
	registry := acceptanceRegistry{tools: []port.Tool{
		&acceptanceTool{name: "succeed", result: entity.ToolResult{Content: "ok"}},
		&acceptanceTool{name: "fail", result: entity.ToolResult{Content: "boom", IsError: true}},
	}}
	t.Run("success", func(t *testing.T) {
		llm := &acceptanceScriptLLM{model: "model-a", steps: []acceptanceScriptStep{
			{message: entity.Message{Role: "assistant", ToolCalls: []entity.ToolCall{
				acceptanceToolCall("call-ok", "succeed"), acceptanceToolCall("call-fail", "fail")}}, prompt: 11, completion: 5},
			{message: entity.Message{Role: "assistant", Content: "done"}, prompt: 7, completion: 3},
		}}
		h := newAcceptanceHarness(t, func(string) (port.LLMPort, error) { return llm, nil }, registry, acceptanceLimits())
		state := executeAcceptanceRun(t, h, "run-metadata-success", "model-a")
		if state.Outcome != "done" || state.Iterations != 2 || state.PromptTokens != 18 || state.CompletionTokens != 8 {
			t.Fatalf("success Run state = %+v", state)
		}
		events, err := h.manager.Events(h.sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		var tools []entity.Event
		for _, ev := range events {
			if ev.RunID == "run-metadata-success" && ev.Message != nil && ev.Message.Role == "tool" {
				tools = append(tools, ev)
			}
		}
		if len(tools) != 2 || tools[0].Message.ToolCallID != "call-ok" || tools[1].Message.ToolCallID != "call-fail" ||
			tools[0].Message.IsError || !tools[1].Message.IsError {
			t.Fatalf("tool events / is_error = %+v", tools)
		}
		raw, err := json.Marshal(tools[1])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"is_error":true`) {
			t.Fatalf("failed tool event JSON missing is_error: %s", raw)
		}
		assertAcceptanceTerminalMarker(t, h, "run-metadata-success", "turn_done", "model-a", "", 2, 18, 8)
	})

	t.Run("failure", func(t *testing.T) {
		llm := &acceptanceScriptLLM{model: "model-a", steps: []acceptanceScriptStep{
			{message: entity.Message{Role: "assistant", ToolCalls: []entity.ToolCall{acceptanceToolCall("call-ok", "succeed")}}, prompt: 17, completion: 4},
			{err: errors.New("upstream rejected request")},
		}}
		h := newAcceptanceHarness(t, func(string) (port.LLMPort, error) { return llm, nil }, registry, acceptanceLimits())
		state := executeAcceptanceRun(t, h, "run-metadata-failure", "model-a")
		if state.Outcome != "failed" || state.ErrorCode != "LLM_ERROR" || state.Iterations != 2 || state.PromptTokens != 17 || state.CompletionTokens != 4 {
			t.Fatalf("failure Run state = %+v", state)
		}
		assertAcceptanceTerminalMarker(t, h, "run-metadata-failure", "turn_failed", "model-a", "LLM_ERROR", 2, 17, 4)
	})
}

func TestRunTextDeltasExactlyMatchFinalAssistant(t *testing.T) {
	chunks := []string{"第一段", "\n", "middle", "", "🙂", " 结尾"}
	want := strings.Join(chunks, "")
	llm := &acceptanceLLM{model: "model-a"}
	llm.chat = func(context.Context, []entity.Message, []entity.ToolDefinition) (*entity.ChatResponse, error) {
		return acceptanceResponse(entity.Message{Role: "assistant", Content: want}, 9, 6), nil
	}
	llm.stream = func(_ context.Context, _ []entity.Message, _ []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
		for _, chunk := range chunks {
			onDelta(chunk)
		}
		return acceptanceResponse(entity.Message{Role: "assistant", Content: want}, 9, 6), nil
	}
	h := newAcceptanceHarness(t, func(string) (port.LLMPort, error) { return llm, nil }, nil, acceptanceLimits())
	h.loop.EnableStreaming(true)
	stream, unsubscribe, err := h.manager.Subscribe(h.sid)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	req := acceptancePrepare("run-delta-exact", "model-a")
	if _, _, err := h.runs.Prepare(h.sid, req); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runs.Start(h.sid, req.RunID); err != nil {
		t.Fatal(err)
	}
	var deltas []entity.Event
	terminal := false
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for !terminal {
		select {
		case ev := <-stream:
			if ev.RunID != req.RunID {
				continue
			}
			if ev.Kind == entity.EventDelta {
				deltas = append(deltas, ev)
			}
			terminal = ev.Marker != nil && (ev.Marker.Type == "turn_done" || ev.Marker.Type == "turn_failed")
		case <-timer.C:
			t.Fatal("timed out waiting for streaming Run terminal marker")
		}
	}
	state := waitForRunState(t, h.runs, h.sid, req.RunID, func(state entity.RunState) bool { return state.Status == entity.RunStopped })
	if state.Outcome != "done" {
		t.Fatalf("streaming Run state = %+v", state)
	}
	var joined strings.Builder
	for _, ev := range deltas {
		if ev.Seq != -1 || ev.Text != ev.Delta || ev.RunID != req.RunID {
			t.Errorf("invalid delta metadata: %+v", ev)
		}
		joined.WriteString(ev.Text)
	}
	if len(deltas) != len(chunks)-1 { // The loop deliberately suppresses an empty delta.
		t.Fatalf("delta count = %d, want %d", len(deltas), len(chunks)-1)
	}
	events, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	var assistants []string
	for _, ev := range events {
		if ev.Kind == entity.EventDelta {
			t.Errorf("temporary delta was persisted: %+v", ev)
		}
		if ev.RunID == req.RunID && ev.Message != nil && ev.Message.Role == "assistant" {
			assistants = append(assistants, ev.Message.Content)
		}
	}
	if len(assistants) != 1 || joined.String() != want || assistants[0] != want {
		t.Fatalf("delta/final mismatch: joined=%q assistants=%q want=%q", joined.String(), assistants, want)
	}
}

func TestRunTokenBudgetResetsAcrossRuns(t *testing.T) {
	var calls atomic.Int32
	llm := &acceptanceLLM{model: "model-a", chat: func(context.Context, []entity.Message, []entity.ToolDefinition) (*entity.ChatResponse, error) {
		calls.Add(1)
		return acceptanceResponse(entity.Message{Role: "assistant", Content: "within budget"}, 13, 7), nil
	}}
	limits := acceptanceLimits()
	limits.TokenBudget = 20
	h := newAcceptanceHarness(t, func(string) (port.LLMPort, error) { return llm, nil }, nil, limits)
	for i := 1; i <= 3; i++ {
		state := executeAcceptanceRun(t, h, fmt.Sprintf("run-budget-%d", i), "model-a")
		if state.Outcome != "done" || state.ErrorCode != "" || state.PromptTokens != 13 || state.CompletionTokens != 7 {
			t.Fatalf("Run %d did not receive a fresh budget: %+v", i, state)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("model calls = %d, want 3", got)
	}
}

type acceptanceCountingStore struct {
	port.SessionStorePort
	loads atomic.Int32
}

func (s *acceptanceCountingStore) Load(id string) (entity.SessionHeader, []entity.Event, error) {
	s.loads.Add(1)
	return s.SessionStorePort.Load(id)
}

func TestManagerColdLoad100GoroutinesReturnsSameRuntime(t *testing.T) {
	store, err := storejsonl.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Create(entity.SessionHeader{ID: "cold-session", Cwd: t.TempDir(), Mode: "full-auto", Model: "model-a"}); err != nil {
		t.Fatal(err)
	}
	counting := &acceptanceCountingStore{SessionStorePort: store}
	manager := NewManager(counting, nil, nil, t.TempDir(), time.Second)
	const goroutines = 100
	type loadResult struct {
		rt  *Runtime
		err error
	}
	results := make([]loadResult, goroutines)
	var ready, done sync.WaitGroup
	ready.Add(goroutines)
	done.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			results[i].rt, results[i].err = manager.Get("cold-session")
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	if results[0].err != nil || results[0].rt == nil {
		t.Fatalf("first cold load = %+v", results[0])
	}
	for i, result := range results {
		if result.err != nil || result.rt != results[0].rt {
			t.Fatalf("load %d returned different Runtime: rt=%p err=%v; first=%p", i, result.rt, result.err, results[0].rt)
		}
	}
	if got := counting.loads.Load(); got != 1 {
		t.Fatalf("underlying cold loads = %d, want 1", got)
	}
}

type acceptanceDelayedTool struct {
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	emit    func(entity.Event)
	runID   string
}

func (t *acceptanceDelayedTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{Type: "function", Function: entity.FunctionSpec{Name: "delayed", Parameters: map[string]any{"type": "object"}}}
}
func (t *acceptanceDelayedTool) ConcurrencySafe() bool { return false }
func (t *acceptanceDelayedTool) Execute(ctx context.Context, _ string, tctx *port.ToolContext) entity.ToolResult {
	t.mu.Lock()
	t.emit = tctx.Emit
	t.runID = tctx.RunID
	t.mu.Unlock()
	t.once.Do(func() { close(t.started) })
	<-ctx.Done()
	return entity.ToolResult{IsError: true, Content: ctx.Err().Error()}
}
func (t *acceptanceDelayedTool) delayedEmit() (func(entity.Event), string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emit, t.runID
}

func TestCancelledRunDelayedEventsCannotWriteIntoLaterRuns(t *testing.T) {
	tool := &acceptanceDelayedTool{started: make(chan struct{})}
	newStarted := make(chan struct{})
	releaseNew := make(chan struct{})
	var newStartOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseNew) }) }
	defer release()
	var calls atomic.Int32
	llm := &acceptanceLLM{model: "model-a", chat: func(ctx context.Context, _ []entity.Message, _ []entity.ToolDefinition) (*entity.ChatResponse, error) {
		switch calls.Add(1) {
		case 1:
			return acceptanceResponse(entity.Message{Role: "assistant", ToolCalls: []entity.ToolCall{acceptanceToolCall("old-call", "delayed")}}, 2, 1), nil
		case 2:
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			newStartOnce.Do(func() { close(newStarted) })
			select {
			case <-releaseNew:
				return acceptanceResponse(entity.Message{Role: "assistant", Content: "new done"}, 2, 1), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}}
	h := newAcceptanceHarness(t, func(string) (port.LLMPort, error) { return llm, nil },
		acceptanceRegistry{tools: []port.Tool{tool}}, acceptanceLimits())
	oldReq := acceptancePrepare("run-cancel-old", "model-a")
	if _, _, err := h.runs.Prepare(h.sid, oldReq); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runs.Start(h.sid, oldReq.RunID); err != nil {
		t.Fatal(err)
	}
	waitForLLMStart(t, tool.started)
	oldCancelled := false
	defer func() {
		if !oldCancelled {
			_, _ = h.runs.Cancel(h.sid, oldReq.RunID)
		}
	}()
	if _, err := h.runs.Cancel(h.sid, oldReq.RunID); err != nil {
		t.Fatal(err)
	}
	oldCancelled = true
	oldState := waitForRunState(t, h.runs, h.sid, oldReq.RunID, func(state entity.RunState) bool { return state.Status == entity.RunStopped })
	if oldState.Outcome != "cancelled" || !oldState.StoppedConfirmed {
		t.Fatalf("old Run cancellation = %+v", oldState)
	}
	emit, originatingRun := tool.delayedEmit()
	if emit == nil || originatingRun != oldReq.RunID {
		t.Fatalf("tool context was not bound to old Run: emit=%v run_id=%q", emit != nil, originatingRun)
	}

	newReq := acceptancePrepare("run-after-cancel", "model-a")
	if _, _, err := h.runs.Prepare(h.sid, newReq); err != nil {
		t.Fatal(err)
	}
	if _, err := h.runs.Start(h.sid, newReq.RunID); err != nil {
		t.Fatal(err)
	}
	waitForLLMStart(t, newStarted)
	before, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	emit(entity.Event{Kind: entity.EventNotice, Note: "late-old-during-new"})
	after, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	release()
	newState := waitForRunState(t, h.runs, h.sid, newReq.RunID, func(state entity.RunState) bool { return state.Status == entity.RunStopped })
	if newState.Outcome != "done" {
		t.Fatalf("new Run state = %+v", newState)
	}
	beforeAfterNew, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	emit(entity.Event{Kind: entity.EventNotice, Note: "late-old-after-new"})
	afterNew, err := h.manager.Events(h.sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || len(afterNew) != len(beforeAfterNew) {
		t.Fatalf("delayed old events were appended: during %d -> %d, after %d -> %d", len(before), len(after), len(beforeAfterNew), len(afterNew))
	}
	for _, ev := range afterNew {
		if strings.HasPrefix(ev.Note, "late-old-") {
			t.Fatalf("found delayed old event with run_id=%q: %+v", ev.RunID, ev)
		}
	}
}

func TestSlowConsumerBackfills1000EventsExactlyOnce(t *testing.T) {
	h := newAcceptanceHarness(t, func(string) (port.LLMPort, error) {
		return &acceptanceLLM{model: "model-a"}, nil
	}, nil, acceptanceLimits())
	stream, unsubscribe, err := h.manager.Subscribe(h.sid)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	rt, err := h.manager.Get(h.sid)
	if err != nil {
		t.Fatal(err)
	}
	const eventCount = 1000
	for i := 1; i <= eventCount; i++ {
		if _, err := rt.AppendEvent(entity.Event{Kind: entity.EventNotice, Note: fmt.Sprintf("event-%04d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	var received []entity.Event
	for ev := range stream { // A 256-entry live buffer must close while the consumer is slow.
		received = append(received, ev)
	}
	if len(received) == 0 || len(received) >= eventCount {
		t.Fatalf("slow consumer live count = %d, want a partial prefix", len(received))
	}
	for i, ev := range received {
		if ev.Seq != i+1 {
			t.Fatalf("live event %d has seq %d", i, ev.Seq)
		}
	}
	next := received[len(received)-1].Seq + 1
	for next <= eventCount {
		page, nextPage, err := h.manager.EventsPage(h.sid, next, 113)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			t.Fatalf("empty backfill page at seq %d", next)
		}
		for _, ev := range page {
			if ev.Seq != next {
				t.Fatalf("backfill seq = %d, want %d (gap or duplicate)", ev.Seq, next)
			}
			received = append(received, ev)
			next++
		}
		if nextPage != next {
			t.Fatalf("backfill cursor = %d, want %d", nextPage, next)
		}
	}
	if len(received) != eventCount {
		t.Fatalf("reconstructed event count = %d, want %d", len(received), eventCount)
	}
	for i, ev := range received {
		if ev.Seq != i+1 || ev.Note != fmt.Sprintf("event-%04d", i+1) {
			t.Fatalf("reconstructed event %d = %+v", i, ev)
		}
	}
}
