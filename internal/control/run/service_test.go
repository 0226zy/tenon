package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/consumer"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/quota"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/idgen"
	"github.com/0226zy/tenon/pkg/contract"
)

type testCatalog struct{}

func (testCatalog) Validate(model string) error {
	if model != "local-demo" {
		return fmt.Errorf("unknown model")
	}
	return nil
}

func (testCatalog) CheckReady(model string) error {
	return testCatalog{}.Validate(model)
}

type fakeSandbox struct {
	handle    sandbox.Handle
	forceStop atomic.Int32
}

func (f *fakeSandbox) Ensure(context.Context, string, string) (sandbox.Handle, error) {
	return f.handle, nil
}
func (f *fakeSandbox) ForceStop(context.Context, string) error {
	f.forceStop.Add(1)
	return nil
}
func (f *fakeSandbox) Lookup(_ context.Context, convID string) (sandbox.Handle, int, error) {
	return sandbox.Handle{ConvID: convID}, 0, nil
}

type fakeConsumer struct {
	consume func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error)
}

func (f *fakeConsumer) Consume(ctx context.Context, h sandbox.Handle, r *store.Run) (consumer.Outcome, error) {
	return f.consume(ctx, h, r)
}

type fakeRuntime struct {
	server      *httptest.Server
	gateWrong   atomic.Bool
	startStatus atomic.Int32
	prepares    atomic.Int32
	starts      atomic.Int32
	cancels     atomic.Int32
	commits     atomic.Int32
	reaps       atomic.Int32
	gates       atomic.Int32
	releases    atomic.Int32
}

func newFakeRuntime(t *testing.T) *fakeRuntime {
	t.Helper()
	f := &fakeRuntime{}
	f.startStatus.Store(http.StatusOK)
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRuntime) serveHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := req.URL.Path
	switch {
	case req.Method == http.MethodPost && path == "/agent/sessions":
		_ = json.NewEncoder(w).Encode(contract.SessionView{ID: "session-1", Epoch: "epoch-1", Model: "local-demo"})
	case req.Method == http.MethodGet && path == "/agent/sessions/session-1":
		_ = json.NewEncoder(w).Encode(contract.SessionView{ID: "session-1", Epoch: "epoch-1", Model: "local-demo"})
	case req.Method == http.MethodPost && path == "/gate/acquire":
		f.gates.Add(1)
		var in contract.GateAcquireRequest
		_ = json.NewDecoder(req.Body).Decode(&in)
		grant := contract.GateGrant{Holder: in.Holder, RunID: in.RunID, Generation: in.Generation}
		if f.gateWrong.Load() {
			grant.Holder = "preview"
			grant.RunID = "another-run"
		}
		_ = json.NewEncoder(w).Encode(grant)
	case req.Method == http.MethodPost && path == "/gate/release":
		f.releases.Add(1)
		w.WriteHeader(http.StatusNoContent)
	case req.Method == http.MethodPost && path == "/agent/sessions/session-1/runs":
		f.prepares.Add(1)
		var in contract.PrepareRunRequest
		_ = json.NewDecoder(req.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(contract.RunState{RunID: in.RunID, Status: contract.RuntimePrepared})
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		f.starts.Add(1)
		runID := strings.Split(path, "/")[5]
		w.WriteHeader(int(f.startStatus.Load()))
		_ = json.NewEncoder(w).Encode(contract.RunState{RunID: runID, Status: contract.RuntimeRunning})
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/cancel"):
		f.cancels.Add(1)
		runID := strings.Split(path, "/")[5]
		_ = json.NewEncoder(w).Encode(contract.RunState{RunID: runID, Status: contract.RuntimeStopped, StoppedConfirmed: true})
	case req.Method == http.MethodGet && strings.Contains(path, "/runs/"):
		runID := strings.Split(path, "/")[5]
		_ = json.NewEncoder(w).Encode(contract.RunState{RunID: runID, Status: contract.RuntimeStopped, Outcome: "done", StoppedConfirmed: true})
	case req.Method == http.MethodPost && path == "/exec/reap":
		f.reaps.Add(1)
		_ = json.NewEncoder(w).Encode(contract.ReapResult{Remaining: 0})
	case req.Method == http.MethodPost && path == "/git/commit":
		f.commits.Add(1)
		_ = json.NewEncoder(w).Encode(contract.CommitResult{Commit: "commit-1", CheckpointStatus: "done"})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(contract.ErrorResponse{Error: contract.ErrorBody{Code: "NOT_FOUND"}})
	}
}

