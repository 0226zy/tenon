package session

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/protoforge/agent-runtime/internal/application/agent"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/derr"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/entity"
	"github.com/0226zy/protoforge/agent-runtime/internal/domain/port"
)

// RunOptions 配置停止时间预算；Models 用于 Prepare 的只读目录校验。
type RunOptions struct {
	StopGrace          time.Duration
	StopConfirmTimeout time.Duration
	Models             port.LLMInfoRepo
	Now                func() time.Time
}

type runExecution struct {
	cancel   context.CancelFunc
	killOnce sync.Once
	killDone chan struct{}

	// 以下字段由对应会话的 Run 互斥锁保护，kill 结果另由 killDone 的关闭同步。
	loopDone  bool
	outcome   string
	errorCode string
	remaining int
	killErr   error
}

// RunService 实现 Run 的幂等接收、启动、查询、取消与重启收敛。
type RunService struct {
	m     *Manager
	store port.RunStore
	exec  port.ExecTracker
	opts  RunOptions

	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	executions map[string]*runExecution
}

func NewRunService(m *Manager, store port.RunStore, exec port.ExecTracker, opts RunOptions) *RunService {
	if opts.StopGrace <= 0 {
		opts.StopGrace = 5 * time.Second
	}
	if opts.StopConfirmTimeout <= 0 {
		opts.StopConfirmTimeout = 20 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if exec == nil {
		exec = noopExecTracker{}
	}
	return &RunService{m: m, store: store, exec: exec, opts: opts,
		locks: map[string]*sync.Mutex{}, executions: map[string]*runExecution{}}
}

func runKey(sid, runID string) string { return sid + "\x00" + runID }

func (s *RunService) lockFor(sid string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.locks[sid]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[sid] = lock
	}
	return lock
}

func (s *RunService) execution(sid, runID string) *runExecution {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executions[runKey(sid, runID)]
}

func (s *RunService) now() string { return s.opts.Now().UTC().Format(time.RFC3339Nano) }

// Prepare 按同 run_id、generation、会话 busy、模型目录的顺序检查并原子持久化准备记录。
func (s *RunService) Prepare(sid string, req entity.PrepareRun) (entity.RunState, bool, error) {
	if err := validatePrepare(req); err != nil {
		return entity.RunState{}, false, err
	}
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()

	rec, err := s.store.Get(sid, req.RunID)
	if err == nil {
		if rec.PayloadHash != req.PayloadHash {
			return entity.RunState{}, false, derr.ErrRunIDConflict
		}
		return rec.RunState, false, nil
	}
	if !errors.Is(err, derr.ErrRunNotFound) {
		return entity.RunState{}, false, err
	}

	maxGeneration, err := s.store.MaxGeneration(sid)
	if err != nil {
		return entity.RunState{}, false, err
	}
	if req.Generation < maxGeneration {
		return entity.RunState{}, false, derr.ErrStaleGeneration
	}
	records, err := s.store.List(sid)
	if err != nil {
		return entity.RunState{}, false, err
	}
	for _, other := range records {
		if other.RunID != req.RunID && other.Status != entity.RunStopped {
			return entity.RunState{}, false, derr.ErrSessionBusy
		}
	}
	rt, err := s.m.Get(sid)
	if err != nil {
		return entity.RunState{}, false, err
	}
	if rt.Running() {
		return entity.RunState{}, false, derr.ErrSessionBusy
	}
	modelName, err := s.validateModel(req.Model)
	if err != nil {
		return entity.RunState{}, false, err
	}
	header := rt.Header()
	rec = entity.RunRecord{RunState: entity.RunState{
		RunID: req.RunID, Status: entity.RunPrepared, Epoch: header.Epoch,
		NextSeq: rt.NextSeq(), Model: modelName, CreatedAt: s.now(),
	}, SessionID: sid, Content: req.Content, PayloadHash: req.PayloadHash, Generation: req.Generation}
	if err := s.store.Put(sid, rec); err != nil {
		return entity.RunState{}, false, err
	}
	if rt.Model() != modelName {
		if err := s.m.SetModel(sid, modelName); err != nil {
			return rec.RunState, true, err
		}
	}
	return rec.RunState, true, nil
}

