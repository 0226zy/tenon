// Package session 实现会话管理与 append-only 事件日志的内存运行时。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/tenon/agent-runtime/internal/application/agent"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/derr"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/port"
	"golang.org/x/sync/singleflight"
)

// Manager 管理全部会话运行时。
type Manager struct {
	store           port.SessionStorePort
	loop            *agent.Loop
	cwdOf           func(sessionID string) string
	spillDir        string
	approvalTimeout time.Duration
	cwdGuard        func(cwd string) error
	compression     compressionConfig

	mu          sync.Mutex
	runtimes    map[string]*Runtime
	defaultMode string
	loadGroup   singleflight.Group
}

func NewManager(store port.SessionStorePort, loop *agent.Loop, cwdOf func(sessionID string) string, spillDir string, approvalTimeout time.Duration) *Manager {
	return &Manager{store: store, loop: loop, cwdOf: cwdOf, spillDir: spillDir,
		approvalTimeout: approvalTimeout, runtimes: map[string]*Runtime{}, defaultMode: "full-auto"}
}

func (m *Manager) SetCwdGuard(guard func(cwd string) error) { m.cwdGuard = guard }

// SetDefaultMode 设置后续创建会话时空 mode 的默认值。
func (m *Manager) SetDefaultMode(mode string) {
	switch mode {
	case "suggest", "auto-edit", "full-auto":
	default:
		return
	}
	m.mu.Lock()
	m.defaultMode = mode
	m.mu.Unlock()
}

func (m *Manager) DefaultMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultMode
}

// ── 会话生命周期 ─────────────────────────────────────────────────

func (m *Manager) Create(cwd, mode, title, model string) (*Runtime, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	if m.cwdGuard != nil {
		if err := m.cwdGuard(cwd); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return nil, fmt.Errorf("工作区目录不可创建: %w", err)
	}
	if mode == "" {
		mode = m.DefaultMode()
	}
	header := entity.SessionHeader{ID: newID("s"), Title: title, Cwd: cwd, Mode: mode, Model: model}
	if err := m.store.Create(header); err != nil {
		return nil, err
	}
	return m.load(header.ID)
}

func (m *Manager) Get(id string) (*Runtime, error)       { return m.load(id) }
func (m *Manager) List() ([]entity.SessionHeader, error) { return m.store.List() }

func (m *Manager) Fork(id string, atSeq int) (*Runtime, error) {
	header, err := m.store.Fork(id, newID("s"), atSeq)
	if err != nil {
		return nil, err
	}
	return m.load(header.ID)
}