type runFixture struct {
	st      *store.Store
	cfg     *config.Config
	events  *event.Store
	hub     *event.Hub
	quota   *quota.Manager
	runtime *fakeRuntime
	sandbox *fakeSandbox
	service *Service
	conv    *store.Conversation
}

func newRunFixture(t *testing.T, consume func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error)) *runFixture {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = "local"
	cfg.Run.MaxDuration = config.Duration{Duration: 2 * time.Second}
	cfg.Run.StopConfirmTimeout = config.Duration{Duration: 200 * time.Millisecond}
	cfg.Run.PrepareRetry = 1
	cfg.Quota.Queue.Timeout = config.Duration{Duration: time.Second}
	cfg.AgentClient.Timeout = config.Duration{Duration: time.Second}
	cfg.AgentClient.PreviewTimeout = config.Duration{Duration: time.Second}
	runtime := newFakeRuntime(t)
	h := sandbox.Handle{ConvID: "conv-1", InstID: "inst-1", Addr: strings.TrimPrefix(runtime.server.URL, "http://"), WSDToken: "token"}
	sb := &fakeSandbox{handle: h}
	quotas := quota.New(cfg.Quota)
	hub := event.NewHub()
	events := event.NewStore(st, hub)
	ac := agentclient.New(cfg.AgentClient)
	fc := &fakeConsumer{consume: consume}
	svc := NewService(st, events, hub, idgen.New(), testCatalog{}, sb, ac, fc, quotas, cfg)
	ctx := context.Background()
	u := &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}
	if err := st.CreateUser(ctx, st.DB(), u); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ConvID: "conv-1", UserID: "alice", Model: "local-demo", Template: "react-go-sqlite", PreviewKey: "preview-key"}
	if err := st.CreateConversation(ctx, st.DB(), conv); err != nil {
		t.Fatal(err)
	}
	f := &runFixture{st: st, cfg: cfg, events: events, hub: hub, quota: quotas, runtime: runtime, sandbox: sb, service: svc, conv: conv}
	t.Cleanup(func() {
		svc.Close()
		_ = st.Close()
	})
	return f
}

