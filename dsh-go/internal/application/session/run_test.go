package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/magents/internal/application/agent"
	"github.com/0226zy/magents/internal/domain/derr"
	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
	"github.com/0226zy/magents/internal/infrastructure/adapter/storejsonl"
)

const runTestModel = "run-test-model"

// controlledRunLLM 可让回合阻塞到 release 或 ctx 取消，从而确定地覆盖 running/cancelling。
type controlledRunLLM struct {
	calls     atomic.Int32
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (l *controlledRunLLM) Chat(ctx context.Context, _ []entity.Message, _ []entity.ToolDefinition) (*entity.ChatResponse, error) {
	l.calls.Add(1)
	if l.started != nil {
		l.startOnce.Do(func() { close(l.started) })
	}
	if l.release != nil {
		select {
		case <-l.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var resp entity.ChatResponse
	resp.Choices = append(resp.Choices, struct {
		Message      entity.Message `json:"message"`
		FinishReason string         `json:"finish_reason"`
	}{Message: entity.Message{Role: "assistant", Content: "run done"}, FinishReason: "stop"})
	resp.Usage.PromptTokens = 13
	resp.Usage.CompletionTokens = 7
	resp.Usage.TotalTokens = 20
	return &resp, nil
}

func (l *controlledRunLLM) ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(string)) (*entity.ChatResponse, error) {
	resp, err := l.Chat(ctx, messages, tools)
	if err == nil && onDelta != nil && len(resp.Choices) > 0 && resp.Choices[0].Message.Content != "" {
		onDelta(resp.Choices[0].Message.Content)
	}
	return resp, err
}

func (l *controlledRunLLM) Model() string { return runTestModel }

type fakeRunExec struct {
	mu         sync.Mutex
	remaining  map[string]int
	kills      map[string]int
	registered map[string][]int
}

func newFakeRunExec() *fakeRunExec {
	return &fakeRunExec{
		remaining:  map[string]int{},
		kills:      map[string]int{},
		registered: map[string][]int{},
	}
}

func (f *fakeRunExec) Register(runID string, pgid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered[runID] = append(f.registered[runID], pgid)
}

func (f *fakeRunExec) Kill(_ context.Context, runID string, _ time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills[runID]++
	return f.remaining[runID], nil
}

func (f *fakeRunExec) Alive(runID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remaining[runID]
}

func (f *fakeRunExec) setRemaining(runID string, remaining int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.remaining[runID] = remaining
}

func (f *fakeRunExec) killCount(runID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.kills[runID]
}

func (f *fakeRunExec) pgids(runID string) []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.registered[runID]...)
}

type runServiceFixture struct {
	svc     *RunService
	manager *Manager
	store   *storejsonl.FileRunStore
	exec    *fakeRunExec
	sid     string
}

func newRunServiceFixture(t *testing.T, llm port.LLMPort) runServiceFixture {
	t.Helper()
	dataDir := t.TempDir()
	sessionStore, err := storejsonl.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	runStore, err := storejsonl.NewRunStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	resolver := func(string) (port.LLMPort, error) { return llm, nil }
	loop := agent.NewLoop(resolver, emptyTools{}, nil, nil, "系统提示词",
		agent.Limits{MaxIterations: 5, TokenBudget: 10000, LivelockRepeat: 3, MaxRetries: 1, CircuitBreakerFailures: 3},
		func(error) (bool, time.Duration) { return false, 0 })
	manager := NewManager(sessionStore, loop, nil, t.TempDir(), time.Second)
	rt, err := manager.Create(t.TempDir(), "full-auto", "Run 测试", runTestModel)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeRunExec()
	svc := NewRunService(manager, runStore, exec, RunOptions{
		StopGrace:          time.Millisecond,
		StopConfirmTimeout: time.Second,
	})
	return runServiceFixture{svc: svc, manager: manager, store: runStore, exec: exec, sid: rt.ID()}
}

func runPrepareRequest(runID string, generation int64) entity.PrepareRun {
	return entity.PrepareRun{
		RunID: runID, Content: "execute " + runID, Model: runTestModel,
		PayloadHash: strings.Repeat("a", 64), Generation: generation,
	}
}

func waitForRunState(t *testing.T, svc *RunService, sid, runID string, want func(entity.RunState) bool) entity.RunState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last entity.RunState
	for time.Now().Before(deadline) {
		state, err := svc.Get(sid, runID)
		if err != nil {
			t.Fatal(err)
		}
		last = state
		if want(state) {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 Run 状态超时，最后状态: %+v", last)
	return entity.RunState{}
}

func waitForLLMStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("模型调用未启动")
	}
}