// Events 返回 fromSeq 起的全部持久事件，供旧调用方与完整回放使用。
func (m *Manager) Events(id string, fromSeq int) ([]entity.Event, error) {
	rt, err := m.load(id)
	if err != nil {
		return nil, err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	events, _, err := eventsFrom(rt.events, fromSeq, 0)
	return events, err
}

// EventsPage 返回一页持久事件及下一游标；limit 默认 500，最大 1000。
func (m *Manager) EventsPage(id string, fromSeq, limit int) ([]entity.Event, int, error) {
	rt, err := m.load(id)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	page, _, err := eventsFrom(rt.events, fromSeq, limit)
	if err != nil {
		return nil, 0, err
	}
	next := fromSeq
	if next < 0 {
		next = 0
	}
	if len(page) > 0 {
		next = page[len(page)-1].Seq + 1
	}
	return page, next, nil
}

func eventsFrom(events []entity.Event, fromSeq, limit int) ([]entity.Event, int, error) {
	if fromSeq < 0 {
		fromSeq = 0
	}
	out := make([]entity.Event, 0)
	for _, ev := range events {
		if ev.Seq < fromSeq {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	next := fromSeq
	if len(out) > 0 {
		next = out[len(out)-1].Seq + 1
	}
	return out, next, nil
}

func (m *Manager) NextSeq(id string) (int, error) {
	rt, err := m.load(id)
	if err != nil {
		return 0, err
	}
	return rt.NextSeq(), nil
}

func (m *Manager) Search(id, query string) ([]entity.Event, error) {
	rt, err := m.load(id)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var hits []entity.Event
	for _, ev := range rt.events {
		if ev.Message != nil {
			if strings.Contains(strings.ToLower(ev.Message.Content), q) {
				hits = append(hits, ev)
				continue
			}
			matched := false
			for _, tc := range ev.Message.ToolCalls {
				if strings.Contains(strings.ToLower(tc.Function.Name+" "+tc.Function.Arguments), q) {
					matched = true
					break
				}
			}
			if matched {
				hits = append(hits, ev)
			}
		} else if strings.Contains(strings.ToLower(ev.Note), q) {
			hits = append(hits, ev)
		}
	}
	return hits, nil
}

func validMode(mode string) bool {
	return mode == "suggest" || mode == "auto-edit" || mode == "full-auto"
}

func (m *Manager) SetMode(id, mode string) error {
	if !validMode(mode) {
		return fmt.Errorf("非法审批模式 %q（可选: suggest / auto-edit / full-auto）", mode)
	}
	rt, err := m.load(id)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	header := rt.header
	header.Mode = mode
	if err := m.store.UpdateHeader(header); err != nil {
		return err
	}
	rt.header = header
	return nil
}

// SetModel 切换下一 Run 使用的模型；运行中拒绝，防止回合内解析结果不稳定。
func (m *Manager) SetModel(id, model string) error {
	rt, err := m.load(id)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.running {
		return derr.ErrSessionBusy
	}
	header := rt.header
	header.Model = model
	if err := m.store.UpdateHeader(header); err != nil {
		return err
	}
	rt.header = header
	return nil
}

func (m *Manager) ModeOf(id string) string {
	rt, err := m.load(id)
	if err != nil {
		return ""
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.header.Mode
}

func (m *Manager) HeaderOf(id string) (entity.SessionHeader, error) {
	rt, err := m.load(id)
	if err != nil {
		return entity.SessionHeader{}, err
	}
	return rt.Header(), nil
}

func (m *Manager) load(id string) (*Runtime, error) {
	m.mu.Lock()
	rt := m.runtimes[id]
	m.mu.Unlock()
	if rt != nil {
		return rt, nil
	}
	value, err, _ := m.loadGroup.Do(id, func() (any, error) {
		m.mu.Lock()
		existing := m.runtimes[id]
		m.mu.Unlock()
		if existing != nil {
			return existing, nil
		}
		header, events, err := m.store.Load(id)
		if err != nil {
			return nil, err
		}
		rt := &Runtime{header: header, events: events, manager: m,
			subs: map[chan entity.Event]struct{}{}, pending: map[string]chan entity.ApprovalDecision{},
			compressCache: newCompressCache()}
		for i := len(events) - 1; i >= 0; i-- {
			ev := events[i]
			if rt.lastPrompt == 0 && ev.Marker != nil && ev.Marker.Type == "progress" && ev.Marker.PromptTokens > 0 {
				rt.lastPrompt = ev.Marker.PromptTokens
			}
			if ev.Kind == entity.EventNotice && strings.Contains(ev.Note, "上下文投影压缩已激活") {
				rt.compressNoticed = true
			}
		}
		m.mu.Lock()
		m.runtimes[id] = rt
		m.mu.Unlock()
		return rt, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(*Runtime), nil
}

// ── 回合控制 ─────────────────────────────────────────────────────

// startTurn 启动唯一回合。done 在 Loop.RunTurn 已返回且 turn marker 已写入后调用；
// RunService 在回调中清理进程组并做 stopped_confirmed 判定。
func (m *Manager) startTurn(ctx context.Context, id, runID, userText string, done func(agent.TurnResult, error)) error {
	rt, err := m.load(id)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	if rt.running {
		rt.mu.Unlock()
		return derr.ErrSessionBusy
	}
	rt.running = true
	rt.activeRunID = runID
	rt.runID = runID
	turnCtx, cancel := context.WithCancel(ctx)
	rt.cancel = cancel
	rt.mu.Unlock()

	go func() {
		result, turnErr := m.loop.RunTurn(turnCtx, rt, userText)
		marker := &entity.Marker{Type: "turn_done", RunID: runID, Iterations: result.Iterations,
			PromptTokens: result.PromptTokens, CompletionTokens: result.CompletionTokens,
			Model: result.Model, ErrorCode: result.ErrorCode}
		if turnErr != nil {
			marker.Type = "turn_failed"
			marker.Detail = turnErr.Error()
			marker.ErrorCode = agent.ErrorCodeOf(turnErr)
		}
		if _, appendErr := rt.AppendEvent(entity.Event{RunID: runID, Kind: entity.EventMarker, Marker: marker}); appendErr != nil && turnErr == nil {
			turnErr = appendErr
		}
		rt.mu.Lock()
		rt.running = false
		rt.activeRunID = ""
		rt.runID = ""
		rt.cancel = nil
		rt.mu.Unlock()
		if done != nil {
			done(result, turnErr)
		}
	}()
	return nil
}

// RunTurn 是旧 messages API 的兼容入口；新控制面应使用 RunService。
func (m *Manager) RunTurn(id, userText string) error {
	return m.startTurn(context.Background(), id, newID("run"), userText, nil)
}

func (m *Manager) Cancel(id string) {
	rt, err := m.load(id)
	if err != nil {
		return
	}
	rt.mu.Lock()
	if rt.cancel != nil {
		rt.cancel()
	}
	rt.mu.Unlock()
}

func (m *Manager) ResolveApproval(id, approvalID string, approved bool) error {
	rt, err := m.load(id)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	ch, ok := rt.pending[approvalID]
	rt.mu.Unlock()
	if !ok {
		return derr.ErrNoPendingApproval
	}
	ch <- entity.ApprovalDecision{Approved: approved, By: "human"}
	return nil
}

// Subscribe 订阅实时事件。256 条缓冲满时删除并关闭订阅，迫使 SSE 断开后分页补齐。
func (m *Manager) Subscribe(id string) (<-chan entity.Event, func(), error) {
	rt, err := m.load(id)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan entity.Event, 256)
	rt.mu.Lock()
	rt.subs[ch] = struct{}{}
	rt.mu.Unlock()
	unsub := func() {
		rt.mu.Lock()
		if _, ok := rt.subs[ch]; ok {
			delete(rt.subs, ch)
			close(ch)
		}
		rt.mu.Unlock()
	}
	return ch, unsub, nil
}

// Snapshot 是会话对外视图的原子快照。
type Snapshot struct {
	Header      entity.SessionHeader
	Running     bool
	ActiveRunID string
	NextSeq     int
	Tokens      int
}

func (m *Manager) SessionSnapshot(id string) (Snapshot, error) {
	rt, err := m.load(id)
	if err != nil {
		return Snapshot{}, err
	}
	return rt.SessionSnapshot(), nil
}

// Runtime 是 port.SessionRuntime 的实现。
type Runtime struct {
	header  entity.SessionHeader
	events  []entity.Event
	manager *Manager

	appendMu    sync.Mutex
	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	runID       string
	activeRunID string
	tokens      int
	lastPrompt  int
	subs        map[chan entity.Event]struct{}
	pending     map[string]chan entity.ApprovalDecision

	compressCache   map[int]string
	compressNoticed bool
}

func (r *Runtime) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header.ID
}
func (r *Runtime) RunID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runID
}
func (r *Runtime) Model() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header.Model
}
func (r *Runtime) Mode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header.Mode
}
func (r *Runtime) Cwd() string {
	r.mu.Lock()
	id, cwd := r.header.ID, r.header.Cwd
	r.mu.Unlock()
	if r.manager.cwdOf != nil {
		if current := r.manager.cwdOf(id); current != "" {
			return current
		}
	}
	return cwd
}
func (r *Runtime) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}
func (r *Runtime) ActiveRunID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.activeRunID
}
func (r *Runtime) NextSeq() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return nextSeq(r.events)
}
func nextSeq(events []entity.Event) int {
	if len(events) == 0 {
		return 0
	}
	return events[len(events)-1].Seq + 1
}
func (r *Runtime) Header() entity.SessionHeader {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header
}