func (f *runFixture) createRun(t *testing.T, runID string, status contract.RunStatus, generation int64) *store.Run {
	t.Helper()
	r := &store.Run{RunID: runID, ConvID: f.conv.ConvID, UserID: "alice", RequestID: "request-" + runID, InputHash: "hash-" + runID, Model: "local-demo", Status: status, Generation: generation}
	m := &store.Message{MsgID: "message-" + runID, ConvID: f.conv.ConvID, RunID: runID, Role: 1, Content: "hello"}
	if err := f.st.WithTx(context.Background(), func(tx *store.Tx) error {
		if err := f.st.CreateRun(context.Background(), tx, r); err != nil {
			return err
		}
		return f.st.CreateMessage(context.Background(), tx, m)
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (f *runFixture) waitStatus(t *testing.T, runID string, want contract.RunStatus) *store.Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := f.st.Run(context.Background(), f.st.DB(), runID)
		if err == nil && r.Status == want {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, err := f.st.Run(context.Background(), f.st.DB(), runID)
	t.Fatalf("Run did not reach %s: run=%+v err=%v", want, r, err)
	return nil
}

func (f *runFixture) eventTypes(t *testing.T, runID string) map[string]int {
	t.Helper()
	events, err := f.st.ListEventsAfter(context.Background(), f.conv.ConvID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range events {
		if e.RunID == runID {
			out[e.Type]++
		}
	}
	return out
}

func TestAcceptedRunCompletesAfterRequestContextCancellation(t *testing.T) {
	f := newRunFixture(t, func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error) {
		return consumer.Outcome{Status: "done", Assistant: "completed"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	accepted, err := f.service.Accept(ctx, "alice", f.conv.ConvID, contract.AcceptRequest{ClientRequestID: "request-http", Content: "build it"})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	r := f.waitStatus(t, accepted.RunID, contract.RunFinished)
	if r.Active || !r.StopConfirmed || r.Commit != "commit-1" {
		t.Fatalf("terminal Run=%+v", r)
	}
	assistant, err := f.st.MessageByRunRole(context.Background(), f.st.DB(), accepted.RunID, 2)
	if err != nil || assistant.Content != "completed" {
		t.Fatalf("assistant=%+v err=%v", assistant, err)
	}
	types := f.eventTypes(t, accepted.RunID)
	for _, typ := range []string{"run_accepted", "run_started", "run_finalizing", "run_finished"} {
		if types[typ] != 1 {
			t.Errorf("%s count=%d all=%v", typ, types[typ], types)
		}
	}
	if got := f.quota.Snapshot()["run"].Active; got != 0 {
		t.Fatalf("active Run quota=%d", got)
	}
	if f.runtime.reaps.Load() != 1 || f.runtime.releases.Load() != 1 {
		t.Fatalf("reap=%d gate releases=%d", f.runtime.reaps.Load(), f.runtime.releases.Load())
	}
}

func TestCancelRunningRunFinalizesAndReleasesQuota(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	f := newRunFixture(t, func(ctx context.Context, _ sandbox.Handle, _ *store.Run) (consumer.Outcome, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return consumer.Outcome{Status: "cancelled"}, nil
	})
	accepted, err := f.service.Accept(context.Background(), "alice", f.conv.ConvID, contract.AcceptRequest{ClientRequestID: "request-cancel", Content: "long work"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer was not entered")
	}
	view, err := f.service.Cancel(context.Background(), "alice", f.conv.ConvID, accepted.RunID)
	if err != nil || view.Status != contract.RunCancelling {
		t.Fatalf("cancel view=%+v err=%v", view, err)
	}
	r := f.waitStatus(t, accepted.RunID, contract.RunCancelled)
	if r.Active || !r.StopConfirmed {
		t.Fatalf("cancelled Run=%+v", r)
	}
	if f.runtime.cancels.Load() != 1 {
		t.Fatalf("runtime cancel calls=%d", f.runtime.cancels.Load())
	}
	if got := f.quota.Snapshot()["run"].Active; got != 0 {
		t.Fatalf("active Run quota=%d", got)
	}
	if types := f.eventTypes(t, accepted.RunID); types["run_cancelled"] != 1 || types["run_cancelling"] != 1 {
		t.Fatalf("cancel events=%v", types)
	}
}

func TestRunningRecoveryDoesNotDuplicatePrepareOrStartedEvent(t *testing.T) {
	f := newRunFixture(t, func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error) {
		return consumer.Outcome{Status: "done", Assistant: "recovered"}, nil
	})
	if err := f.st.SetRuntimeSession(context.Background(), f.st.DB(), f.conv.ConvID, "session-1", "epoch-1", 0); err != nil {
		t.Fatal(err)
	}
	r := f.createRun(t, "run-recovery", contract.RunRunning, 3)
	if err := f.st.MarkRunning(context.Background(), f.st.DB(), r.RunID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.st.WithTx(context.Background(), func(tx *store.Tx) error {
		_, err := f.events.AppendTx(context.Background(), tx, event.Control(f.conv.ConvID, r.RunID, "run_started", map[string]any{"status": contract.RunRunning}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.runtime.startStatus.Store(http.StatusConflict)
	if err := f.service.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, r.RunID, contract.RunFinished)
	if f.runtime.prepares.Load() != 0 || f.runtime.starts.Load() != 1 {
		t.Fatalf("prepare=%d start=%d", f.runtime.prepares.Load(), f.runtime.starts.Load())
	}
	if got := f.eventTypes(t, r.RunID)["run_started"]; got != 1 {
		t.Fatalf("run_started count=%d", got)
	}
}

func TestFinalizingRecoverySkipsPrepareAndStart(t *testing.T) {
	f := newRunFixture(t, func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error) {
		t.Fatal("consumer must not run while finalizing")
		return consumer.Outcome{}, nil
	})
	r := f.createRun(t, "run-finalizing", contract.RunRunning, 4)
	if err := f.st.MarkRunning(context.Background(), f.st.DB(), r.RunID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.st.WithTx(context.Background(), func(tx *store.Tx) error {
		ok, err := f.st.TransitionRun(context.Background(), tx, r.RunID, nil, contract.RunFinalizing)
		if err == nil && !ok {
			t.Fatal("Run was not transitioned")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Drive(r.RunID); err != nil {
		t.Fatal(err)
	}
	f.waitStatus(t, r.RunID, contract.RunFinished)
	if f.runtime.prepares.Load() != 0 || f.runtime.starts.Load() != 0 || f.runtime.commits.Load() != 1 {
		t.Fatalf("prepare=%d start=%d commit=%d", f.runtime.prepares.Load(), f.runtime.starts.Load(), f.runtime.commits.Load())
	}
}

func TestMismatchedGateGrantCannotExecuteOrCommit(t *testing.T) {
	f := newRunFixture(t, func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error) {
		t.Fatal("consumer must not run without a gate grant")
		return consumer.Outcome{}, nil
	})
	f.runtime.gateWrong.Store(true)
	r := f.createRun(t, "run-gate-denied", contract.RunQueued, 0)
	if err := f.service.Drive(r.RunID); err != nil {
		t.Fatal(err)
	}
	got := f.waitStatus(t, r.RunID, contract.RunFailed)
	if got.ErrorCode != "GATE_BUSY" || got.Active {
		t.Fatalf("gate-denied Run=%+v", got)
	}
	if f.runtime.prepares.Load() != 0 || f.runtime.starts.Load() != 0 || f.runtime.commits.Load() != 0 {
		t.Fatalf("prepare=%d start=%d commit=%d", f.runtime.prepares.Load(), f.runtime.starts.Load(), f.runtime.commits.Load())
	}
	if got := f.quota.Snapshot()["run"].Active; got != 0 {
		t.Fatalf("active Run quota=%d", got)
	}
}

func TestUnknownAcceptedResponseRetryUsesOriginalRunOnce(t *testing.T) {
	var executions atomic.Int32
	f := newRunFixture(t, func(context.Context, sandbox.Handle, *store.Run) (consumer.Outcome, error) {
		executions.Add(1)
		return consumer.Outcome{Status: "done", Assistant: "executed once"}, nil
	})
	request := contract.AcceptRequest{ClientRequestID: "request-lost-202", Content: "build exactly once"}
	// The commit succeeds, but the caller treats the first 202 as lost and therefore
	// deliberately discards its body before retrying the same idempotency key.
	if _, err := f.service.Accept(context.Background(), "alice", f.conv.ConvID, request); err != nil {
		t.Fatal(err)
	}
	retried, err := f.service.Accept(context.Background(), "alice", f.conv.ConvID, request)
	if err != nil {
		t.Fatal(err)
	}
	var originalRunID string
	if err := f.st.DB().QueryRow(`SELECT Frun_id FROM t_run WHERE Fconv_id=? AND Frequest_id=?`, f.conv.ConvID, request.ClientRequestID).Scan(&originalRunID); err != nil {
		t.Fatal(err)
	}
	if retried.RunID != originalRunID {
		t.Fatalf("retry Run ID=%q, original=%q", retried.RunID, originalRunID)
	}
	view, err := f.service.Get(context.Background(), "alice", f.conv.ConvID, originalRunID)
	if err != nil || view.RunID != originalRunID {
		t.Fatalf("query original Run=%+v err=%v", view, err)
	}
	f.waitStatus(t, originalRunID, contract.RunFinished)
	view, err = f.service.Get(context.Background(), "alice", f.conv.ConvID, originalRunID)
	if err != nil || view.Status != contract.RunFinished {
		t.Fatalf("finished original Run=%+v err=%v", view, err)
	}
	var runCount, messageCount int
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM t_run WHERE Fconv_id=? AND Frequest_id=?`, f.conv.ConvID, request.ClientRequestID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DB().QueryRow(`SELECT COUNT(*) FROM t_message WHERE Frun_id=? AND Frole=1`, originalRunID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || messageCount != 1 || executions.Load() != 1 {
		t.Fatalf("runs=%d user messages=%d executions=%d", runCount, messageCount, executions.Load())
	}
	if f.runtime.prepares.Load() != 1 || f.runtime.starts.Load() != 1 || f.runtime.commits.Load() != 1 {
		t.Fatalf("prepare=%d start=%d commit=%d", f.runtime.prepares.Load(), f.runtime.starts.Load(), f.runtime.commits.Load())
	}
	conversation, err := f.st.Conversation(context.Background(), f.st.DB(), f.conv.ConvID)
	if err != nil || conversation.Title != request.Content {
		t.Fatalf("automatic title=%q err=%v", conversation.Title, err)
	}
}