func TestRunPrepareAndStartAreIdempotent(t *testing.T) {
	llm := &controlledRunLLM{started: make(chan struct{}), release: make(chan struct{})}
	fx := newRunServiceFixture(t, llm)
	req := runPrepareRequest("run-idempotent", 1)

	prepared, created, err := fx.svc.Prepare(fx.sid, req)
	if err != nil || !created || prepared.Status != entity.RunPrepared {
		t.Fatalf("首次 Prepare = %+v, created=%v, err=%v", prepared, created, err)
	}
	duplicate, created, err := fx.svc.Prepare(fx.sid, req)
	if err != nil || created || !reflect.DeepEqual(duplicate, prepared) {
		t.Fatalf("重复 Prepare 不幂等: state=%+v, created=%v, err=%v", duplicate, created, err)
	}
	conflict := req
	conflict.PayloadHash = strings.Repeat("b", 64)
	if _, _, err := fx.svc.Prepare(fx.sid, conflict); !errors.Is(err, derr.ErrRunIDConflict) {
		t.Fatalf("同 run_id 不同 hash: err=%v，期望 ErrRunIDConflict", err)
	}

	state, err := fx.svc.Start(fx.sid, req.RunID)
	if err != nil || state.Status != entity.RunRunning {
		t.Fatalf("首次 Start = %+v, err=%v", state, err)
	}
	waitForLLMStart(t, llm.started)
	state, err = fx.svc.Start(fx.sid, req.RunID)
	if err != nil || state.Status != entity.RunRunning {
		t.Fatalf("重复 Start = %+v, err=%v", state, err)
	}
	close(llm.release)
	final := waitForRunState(t, fx.svc, fx.sid, req.RunID, func(state entity.RunState) bool {
		return state.Status == entity.RunStopped
	})
	if !final.StoppedConfirmed || final.Outcome != "done" {
		t.Fatalf("正常回合停止状态不正确: %+v", final)
	}
	if final.Iterations != 1 || final.PromptTokens != 13 || final.CompletionTokens != 7 {
		t.Fatalf("回合统计未写入 Run: %+v", final)
	}
	if got := llm.calls.Load(); got != 1 {
		t.Fatalf("模型执行次数 = %d，期望 1", got)
	}
	if got := fx.exec.killCount(req.RunID); got != 1 {
		t.Fatalf("执行树清理次数 = %d，期望 1", got)
	}
	if _, err := fx.svc.Start(fx.sid, req.RunID); err != nil {
		t.Fatal(err)
	}
	if got := llm.calls.Load(); got != 1 {
		t.Fatalf("终态重复 Start 后模型执行次数 = %d，期望 1", got)
	}
}

func TestRunPrepareChecksGenerationAndBusy(t *testing.T) {
	fx := newRunServiceFixture(t, &controlledRunLLM{})
	first := runPrepareRequest("run-generation-base", 2)
	if _, _, err := fx.svc.Prepare(fx.sid, first); err != nil {
		t.Fatal(err)
	}
	stale := runPrepareRequest("run-generation-stale", 1)
	stale.PayloadHash = strings.Repeat("b", 64)
	if _, _, err := fx.svc.Prepare(fx.sid, stale); !errors.Is(err, derr.ErrStaleGeneration) {
		t.Fatalf("旧 generation: err=%v，期望 ErrStaleGeneration", err)
	}
	busy := runPrepareRequest("run-generation-busy", 2)
	busy.PayloadHash = strings.Repeat("b", 64)
	if _, _, err := fx.svc.Prepare(fx.sid, busy); !errors.Is(err, derr.ErrSessionBusy) {
		t.Fatalf("会话已有活动 Run: err=%v，期望 ErrSessionBusy", err)
	}
	if _, err := fx.svc.Cancel(fx.sid, first.RunID); err != nil {
		t.Fatal(err)
	}
	if _, created, err := fx.svc.Prepare(fx.sid, busy); err != nil || !created {
		t.Fatalf("活动 Run 停止后仍无法 Prepare: created=%v, err=%v", created, err)
	}
}