// Snapshot 加锁读取模型与审批模式，供工具钩子获得一致视图。
func (r *Runtime) Snapshot() (model, mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.header.Model, r.header.Mode
}

func (r *Runtime) SessionSnapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Snapshot{Header: r.header, Running: r.running, ActiveRunID: r.activeRunID,
		NextSeq: nextSeq(r.events), Tokens: r.tokens}
}

// AppendEvent 严格按 store.Append → 内存 append → 非阻塞广播排序。
func (r *Runtime) AppendEvent(ev entity.Event) (entity.Event, error) {
	r.appendMu.Lock()
	defer r.appendMu.Unlock()
	r.mu.Lock()
	if ev.RunID == "" {
		ev.RunID = r.runID
	}
	if ev.RunID != r.runID {
		activeRunID := r.runID
		r.mu.Unlock()
		return entity.Event{}, fmt.Errorf("拒绝写入非活动 Run %q 的延迟事件（当前 Run %q）", ev.RunID, activeRunID)
	}
	if ev.Marker != nil && ev.Marker.RunID == "" {
		ev.Marker.RunID = ev.RunID
	}
	id := r.header.ID
	r.mu.Unlock()
	persisted, err := r.manager.store.Append(id, ev)
	if err != nil {
		return persisted, err
	}
	r.mu.Lock()
	r.events = append(r.events, persisted)
	broadcastLocked(r.subs, persisted)
	r.mu.Unlock()
	return persisted, nil
}

