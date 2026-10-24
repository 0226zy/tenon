// Package run is the sole control-plane Run workflow and recovery driver.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/consumer"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/quota"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/httpx"
	"github.com/0226zy/tenon/internal/kit/idgen"
	"github.com/0226zy/tenon/internal/kit/logx"
	"github.com/0226zy/tenon/pkg/contract"
)

type Catalog interface{ Validate(string) error }
type Sandbox interface {
	Ensure(context.Context, string, string) (sandbox.Handle, error)
	ForceStop(context.Context, string) error
	Lookup(context.Context, string) (sandbox.Handle, int, error)
}
type Consumer interface {
	Consume(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error)
}
type Service struct {
	st       *store.Store
	ev       *event.Store
	hub      *event.Hub
	ids      idgen.IDGen
	catalog  Catalog
	sb       Sandbox
	ac       *agentclient.Client
	consumer Consumer
	q        *quota.Manager
	cfg      *config.Config
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	drivers  map[string]bool
	signals  map[string]chan struct{}
	acceptMu sync.Mutex
}

func NewService(st *store.Store, ev *event.Store, hub *event.Hub, ids idgen.IDGen, cat Catalog, sb Sandbox, ac *agentclient.Client, c Consumer, q *quota.Manager, cfg *config.Config) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{st: st, ev: ev, hub: hub, ids: ids, catalog: cat, sb: sb, ac: ac, consumer: c, q: q, cfg: cfg, ctx: ctx, cancel: cancel, drivers: map[string]bool{}, signals: map[string]chan struct{}{}}
}
func (s *Service) Accept(ctx context.Context, userID, convID string, req contract.AcceptRequest) (contract.AcceptResult, error) {
	if strings.TrimSpace(req.ClientRequestID) == "" || len(req.ClientRequestID) > 64 {
		return contract.AcceptResult{}, httpx.E(400, "BAD_REQUEST", "client_request_id is required and must be at most 64 bytes")
	}
	if strings.TrimSpace(req.Content) == "" {
		return contract.AcceptResult{}, httpx.E(400, "EMPTY", "message content is empty")
	}
	if utf8.RuneCountInString(req.Content) > s.cfg.Run.ContentMaxChars {
		return contract.AcceptResult{}, httpx.E(400, "TOO_LONG", "message content is too long")
	}
	s.acceptMu.Lock()
	defer s.acceptMu.Unlock()
	var result contract.AcceptResult
	err := s.st.WithTx(ctx, func(tx *store.Tx) error {
		conv, err := s.st.LockConversation(ctx, tx, userID, convID)
		if err != nil {
			return err
		}
		model := req.Model
		if model == "" {
			model = conv.Model
		}
		if err := s.catalog.Validate(model); err != nil {
			return httpx.E(400, "BAD_MODEL", "unknown model")
		}
		hash := inputHash(req.Content, model)
		if old, err := s.st.RunByRequest(ctx, tx, convID, req.ClientRequestID); err == nil {
			if old.InputHash != hash {
				return httpx.E(409, "IDEMPOTENCY_CONFLICT", "client_request_id was used with different input")
			}
			msg, err := s.st.MessageByRunRole(ctx, tx, old.RunID, 1)
			if err != nil {
				return err
			}
			seq, _ := s.st.EventSeqByRunType(ctx, tx, old.RunID, "run_accepted")
			result = contract.AcceptResult{RunID: old.RunID, MessageID: msg.MsgID, Seq: seq, Status: old.Status}
			return nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if s.q.QueueLen(userID) >= s.cfg.Quota.Queue.PerUser {
			return httpx.E(429, "QUOTA_EXCEEDED", "user Run queue is full")
		}
		r := &store.Run{RunID: s.ids.ULID(), ConvID: convID, UserID: userID, RequestID: req.ClientRequestID, InputHash: hash, Model: model, Status: contract.RunQueued}
		m := &store.Message{MsgID: s.ids.ULID(), ConvID: convID, RunID: r.RunID, Role: 1, Content: req.Content}
		if err := s.st.CreateRun(ctx, tx, r); err != nil {
			if errors.Is(err, store.ErrDuplicate) {
				return httpx.E(409, "RUN_IN_PROGRESS", "conversation already has an active Run")
			}
			return err
		}
		if err := s.st.CreateMessage(ctx, tx, m); err != nil {
			return err
		}
		if conv.Title == "" {
			title := autoTitle(req.Content)
			if err := s.st.UpdateConversation(ctx, tx, convID, &title, nil, nil); err != nil {
				return err
			}
		}
		seq, err := s.ev.AppendTx(ctx, tx, event.Control(convID, r.RunID, "run_accepted", map[string]any{"run_id": r.RunID, "message_id": m.MsgID, "status": contract.RunQueued}))
		if err != nil {
			return err
		}
		result = contract.AcceptResult{RunID: r.RunID, MessageID: m.MsgID, Seq: seq, Status: contract.RunQueued}
		tx.AfterCommit(func() { s.Enqueue(r.RunID) })
		return nil
	})
	return result, err
}
func inputHash(content, model string) string {
	raw, _ := json.Marshal(struct{ Content, Model string }{content, model})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func autoTitle(content string) string {
	line := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	r := []rune(line)
	if len(r) > 32 {
		r = append(r[:32], '…')
	}
	if len(r) == 0 {
		return "新对话"
	}
	return string(r)
}
func (s *Service) Get(ctx context.Context, userID, convID, runID string) (contract.RunView, error) {
	r, err := s.st.RunOwned(ctx, s.st.DB(), userID, convID, runID)
	if err != nil {
		return contract.RunView{}, err
	}
	return runView(r), nil
}
func runView(r *store.Run) contract.RunView {
	v := contract.RunView{RunID: r.RunID, Status: r.Status, CheckpointStatus: store.CheckpointName(r.CheckpointStatus), Commit: r.Commit, ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage, Generation: r.Generation, Usage: contract.Usage{Prompt: r.PromptTokens, Completion: r.ComplTokens}}
	if r.StartedAt != nil {
		v.StartedAt = r.StartedAt.Format(time.RFC3339Nano)
	}
	if r.EndedAt != nil {
		v.EndedAt = r.EndedAt.Format(time.RFC3339Nano)
	}
	return v
}
func (s *Service) Cancel(ctx context.Context, userID, convID, runID string) (contract.RunView, error) {
	var out contract.RunView
	err := s.st.WithTx(ctx, func(tx *store.Tx) error {
		if _, err := s.st.ConversationOwned(ctx, tx, userID, convID); err != nil {
			return err
		}
		r, err := s.st.RunOwned(ctx, tx, userID, convID, runID)
		if err != nil {
			return err
		}
		out = runView(r)
		if !r.Status.Active() || r.Status == contract.RunFinalizing {
			return nil
		}
		was := r.Status
		if err := s.st.RequestRunCancel(ctx, tx, runID); err != nil {
			return err
		}
		if was != contract.RunCancelling {
			if _, err := s.ev.AppendTx(ctx, tx, event.Control(convID, runID, "run_cancelling", map[string]any{"status": contract.RunCancelling})); err != nil {
				return err
			}
		}
		out.Status = contract.RunCancelling
		tx.AfterCommit(func() { s.Signal(runID) })
		return nil
	})
	return out, err
}
func (s *Service) CancelActive(ctx context.Context, convID, reason string) error {
	r, err := s.st.ActiveRun(ctx, s.st.DB(), convID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.Cancel(ctx, r.UserID, convID, r.RunID)
	return err
}
func (s *Service) Signal(runID string) {
	s.mu.Lock()
	ch := s.signals[runID]
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
func (s *Service) Enqueue(runID string) {
	s.mu.Lock()
	if s.drivers[runID] {
		s.mu.Unlock()
		return
	}
	s.drivers[runID] = true
	s.signals[runID] = make(chan struct{}, 1)
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		err := s.Drive(runID)
		s.mu.Lock()
		delete(s.drivers, runID)
		delete(s.signals, runID)
		s.mu.Unlock()
		if err != nil {
			logx.From(s.ctx).Error("Run driver will retry", "run_id", runID, "error", err)
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(time.Second):
				s.Enqueue(runID)
			}
		}
	}()
}
func (s *Service) Close() { s.cancel(); s.wg.Wait() }
func (s *Service) Recover(ctx context.Context) error {
	runs, err := s.st.ListActiveRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		s.Enqueue(r.RunID)
	}
	return nil
}
func (s *Service) HasActiveRun(convID string) bool {
	r, err := s.st.ActiveRun(context.Background(), s.st.DB(), convID)
	return err == nil && r.Active
}

func (s *Service) Drive(runID string) error {
	r, err := s.st.Run(s.ctx, s.st.DB(), runID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.Status.Active() {
		return nil
	}
	s.mu.Lock()
	signal := s.signals[runID]
	s.mu.Unlock()
	if r.Status == contract.RunFinalizing {
		out := consumer.Outcome{Status: terminalOutcome(r), ErrorCode: r.ErrorCode, RtSeqEnd: r.RtSeqEnd, Iterations: r.Iterations, RunID: r.RunID}
		if r.Generation > 0 {
			h, err := s.sb.Ensure(s.ctx, r.ConvID, r.UserID)
			if err != nil {
				return err
			}
			return s.finalize(h, r, out, true)
		}
		return s.finalize(sandbox.Handle{ConvID: r.ConvID}, r, out, false)
	}
	acqCtx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-signal:
			cancel()
		case <-done:
		}
	}()
	timer := time.AfterFunc(s.cfg.Quota.Queue.Timeout.Duration, cancel)
	res, err := s.q.Acquire(acqCtx, quota.KindRun, r.UserID, r.ConvID)
	timer.Stop()
	close(done)
	cancel()
	if err != nil {
		r, _ = s.st.Run(s.ctx, s.st.DB(), runID)
		if r != nil && r.CancelReq {
			return s.finalize(sandbox.Handle{ConvID: r.ConvID}, r, consumer.Outcome{Status: "cancelled", RunID: runID}, false)
		}
		if s.ctx.Err() != nil {
			return s.ctx.Err()
		}
		return s.failBeforeStart(r, "QUEUE_TIMEOUT")
	}
	defer s.q.Release(res.ID)
	r, err = s.st.Run(s.ctx, s.st.DB(), runID)
	if err != nil {
		return err
	}
	if r.CancelReq && r.Generation == 0 {
		return s.finalize(sandbox.Handle{ConvID: r.ConvID}, r, consumer.Outcome{Status: "cancelled", RunID: runID}, false)
	}
	h, err := s.sb.Ensure(s.ctx, r.ConvID, r.UserID)
	if err != nil {
		if r.Generation == 0 {
			return s.failBeforeStart(r, "SANDBOX_START_FAILED")
		}
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		stopErr := s.sb.ForceStop(ctx, r.ConvID)
		cancel()
		if stopErr != nil {
			return fmt.Errorf("cannot confirm sandbox stop: %w", stopErr)
		}
		return s.failBeforeStart(r, "CONTAINER_STOPPED")
	}
	conv, err := s.st.Conversation(s.ctx, s.st.DB(), r.ConvID)
	if err != nil {
		return err
	}
	if conv.AgentSessionID == "" {
		cwd := "/workspace/app"
		if s.cfg.Mode == "local" {
			cwd = filepath.Join(s.cfg.Local.Root, r.ConvID, "workspace", "app")
		}
		sess, err := s.ac.CreateSession(s.ctx, h, agentclient.CreateSessionRequest{Cwd: cwd, Mode: "full-auto", Title: conv.Title, Model: r.Model})
		if err != nil {
			return s.failBeforeStart(r, "SANDBOX_START_FAILED")
		}
		epoch := sess.Epoch
		if epoch == "" {
			epoch = s.ids.ULID()
		}
		if err := s.st.SetRuntimeSession(s.ctx, s.st.DB(), r.ConvID, sess.ID, epoch, 0); err != nil {
			return err
		}
		conv.AgentSessionID, conv.RuntimeEpoch = sess.ID, epoch
	} else {
		sess, err := s.ac.GetSession(s.ctx, h, conv.AgentSessionID)
		if err == nil && sess.Model != r.Model {
			if err := s.ac.SetModel(s.ctx, h, conv.AgentSessionID, r.Model); err != nil {
				return err
			}
		}
	}
	if r.Status == contract.RunQueued {
		err := s.st.WithTx(s.ctx, func(tx *store.Tx) error {
			gen, err := s.st.NextGeneration(s.ctx, tx, r.ConvID)
			if err != nil {
				return err
			}
			r.Generation = gen
			return s.st.MarkDispatching(s.ctx, tx, r.RunID, gen)
		})
		if err != nil {
			return err
		}
		r.Status = contract.RunDispatching
	}
	if r.Generation == 0 {
		return fmt.Errorf("active Run has no generation")
	}
	grant, err := s.ac.GateAcquire(s.ctx, h, contract.GateAcquireRequest{Holder: "run", RunID: r.RunID, Generation: r.Generation, WaitMS: int(s.cfg.Preview.BuildTimeout.Milliseconds())})
	if err != nil || grant.Holder != "run" || grant.RunID != r.RunID || grant.Generation != r.Generation {
		// A response without a matching grant is not permission to execute or checkpoint.
		return s.failBeforeStart(r, "GATE_BUSY")
	}
	if r.Status == contract.RunDispatching {
		req := contract.PrepareRunRequest{RunID: r.RunID, Content: s.messageContent(r), Model: r.Model, PayloadHash: r.InputHash, Generation: r.Generation}
		if err := s.prepare(h, conv.AgentSessionID, req); err != nil {
			return s.stopAndFail(h, r, "RUNTIME_RESTARTED", true)
		}
		if err := s.st.WithTx(s.ctx, func(tx *store.Tx) error { return s.st.MarkRunning(s.ctx, tx, r.RunID, time.Now().UTC()) }); err != nil {
			return err
		}
		r.Status = contract.RunRunning
	}
	r, _ = s.st.Run(s.ctx, s.st.DB(), r.RunID)
	if r.Status == contract.RunRunning {
		if _, err := s.ac.StartRun(s.ctx, h, conv.AgentSessionID, r.RunID); err != nil {
			var ae *agentclient.Error
			if !errors.As(err, &ae) || ae.Status != 409 {
				return s.stopAndFail(h, r, "RUNTIME_RESTARTED", true)
			}
		}
		if err := s.appendControlOnce(r, "run_started", map[string]any{"status": contract.RunRunning, "generation": r.Generation}); err != nil {
			return err
		}
	}
	if r.CancelReq || r.Status == contract.RunCancelling {
		return s.cancelAndFinalize(h, r, "cancelled", "")
	}
	runCtx, runCancel := context.WithDeadline(s.ctx, time.Now().Add(s.cfg.Run.MaxDuration.Duration))
	signalDone := make(chan struct{})
	go func() {
		select {
		case <-signal:
			runCancel()
		case <-signalDone:
		case <-runCtx.Done():
		}
	}()
	outcome, consumeErr := s.consumer.Consume(runCtx, h, r)
	close(signalDone)
	runCancel()
	outcome.RunID = r.RunID
	r, _ = s.st.Run(s.ctx, s.st.DB(), r.RunID)
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		_ = s.st.RequestRunCancel(s.ctx, s.st.DB(), r.RunID)
		return s.cancelAndFinalize(h, r, "failed", "RUN_TIMEOUT")
	}
	if r.CancelReq || r.Status == contract.RunCancelling {
		return s.cancelAndFinalize(h, r, "cancelled", "")
	}
	if consumeErr != nil {
		return s.cancelAndFinalize(h, r, "failed", "LLM_ERROR")
	}
	return s.stopAndFinalize(h, r, outcome)
}
func terminalOutcome(r *store.Run) string {
	if r.CancelReq {
		return "cancelled"
	}
	if r.ErrorCode != "" {
		return "failed"
	}
	return "done"
}
func (s *Service) messageContent(r *store.Run) string {
	m, err := s.st.MessageByRunRole(s.ctx, s.st.DB(), r.RunID, 1)
	if err != nil {
		return ""
	}
	return m.Content
}
func (s *Service) prepare(h sandbox.Handle, sid string, req contract.PrepareRunRequest) error {
	var last error
	for i := 0; i < s.cfg.Run.PrepareRetry; i++ {
		_, err := s.ac.PrepareRun(s.ctx, h, sid, req)
		if err == nil {
			return nil
		}
		last = err
		if state, qerr := s.ac.GetRun(s.ctx, h, sid, req.RunID); qerr == nil && state.RunID == req.RunID {
			return nil
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(time.Duration(1<<i) * time.Second):
		}
	}
	return last
}
func (s *Service) cancelAndFinalize(h sandbox.Handle, r *store.Run, status, code string) error {
	if r.Status != contract.RunCancelling && r.Status != contract.RunFinalizing {
		if err := s.st.WithTx(s.ctx, func(tx *store.Tx) error {
			if err := s.st.RequestRunCancel(s.ctx, tx, r.RunID); err != nil {
				return err
			}
			_, err := s.ev.AppendTx(s.ctx, tx, event.Control(r.ConvID, r.RunID, "run_cancelling", map[string]any{"status": contract.RunCancelling}))
			return err
		}); err != nil {
			return err
		}
	}
	if _, err := s.ac.CancelRun(s.ctx, h, convSession(s.ctx, s.st, r.ConvID), r.RunID); err != nil {
		logx.From(s.ctx).Warn("cancel runtime failed", "run_id", r.RunID, "error", err)
	}
	out := consumer.Outcome{Status: status, ErrorCode: code, RtSeqEnd: r.RtSeqEnd, Iterations: r.Iterations, RunID: r.RunID}
	return s.stopAndFinalize(h, r, out)
}
func convSession(ctx context.Context, st *store.Store, convID string) string {
	c, err := st.Conversation(ctx, st.DB(), convID)
	if err != nil {
		return ""
	}
	return c.AgentSessionID
}
func (s *Service) stopAndFail(h sandbox.Handle, r *store.Run, code string, cancel bool) error {
	status := "failed"
	if cancel {
		_, _ = s.ac.CancelRun(s.ctx, h, convSession(s.ctx, s.st, r.ConvID), r.RunID)
	}
	return s.stopAndFinalize(h, r, consumer.Outcome{Status: status, ErrorCode: code, RtSeqEnd: r.RtSeqEnd, Iterations: r.Iterations, RunID: r.RunID})
}
func (s *Service) stopAndFinalize(h sandbox.Handle, r *store.Run, out consumer.Outcome) error {
	conv, err := s.st.Conversation(s.ctx, s.st.DB(), r.ConvID)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(s.cfg.Run.StopConfirmTimeout.Duration)
	confirmed := false
	for time.Now().Before(deadline) {
		state, stateErr := s.ac.GetRun(s.ctx, h, conv.AgentSessionID, r.RunID)
		stopped := stateErr == nil && (state.StoppedConfirmed || state.Status == contract.RuntimeStopped)
		remaining := -1
		if stopped {
			reap, reapErr := s.ac.Reap(s.ctx, h, r.RunID)
			if reapErr == nil {
				remaining = reap.Remaining
			}
		}
		if stopped && remaining == 0 {
			confirmed = true
			out.RtSeqEnd = max(out.RtSeqEnd, state.NextSeq)
			out.Iterations = max(out.Iterations, state.Iterations)
			break
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !confirmed {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		err := s.sb.ForceStop(ctx, r.ConvID)
		cancel()
		if err != nil {
			return fmt.Errorf("stop not confirmed: %w", err)
		}
		if out.Status == "done" {
			out.Status = "failed"
			out.ErrorCode = "CONTAINER_STOPPED"
		}
	}
	if err := s.st.MarkStopConfirmed(s.ctx, s.st.DB(), r.RunID); err != nil {
		return err
	}
	if out.ErrorCode == "" && out.Status == "failed" {
		out.ErrorCode = "LLM_ERROR"
	}
	if r.Status != contract.RunFinalizing {
		err := s.st.WithTx(s.ctx, func(tx *store.Tx) error {
			if err := s.st.SetRunRuntimeStats(s.ctx, tx, r.RunID, out.RtSeqEnd, out.Iterations); err != nil {
				return err
			}
			if out.ErrorCode != "" {
				if err := s.st.SetRunFailure(s.ctx, tx, r.RunID, out.ErrorCode, publicError(out.ErrorCode)); err != nil {
					return err
				}
			}
			ok, err := s.st.TransitionRun(s.ctx, tx, r.RunID, nil, contract.RunFinalizing)
			if err != nil {
				return err
			}
			if ok {
				_, err = s.ev.AppendTx(s.ctx, tx, event.Control(r.ConvID, r.RunID, "run_finalizing", map[string]any{"status": contract.RunFinalizing}))
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	r, _ = s.st.Run(s.ctx, s.st.DB(), r.RunID)
	return s.finalize(h, r, out, true)
}
func (s *Service) failBeforeStart(r *store.Run, code string) error {
	if r == nil {
		return fmt.Errorf("missing Run")
	}
	return s.finalize(sandbox.Handle{ConvID: r.ConvID}, r, consumer.Outcome{Status: "failed", ErrorCode: code, RtSeqEnd: r.RtSeqEnd, Iterations: r.Iterations, RunID: r.RunID}, false)
}
func (s *Service) finalize(h sandbox.Handle, r *store.Run, out consumer.Outcome, executed bool) error {
	if !executed && r.Status != contract.RunFinalizing {
		err := s.st.WithTx(s.ctx, func(tx *store.Tx) error {
			if err := s.st.MarkStopConfirmed(s.ctx, tx, r.RunID); err != nil {
				return err
			}
			if out.ErrorCode != "" {
				if err := s.st.SetRunFailure(s.ctx, tx, r.RunID, out.ErrorCode, publicError(out.ErrorCode)); err != nil {
					return err
				}
			}
			ok, err := s.st.TransitionRun(s.ctx, tx, r.RunID, nil, contract.RunFinalizing)
			if err != nil {
				return err
			}
			if ok {
				_, err = s.ev.AppendTx(s.ctx, tx, event.Control(r.ConvID, r.RunID, "run_finalizing", map[string]any{"status": contract.RunFinalizing, "not_executed": r.Generation == 0}))
			}
			return err
		})
		if err != nil {
			return err
		}
		r, err = s.st.Run(s.ctx, s.st.DB(), r.RunID)
		if err != nil {
			return err
		}
	}
	checkpoint, commit := "done", ""
	if !executed && r.Generation > 0 && r.StartedAt != nil {
		checkpoint = "failed"
	}
	var changes []contract.FileChange
	filesTotal := 0
	if executed && h.Addr != "" {
		cr, err := s.ac.Commit(s.ctx, h, contract.CommitRequest{RunID: r.RunID, Generation: r.Generation, Message: "Run " + r.RunID})
		if err != nil || cr.Error != "" {
			checkpoint = "failed"
		} else {
			checkpoint = cr.CheckpointStatus
			if checkpoint == "" {
				checkpoint = "done"
			}
			commit = cr.Commit
			changes = cr.Files
			filesTotal = cr.FilesTotal
		}
		_ = s.ac.GateRelease(s.ctx, h, r.RunID)
	}
	terminal := contract.RunFinished
	switch {
	case out.Status == "cancelled":
		terminal = contract.RunCancelled
	case out.Status == "failed":
		terminal = contract.RunFailed
	case checkpoint == "failed":
		terminal = contract.RunFailed
		out.ErrorCode = "CHECKPOINT_FAILED"
	}
	if out.ErrorCode == "" && terminal == contract.RunFailed {
		out.ErrorCode = "LLM_ERROR"
	}
	prompt, completion, err := s.st.SumUsageByRun(s.ctx, s.st.DB(), r.RunID)
	if err != nil {
		return err
	}
	assistant := out.Assistant
	if assistant == "" {
		assistant, _ = s.st.LatestAssistantContent(s.ctx, s.st.DB(), r.ConvID, r.RunID)
	}
	ended := time.Now().UTC()
	typ := "run_finished"
	if terminal == contract.RunFailed {
		typ = "run_failed"
	} else if terminal == contract.RunCancelled {
		typ = "run_cancelled"
	}
	payload := contract.RunEnd{Commit: commit, CheckpointStatus: checkpoint, Files: changes, FilesTotal: filesTotal, DurationMS: ended.Sub(r.CreatedAt).Milliseconds(), Usage: contract.Usage{Prompt: prompt, Completion: completion}, ErrorCode: out.ErrorCode}
	err = s.st.WithTx(s.ctx, func(tx *store.Tx) error {
		current, err := s.st.Run(s.ctx, tx, r.RunID)
		if err != nil {
			return err
		}
		if !current.Status.Active() {
			return nil
		}
		if terminal == contract.RunFinished && assistant != "" {
			if _, err := s.st.MessageByRunRole(s.ctx, tx, r.RunID, 2); errors.Is(err, store.ErrNotFound) {
				m := &store.Message{MsgID: s.ids.ULID(), ConvID: r.ConvID, RunID: r.RunID, Role: 2, Content: assistant}
				if err := s.st.CreateMessage(s.ctx, tx, m); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		ok, err := s.st.FinalizeRun(s.ctx, tx, current, terminal, checkpoint, commit, out.ErrorCode, publicError(out.ErrorCode), prompt, completion, ended)
		if err != nil || !ok {
			return err
		}
		_, err = s.ev.AppendTx(s.ctx, tx, event.Control(r.ConvID, r.RunID, typ, payload))
		return err
	})
	if err != nil {
		return err
	}
	if terminal == contract.RunFinished {
		txCtx := s.ctx
		go func() {
			ctx, cancel := context.WithTimeout(txCtx, s.cfg.AgentClient.PreviewTimeout.Duration)
			defer cancel()
			handle, status, err := s.sb.Lookup(ctx, r.ConvID)
			if err != nil || status != 2 {
				return
			}
			st, err := s.ac.PreviewEnsure(ctx, handle, contract.PreviewEnsureRequest{Generation: r.Generation, Commit: commit})
			if err != nil {
				return
			}
			_ = s.st.UpdateSandboxPreview(ctx, s.st.DB(), r.ConvID, st.Status, st.Commit, st.Generation)
			if f, ok := (event.Mapper{}).TempToUI(r.RunID, "preview_status", st); ok {
				s.hub.PublishTemp(r.ConvID, f)
			}
		}()
	}
	return nil
}
func (s *Service) appendControlOnce(r *store.Run, typ string, data any) error {
	return s.st.WithTx(s.ctx, func(tx *store.Tx) error {
		_, err := s.st.EventSeqByRunType(s.ctx, tx, r.RunID, typ)
		if err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		_, err = s.ev.AppendTx(s.ctx, tx, event.Control(r.ConvID, r.RunID, typ, data))
		return err
	})
}
func publicError(code string) string {
	if code == "" {
		return ""
	}
	switch code {
	case "RUN_TIMEOUT":
		return "Run exceeded its time limit"
	case "QUEUE_TIMEOUT":
		return "Run waited too long in queue"
	case "SANDBOX_START_FAILED":
		return "sandbox failed to start"
	case "STOP_UNCONFIRMED":
		return "sandbox stop was not confirmed"
	case "CHECKPOINT_FAILED":
		return "checkpoint failed"
	default:
		return "Run failed: " + code
	}
}