func validatePrepare(req entity.PrepareRun) error {
	if len(req.RunID) == 0 || len(req.RunID) > 64 {
		return fmt.Errorf("%w: run_id 长度应为 1..64", derr.ErrInvalidRun)
	}
	for _, r := range req.RunID {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || r == '-') {
			return fmt.Errorf("%w: run_id 含非法字符", derr.ErrInvalidRun)
		}
	}
	if strings.TrimSpace(req.Content) == "" {
		return fmt.Errorf("%w: content 不能为空", derr.ErrInvalidRun)
	}
	if len(req.PayloadHash) != 64 {
		return fmt.Errorf("%w: payload_hash 应为 64 位 hex", derr.ErrInvalidRun)
	}
	if _, err := hex.DecodeString(req.PayloadHash); err != nil {
		return fmt.Errorf("%w: payload_hash 应为 64 位 hex", derr.ErrInvalidRun)
	}
	return nil
}

func (s *RunService) validateModel(name string) (string, error) {
	if s.opts.Models == nil {
		return name, nil
	}
	var profile entity.LLMInfo
	var err error
	if name == "" {
		profile, err = s.opts.Models.Default()
	} else {
		profile, err = s.opts.Models.Get(name)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s", derr.ErrModelNotFound, name)
	}
	return profile.Name, nil
}

// Start 仅 prepared 会启动；重复 start 返回当前状态，绝不重复执行。
func (s *RunService) Start(sid, runID string) (entity.RunState, error) {
	state, _, err := s.StartWithTransition(sid, runID)
	return state, err
}

// StartWithTransition 额外报告本次调用是否完成了 prepared → running 的首次转换。
// 状态检查与启动处于同一会话临界区，并发重复调用也至多一个 transitioned=true。
func (s *RunService) StartWithTransition(sid, runID string) (entity.RunState, bool, error) {
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()
	rec, err := s.store.Get(sid, runID)
	if err != nil {
		return entity.RunState{}, false, err
	}
	if rec.Status != entity.RunPrepared {
		return rec.RunState, false, nil
	}
	rt, err := s.m.Get(sid)
	if err != nil {
		return entity.RunState{}, false, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	execution := &runExecution{cancel: cancel, killDone: make(chan struct{})}
	s.mu.Lock()
	s.executions[runKey(sid, runID)] = execution
	s.mu.Unlock()

	rec.Status = entity.RunRunning
	rec.StartedAt = s.now()
	rec.SeqStart = rt.NextSeq()
	rec.NextSeq = rec.SeqStart
	if err := s.store.Put(sid, rec); err != nil {
		cancel()
		s.removeExecution(sid, runID, execution)
		return entity.RunState{}, false, err
	}
	err = s.m.startTurn(ctx, sid, runID, rec.Content, func(result agent.TurnResult, turnErr error) {
		s.turnFinished(sid, runID, result, turnErr)
	})
	if err != nil {
		cancel()
		s.removeExecution(sid, runID, execution)
		rec.Status = entity.RunPrepared
		rec.StartedAt = ""
		_ = s.store.Put(sid, rec)
		return rec.RunState, false, err
	}
	return rec.RunState, true, nil
}

func (s *RunService) removeExecution(sid, runID string, expected *runExecution) {
	s.mu.Lock()
	key := runKey(sid, runID)
	if s.executions[key] == expected {
		delete(s.executions, key)
	}
	s.mu.Unlock()
}

func (s *RunService) turnFinished(sid, runID string, result agent.TurnResult, turnErr error) {
	lock := s.lockFor(sid)
	lock.Lock()
	rec, err := s.store.Get(sid, runID)
	execution := s.execution(sid, runID)
	if err != nil || execution == nil {
		lock.Unlock()
		return
	}
	execution.loopDone = true
	execution.outcome = "done"
	execution.errorCode = result.ErrorCode
	switch {
	case rec.Status == entity.RunCancelling || errors.Is(turnErr, context.Canceled):
		execution.outcome = "cancelled"
		execution.errorCode = "CANCELLED"
	case turnErr != nil:
		execution.outcome = "failed"
		execution.errorCode = agent.ErrorCodeOf(turnErr)
	}
	rec.Iterations = result.Iterations
	rec.PromptTokens = result.PromptTokens
	rec.CompletionTokens = result.CompletionTokens
	if result.Model != "" {
		rec.Model = result.Model
	}
	if next, nextErr := s.m.NextSeq(sid); nextErr == nil {
		rec.NextSeq = next
	}
	_ = s.store.Put(sid, rec)
	lock.Unlock()

	done := s.startKill(sid, runID, execution)
	<-done
	_ = s.confirmStopped(sid, runID)
}

// startKill 保证同一执行树只启动一次清理；Cancel 与回合结束回调共享结果。
func (s *RunService) startKill(sid, runID string, execution *runExecution) <-chan struct{} {
	execution.killOnce.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), s.opts.StopConfirmTimeout)
			defer cancel()
			remaining, err := s.exec.Kill(ctx, runID, s.opts.StopGrace)
			execution.remaining = remaining
			execution.killErr = err
			// killDone 只同步清理结果；turnFinished 统一持久化停止状态，避免并发重复写。
			close(execution.killDone)
		}()
	})
	return execution.killDone
}