func TestRunCancelPreparedDoesNotExecute(t *testing.T) {
	llm := &controlledRunLLM{}
	fx := newRunServiceFixture(t, llm)
	req := runPrepareRequest("run-prepared-cancel", 1)
	if _, _, err := fx.svc.Prepare(fx.sid, req); err != nil {
		t.Fatal(err)
	}
	state, err := fx.svc.Cancel(fx.sid, req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != entity.RunStopped || state.Outcome != "cancelled" || !state.StoppedConfirmed || state.ErrorCode != "CANCELLED" {
		t.Fatalf("prepared 取消状态不正确: %+v", state)
	}
	if got := fx.exec.killCount(req.RunID); got != 0 {
		t.Fatalf("prepared 取消不应清理执行树，Kill 次数 = %d", got)
	}
	state, err = fx.svc.Start(fx.sid, req.RunID)
	if err != nil || state.Status != entity.RunStopped {
		t.Fatalf("取消后的 Start = %+v, err=%v", state, err)
	}
	if got := llm.calls.Load(); got != 0 {
		t.Fatalf("prepared 取消后仍执行模型 %d 次", got)
	}
}

func TestRunCancelRunningConfirmsOnlyAfterLoopAndKill(t *testing.T) {
	llm := &controlledRunLLM{started: make(chan struct{}), release: make(chan struct{})}
	fx := newRunServiceFixture(t, llm)
	req := runPrepareRequest("run-running-cancel", 1)
	if _, _, err := fx.svc.Prepare(fx.sid, req); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Start(fx.sid, req.RunID); err != nil {
		t.Fatal(err)
	}
	waitForLLMStart(t, llm.started)
	state, err := fx.svc.Cancel(fx.sid, req.RunID)
	if err != nil || state.Status != entity.RunCancelling || state.StoppedConfirmed {
		t.Fatalf("Cancel 只应返回受理状态: %+v, err=%v", state, err)
	}
	final := waitForRunState(t, fx.svc, fx.sid, req.RunID, func(state entity.RunState) bool {
		return state.Status == entity.RunStopped
	})
	if !final.StoppedConfirmed || final.Outcome != "cancelled" || final.ErrorCode != "CANCELLED" {
		t.Fatalf("running 取消的最终状态不正确: %+v", final)
	}
	if got := fx.exec.killCount(req.RunID); got != 1 {
		t.Fatalf("Cancel 与回合回调应共享一次 Kill，实际 %d 次", got)
	}
}

func TestRunRemainingProcessesPreventStoppedConfirmation(t *testing.T) {
	fx := newRunServiceFixture(t, &controlledRunLLM{})
	req := runPrepareRequest("run-kill-remaining", 1)
	fx.exec.setRemaining(req.RunID, 2)
	if _, _, err := fx.svc.Prepare(fx.sid, req); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.Start(fx.sid, req.RunID); err != nil {
		t.Fatal(err)
	}
	state := waitForRunState(t, fx.svc, fx.sid, req.RunID, func(state entity.RunState) bool {
		return state.ErrorCode == "STOP_UNCONFIRMED"
	})
	if state.Status != entity.RunCancelling || state.StoppedConfirmed || state.Outcome != "" || state.StoppedAt != "" {
		t.Fatalf("仍有进程时不得确认停止: %+v", state)
	}
	persisted, err := fx.store.Get(fx.sid, req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != entity.RunCancelling || persisted.StoppedConfirmed || persisted.ErrorCode != "STOP_UNCONFIRMED" {
		t.Fatalf("未确认状态未持久化: %+v", persisted.RunState)
	}
}

func TestRunRecoverConvergesActiveAndPreservesPrepared(t *testing.T) {
	fx := newRunServiceFixture(t, &controlledRunLLM{})
	records := []entity.RunRecord{
		{RunState: entity.RunState{RunID: "run-recover-prepared", Status: entity.RunPrepared, CreatedAt: "2026-01-01T00:00:00Z"}, SessionID: fx.sid, Content: "prepared"},
		{RunState: entity.RunState{RunID: "run-recover-running", Status: entity.RunRunning, CreatedAt: "2026-01-01T00:00:01Z"}, SessionID: fx.sid, Content: "running"},
		{RunState: entity.RunState{RunID: "run-recover-cancelling", Status: entity.RunCancelling, CreatedAt: "2026-01-01T00:00:02Z"}, SessionID: fx.sid, Content: "cancelling"},
	}
	for _, rec := range records {
		if err := fx.store.Put(fx.sid, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := fx.store.AddRunPGID(fx.sid, "run-recover-running", 4321); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	prepared, err := fx.store.Get(fx.sid, "run-recover-prepared")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != entity.RunPrepared || prepared.StoppedConfirmed || prepared.StoppedAt != "" {
		t.Fatalf("Recover 不应修改 prepared: %+v", prepared.RunState)
	}
	if got := fx.exec.killCount("run-recover-prepared"); got != 0 {
		t.Fatalf("Recover 不应 Kill prepared，次数 = %d", got)
	}
	for _, runID := range []string{"run-recover-running", "run-recover-cancelling"} {
		rec, err := fx.store.Get(fx.sid, runID)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Status != entity.RunStopped || rec.Outcome != "failed" || rec.ErrorCode != "RUNTIME_RESTARTED" || !rec.StoppedConfirmed || rec.StoppedAt == "" {
			t.Fatalf("活动 Run 未收敛: %s => %+v", runID, rec.RunState)
		}
		if got := fx.exec.killCount(runID); got != 1 {
			t.Fatalf("%s 的 Recover Kill 次数 = %d，期望 1", runID, got)
		}
	}
	if got := fx.exec.pgids("run-recover-running"); !reflect.DeepEqual(got, []int{4321}) {
		t.Fatalf("Recover 未重新登记持久化 PGID: %v", got)
	}
}

func TestRunPrepareRejectsLegacyRunningTurn(t *testing.T) {
	llm := &controlledRunLLM{started: make(chan struct{}), release: make(chan struct{})}
	fx := newRunServiceFixture(t, llm)
	if err := fx.manager.RunTurn(fx.sid, "legacy turn"); err != nil {
		t.Fatal(err)
	}
	waitForLLMStart(t, llm.started)
	_, _, prepareErr := fx.svc.Prepare(fx.sid, runPrepareRequest("run-during-legacy", 1))
	fx.manager.Cancel(fx.sid)
	waitTurnDone(t, fx.manager, fx.sid)
	if !errors.Is(prepareErr, derr.ErrSessionBusy) {
		t.Fatalf("旧 messages 回合运行中 Prepare: err=%v，期望 ErrSessionBusy", prepareErr)
	}
}