// EmitTemp 只广播无序号的临时事件，不写 JSONL。
func (r *Runtime) EmitTemp(ev entity.Event) {
	ev.Seq = -1
	ev.TS = time.Now()
	r.mu.Lock()
	if ev.RunID == "" {
		ev.RunID = r.runID
	}
	broadcastLocked(r.subs, ev)
	r.mu.Unlock()
}

func broadcastLocked(subs map[chan entity.Event]struct{}, ev entity.Event) {
	for ch := range subs {
		select {
		case ch <- ev:
		default:
			delete(subs, ch)
			close(ch)
		}
	}
}

func (r *Runtime) Messages() []entity.Message {
	r.mu.Lock()
	events := append([]entity.Event(nil), r.events...)
	r.mu.Unlock()
	var indexed []indexedMessage
	for _, ev := range events {
		switch {
		case ev.Message != nil:
			indexed = append(indexed, indexedMessage{seq: ev.Seq, msg: *ev.Message})
		case ev.Kind == entity.EventNotice:
			indexed = append(indexed, indexedMessage{seq: ev.Seq, msg: entity.Message{Role: "system", Content: ev.Note}})
		}
	}
	if r.compressActive() {
		r.compressIndexed(indexed)
	}
	raw := make([]entity.Message, len(indexed))
	for i := range indexed {
		raw[i] = indexed[i].msg
	}
	var out []entity.Message
	for i := 0; i < len(raw); i++ {
		m := raw[i]
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		pending := map[string]string{}
		for _, tc := range m.ToolCalls {
			pending[tc.ID] = tc.Function.Name
		}
		j := i + 1
		for j < len(raw) && raw[j].Role == "tool" {
			out = append(out, raw[j])
			delete(pending, raw[j].ToolCallID)
			j++
		}
		for id, name := range pending {
			out = append(out, entity.Message{Role: "tool", ToolCallID: id, Name: name,
				Content: "（该工具调用未产生结果：回合在此中断，或会话从更早的事件点 fork。请勿假设其执行成功。）"})
		}
		i = j - 1
	}
	return out
}

// AddTokens 只维护会话统计；压缩由 ObservePrompt 独立驱动。
func (r *Runtime) AddTokens(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens += n
	return r.tokens
}

// ObservePrompt 记录最近一次请求的 prompt tokens，供下一次上下文投影判定。
func (r *Runtime) ObservePrompt(promptTokens int) {
	if promptTokens < 0 {
		return
	}
	r.mu.Lock()
	r.lastPrompt = promptTokens
	r.mu.Unlock()
}

func (r *Runtime) Approve(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision {
	id := newID("appr")
	ch := make(chan entity.ApprovalDecision, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	_, _ = r.AppendEvent(entity.Event{Kind: entity.EventApproval, Approval: &entity.ApprovalRecord{
		ID: id, ToolName: toolName, Summary: summary, Detail: detail, By: "pending"}})
	finish := func(d entity.ApprovalDecision) entity.ApprovalDecision {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		_, _ = r.AppendEvent(entity.Event{Kind: entity.EventApproval, Approval: &entity.ApprovalRecord{
			ID: id, ToolName: toolName, Summary: summary, Approved: d.Approved, By: d.By}})
		return d
	}
	timer := time.NewTimer(r.manager.approvalTimeout)
	defer timer.Stop()
	select {
	case d := <-ch:
		return finish(d)
	case <-timer.C:
		return finish(entity.ApprovalDecision{Approved: false, By: "timeout", Reason: "审批超时（默认拒绝）"})
	case <-ctx.Done():
		return finish(entity.ApprovalDecision{Approved: false, By: "cancelled", Reason: "回合已取消"})
	}
}

func (r *Runtime) Spill(toolName, content string) string {
	dir := filepath.Join(r.manager.spillDir, r.ID())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "(spill 落盘失败: " + err.Error() + ")"
	}
	p := filepath.Join(dir, fmt.Sprintf("%s-%d.log", toolName, time.Now().UnixNano()))
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "(spill 落盘失败: " + err.Error() + ")"
	}
	return p
}

func newID(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%s_%s%s", prefix, time.Now().Format("20060102"), hex.EncodeToString(buf))
}