// confirmStopped 是唯一写 stopped_confirmed=true 的路径（prepared 取消除外）。
func (s *RunService) confirmStopped(sid, runID string) error {
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()
	rec, err := s.store.Get(sid, runID)
	if err != nil {
		return err
	}
	if rec.Status == entity.RunStopped {
		return nil
	}
	execution := s.execution(sid, runID)
	if execution == nil {
		return nil
	}
	select {
	case <-execution.killDone:
	default:
		return nil
	}
	if !execution.loopDone {
		return nil
	}
	if next, nextErr := s.m.NextSeq(sid); nextErr == nil {
		rec.NextSeq = next
	}
	if execution.remaining != 0 || execution.killErr != nil {
		rec.Status = entity.RunCancelling
		rec.Outcome = ""
		rec.StoppedConfirmed = false
		rec.ErrorCode = "STOP_UNCONFIRMED"
		return s.store.Put(sid, rec)
	}
	rec.Status = entity.RunStopped
	rec.Outcome = execution.outcome
	if rec.Outcome == "" {
		rec.Outcome = "failed"
	}
	rec.ErrorCode = execution.errorCode
	rec.StoppedConfirmed = true
	rec.StoppedAt = s.now()
	if err := s.store.Put(sid, rec); err != nil {
		return err
	}
	s.removeExecution(sid, runID, execution)
	return nil
}

func (s *RunService) Get(sid, runID string) (entity.RunState, error) {
	rec, err := s.store.Get(sid, runID)
	if err != nil {
		return entity.RunState{}, err
	}
	if next, nextErr := s.m.NextSeq(sid); nextErr == nil {
		rec.NextSeq = next
	}
	return rec.RunState, nil
}

// List 返回会话的持久 Run 视图，不暴露 Content、PayloadHash 或 PGID。
func (s *RunService) List(sid string) ([]entity.RunState, error) {
	if _, err := s.m.Get(sid); err != nil {
		return nil, err
	}
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()
	records, err := s.store.List(sid)
	if err != nil {
		return nil, err
	}
	next, err := s.m.NextSeq(sid)
	if err != nil {
		return nil, err
	}
	states := make([]entity.RunState, 0, len(records))
	for _, rec := range records {
		rec.NextSeq = next
		states = append(states, rec.RunState)
	}
	return states, nil
}

// SetModel 只允许在所有持久 Run 均为终态且没有旧回合运行时切换模型。
func (s *RunService) SetModel(sid, name string) (string, error) {
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()
	rt, err := s.m.Get(sid)
	if err != nil {
		return "", err
	}
	records, err := s.store.List(sid)
	if err != nil {
		return "", err
	}
	if rt.Running() {
		return "", derr.ErrSessionBusy
	}
	for _, rec := range records {
		if rec.Status != entity.RunStopped {
			return "", derr.ErrSessionBusy
		}
	}
	modelName, err := s.validateModel(name)
	if err != nil {
		return "", err
	}
	if err := s.m.SetModel(sid, modelName); err != nil {
		return "", err
	}
	return modelName, nil
}

// Cancel 撤销准备任务或请求停止执行任务；返回受理状态而非停止证明。
func (s *RunService) Cancel(sid, runID string) (entity.RunState, error) {
	lock := s.lockFor(sid)
	lock.Lock()
	rec, err := s.store.Get(sid, runID)
	if err != nil {
		lock.Unlock()
		return entity.RunState{}, err
	}
	if rec.Status == entity.RunStopped {
		lock.Unlock()
		return rec.RunState, nil
	}
	if rec.Status == entity.RunPrepared {
		rec.Status = entity.RunStopped
		rec.Outcome = "cancelled"
		rec.ErrorCode = "CANCELLED"
		rec.StoppedConfirmed = true
		rec.StoppedAt = s.now()
		err = s.store.Put(sid, rec)
		lock.Unlock()
		return rec.RunState, err
	}
	if rec.Status == entity.RunRunning {
		rec.Status = entity.RunCancelling
		rec.Outcome = ""
		rec.ErrorCode = ""
		if err := s.store.Put(sid, rec); err != nil {
			lock.Unlock()
			return entity.RunState{}, err
		}
		if rt, rtErr := s.m.Get(sid); rtErr == nil {
			_, _ = rt.AppendEvent(entity.Event{RunID: runID, Kind: entity.EventMarker,
				Marker: &entity.Marker{Type: "run_cancelling", RunID: runID}})
		}
	}
	execution := s.execution(sid, runID)
	if execution != nil {
		execution.cancel()
	}
	lock.Unlock()
	if execution != nil {
		s.startKill(sid, runID, execution)
	}
	return rec.RunState, nil
}

// Recover 将上次进程遗留的 running/cancelling Run 收敛；prepared 记录保持不变。
func (s *RunService) Recover(ctx context.Context) error {
	records, err := s.store.ListActive()
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range records {
		for _, pgid := range rec.Pgids {
			s.exec.Register(rec.RunID, pgid)
		}
		killCtx, cancel := context.WithTimeout(ctx, s.opts.StopConfirmTimeout)
		remaining, killErr := s.exec.Kill(killCtx, rec.RunID, 0)
		cancel()
		if next, nextErr := s.m.NextSeq(rec.SessionID); nextErr == nil {
			rec.NextSeq = next
		}
		if killErr == nil && remaining == 0 {
			rec.Status = entity.RunStopped
			rec.Outcome = "failed"
			rec.ErrorCode = "RUNTIME_RESTARTED"
			rec.StoppedConfirmed = true
			rec.StoppedAt = s.now()
		} else {
			rec.Status = entity.RunCancelling
			rec.Outcome = ""
			rec.ErrorCode = "STOP_UNCONFIRMED"
			rec.StoppedConfirmed = false
			errs = append(errs, fmt.Errorf("Run %s 停止未确认: remaining=%d: %w", rec.RunID, remaining, killErr))
		}
		if err := s.store.Put(rec.SessionID, rec); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type noopExecTracker struct{}

func (noopExecTracker) Register(string, int)                                     {}
func (noopExecTracker) Kill(context.Context, string, time.Duration) (int, error) { return 0, nil }
func (noopExecTracker) Alive(string) int                                         { return 0 }
