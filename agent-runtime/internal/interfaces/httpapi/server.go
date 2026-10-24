// Package httpapi 是 agent-runtime 的服务端协议适配层（interfaces）：
// REST + SSE，对齐 dsh web 形态——模型、会话、工具全部经 HTTP 暴露，
// 审批通过 API 裁决，事件流经 SSE 实时推送。
package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/application/project"
	"github.com/0226zy/proto-platform/agent-runtime/internal/application/session"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/derr"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/plugin"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

const maxRequestBodyBytes = 1 << 20

// Version 是 agent-runtime 对外报告的服务版本，可在发布构建时通过 ldflags 覆盖。
var Version = "dev"

// ServerOptions 控制 HTTP 暴露面；空 AuthToken 仅适用于本机开发。
type ServerOptions struct {
	AuthToken       string
	DisableModelAPI bool
}

// Server 是 HTTP API 处理器集合。
type Server struct {
	manager   *session.Manager
	runs      *session.RunService
	registry  *plugin.Registry
	models    port.LLMInfoRepo
	orch      *project.Orchestrator
	authToken string
	webUI     http.Handler
	mux       *http.ServeMux

	// tickets 是一次性短期凭证（60s，消费即焚）：EventSource 不能自定义请求头，
	// 用 ticket 替代 URL 里的长期 token，降低凭证进日志/Referer 的泄露面（评审 P0-3）。
	ticketsMu sync.Mutex
	tickets   map[string]time.Time
}

// NewServer 装配 REST、持久 Run 与 SSE 路由。
func NewServer(manager *session.Manager, runs *session.RunService, registry *plugin.Registry, models port.LLMInfoRepo, orch *project.Orchestrator, opts ServerOptions, webUI http.Handler) *Server {
	if webUI == nil {
		webUI = http.NotFoundHandler()
	}
	s := &Server{manager: manager, runs: runs, registry: registry, models: models, orch: orch,
		authToken: opts.AuthToken, webUI: webUI, tickets: map[string]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /v1/plugins", s.plugins)
	if !opts.DisableModelAPI {
		mux.HandleFunc("GET /v1/models", s.listModels)
		mux.HandleFunc("POST /v1/models", s.saveModel)
		mux.HandleFunc("DELETE /v1/models/{name}", s.deleteModel)
	}
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /v1/sessions/{id}/events", s.events)
	mux.HandleFunc("GET /v1/sessions/{id}/search", s.search)
	mux.HandleFunc("POST /v1/sessions/{id}/fork", s.fork)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", s.postMessage)
	mux.HandleFunc("POST /v1/sessions/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/sessions/{id}/mode", s.setMode)
	mux.HandleFunc("POST /v1/sessions/{id}/model", s.setSessionModel)
	mux.HandleFunc("GET /v1/sessions/{id}/runs", s.listRuns)
	mux.HandleFunc("POST /v1/sessions/{id}/runs", s.prepareRun)
	mux.HandleFunc("GET /v1/sessions/{id}/runs/{runID}", s.getRun)
	mux.HandleFunc("POST /v1/sessions/{id}/runs/{runID}/start", s.startRun)
	mux.HandleFunc("POST /v1/sessions/{id}/runs/{runID}/cancel", s.cancelRun)
	mux.HandleFunc("POST /v1/sessions/{id}/approvals/{approvalID}", s.resolveApproval)
	mux.HandleFunc("GET /v1/sessions/{id}/stream", s.stream)
	mux.HandleFunc("POST /v1/tickets", s.issueTicket)
	mux.HandleFunc("POST /v1/projects/plan", s.startPlan)
	mux.HandleFunc("GET /v1/projects/plans/{id}", s.getPlan)
	mux.Handle("GET /", webUI)
	s.mux = mux
	return s
}

// Handler 返回挂好鉴权与请求体上限的根 handler。
func (s *Server) Handler() http.Handler {
	return s.auth(s.limitBody(s.mux))
}

// limitBody 在鉴权后缓冲至多 1 MiB 的 API 请求体，拒绝无 Content-Length 的超大流。
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > maxRequestBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes+1))
		_ = r.Body.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(raw) > maxRequestBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authToken == "" || r.URL.Path == "/healthz" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		// SSE 的 EventSource 不能自定义请求头：优先一次性 ticket，兼容 ?token=（旧客户端）。
		var token string
		authorization := r.Header.Get("Authorization")
		if len(authorization) >= len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
			token = authorization[len("Bearer "):]
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) != 1 {
			if strings.HasPrefix(r.URL.Path, "/v1/sessions/") && strings.HasSuffix(r.URL.Path, "/stream") &&
				s.redeemTicket(r.URL.Query().Get("ticket")) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// issueTicket 签发一次性短期 ticket（60s 有效，消费即焚），供 EventSource 连接 SSE。
func (s *Server) issueTicket(w http.ResponseWriter, r *http.Request) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ticket := hex.EncodeToString(buf)
	s.ticketsMu.Lock()
	// 惰性清理过期 ticket
	now := time.Now()
	for t, exp := range s.tickets {
		if now.After(exp) {
			delete(s.tickets, t)
		}
	}
	s.tickets[ticket] = now.Add(60 * time.Second)
	s.ticketsMu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"ticket": ticket, "expires_in": 60})
}

// redeemTicket 兑换 ticket：一次性，过期无效。
func (s *Server) redeemTicket(ticket string) bool {
	if ticket == "" {
		return false
	}
	s.ticketsMu.Lock()
	defer s.ticketsMu.Unlock()
	exp, ok := s.tickets[ticket]
	if !ok {
		return false
	}
	delete(s.tickets, ticket)
	return time.Now().Before(exp)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": Version, "api": "agent-runtime/1", "status": "ok",
	})
}

func (s *Server) plugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"plugins": s.registry.List()})
}

// modelView 是模型信息的对外视图：绝不暴露字面 api_key（保护主人的私密信息，
// 甚至不泄露其是否存在——只告知密钥来源形式）。
func modelView(m entity.LLMInfo) map[string]any {
	keySource := "none"
	switch {
	case m.APIKeyEnv != "":
		keySource = "env:" + m.APIKeyEnv
	case m.APIKey != "":
		keySource = "literal"
	}
	return map[string]any{
		"name": m.Name, "provider": m.Provider, "base_url": m.BaseURL,
		"model": m.Model, "temperature": m.Temperature, "max_tokens": m.MaxTokens,
		"timeout_seconds": m.TimeoutSeconds, "default": m.Default, "builtin": m.Builtin,
		"api_key_source": keySource,
	}
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.models.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]map[string]any, 0, len(models))
	for _, m := range models {
		views = append(views, modelView(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": views})
}

// saveModel 新增/更新模型信息（用户自定义模型的写入口；DB 仓储后同一接口）。
func (s *Server) saveModel(w http.ResponseWriter, r *http.Request) {
	var profile entity.LLMInfo
	if err := decode(r, &profile); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.models.Save(profile); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.models.Get(profile.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, modelView(saved))
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	if err := s.models.Delete(r.PathValue("name")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

type createSessionRequest struct {
	Cwd   string `json:"cwd"`
	Mode  string `json:"mode"`
	Title string `json:"title"`
	Model string `json:"model"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rt, err := s.manager.Create(req.Cwd, req.Mode, req.Title, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sessionView(rt))
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	headers, err := s.manager.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]map[string]any, 0, len(headers))
	for _, h := range headers {
		if rt, err := s.manager.Get(h.ID); err == nil {
			views = append(views, sessionView(rt))
			continue
		}
		// 单个会话加载失败时保留原有清单容错行为，并补齐稳定的视图字段。
		views = append(views, map[string]any{
			"id": h.ID, "title": h.Title, "cwd": h.Cwd, "mode": h.Mode, "model": h.Model,
			"created_at": h.CreatedAt, "parent_id": h.ParentID,
			"inherited_event_count": h.InheritedEventCount, "running": false,
			"epoch": h.Epoch, "next_seq": 0, "active_run_id": "",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	rt, err := s.manager.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionView(rt))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fromSeq := 0
	if v := r.URL.Query().Get("from_seq"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			fromSeq = n
		}
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	events, nextSeq, err := s.manager.EventsPage(r.PathValue("id"), fromSeq, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "next_seq": nextSeq})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "缺少查询参数 q")
		return
	}
	hits, err := s.manager.Search(r.PathValue("id"), q)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": hits})
}

type forkRequest struct {
	AtSeq int `json:"at_seq"` // 省略或 -1 表示当前末尾
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) {
	var req forkRequest
	req.AtSeq = -1
	_ = decode(r, &req)
	rt, err := s.manager.Fork(r.PathValue("id"), req.AtSeq)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sessionView(rt))
}

type postMessageRequest struct {
	Content string `json:"content"`
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	var req postMessageRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "content 不能为空")
		return
	}
	if err := s.manager.RunTurn(r.PathValue("id"), req.Content); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "running"})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	snapshot, err := s.manager.SessionSnapshot(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if s.runs != nil && snapshot.ActiveRunID != "" {
		if _, err := s.runs.Cancel(id, snapshot.ActiveRunID); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		s.manager.Cancel(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelling"})
}

type modelRequest struct {
	Model string `json:"model"`
}

func (s *Server) setSessionModel(w http.ResponseWriter, r *http.Request) {
	var req modelRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	modelName := req.Model
	if s.runs != nil {
		var err error
		modelName, err = s.runs.SetModel(id, req.Model)
		if err != nil {
			writeErr(w, err)
			return
		}
	} else {
		if s.models != nil {
			profile, err := s.models.Get(req.Model)
			if err != nil {
				writeErr(w, fmt.Errorf("%w: %v", derr.ErrModelNotFound, err))
				return
			}
			modelName = profile.Name
		}
		if err := s.manager.SetModel(id, modelName); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": modelName})
}

func (s *Server) requireRuns(w http.ResponseWriter) bool {
	if s.runs != nil {
		return true
	}
	writeError(w, http.StatusNotFound, "Run API 未启用")
	return false
}

func (s *Server) prepareRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	var req entity.PrepareRun
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	state, created, err := s.runs.Prepare(r.PathValue("id"), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, state)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	state, transitioned, err := s.runs.StartWithTransition(r.PathValue("id"), r.PathValue("runID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusOK
	if transitioned {
		status = http.StatusAccepted
	}
	writeJSON(w, status, state)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	state, err := s.runs.Get(r.PathValue("id"), r.PathValue("runID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	states, err := s.runs.List(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": states})
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireRuns(w) {
		return
	}
	state, err := s.runs.Cancel(r.PathValue("id"), r.PathValue("runID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, state)
}

type modeRequest struct {
	Mode string `json:"mode"`
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	var req modeRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.manager.SetMode(r.PathValue("id"), req.Mode); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": req.Mode})
}

type approvalRequest struct {
	Approved bool `json:"approved"`
}

func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	var req approvalRequest
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.manager.ResolveApproval(r.PathValue("id"), r.PathValue("approvalID"), req.Approved); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "resolved"})
}

// stream 是 SSE 实时事件流：每追加一条会话事件推送一次。
// 游标（评审 P0-2）：?from_seq=N 或 SSE 标准 Last-Event-ID 头（浏览器自动重连时携带），
// 服务端补发 seq ≥ 游标的历史事件后再接实时流——先订阅后读历史，交集按 seq 去重，
// 消除「先 GET /events 再连 stream」之间的丢事件窗口。
// 每条事件带 `id: <seq>` 行，EventSource 重连时自动携带 Last-Event-ID。
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fromSeq := 0
	if v := r.URL.Query().Get("from_seq"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			fromSeq = n
		}
	} else if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			fromSeq = n + 1
		}
	}

	// 顺序关键：先订阅实时流，再读历史——两者之间到达的事件会同时出现在
	// history 与 ch 中，由 sent 去重；反过来（先读后订阅）则会漏掉窗口内事件。
	ch, unsub, err := s.manager.Subscribe(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer unsub()
	history, err := s.manager.Events(id, fromSeq)
	if err != nil {
		writeErr(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	sent := map[int]bool{}
	send := func(ev entity.Event) bool { // false = 连接已断
		raw, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		if ev.IsDelta() {
			// 临时 delta 没有持久游标：绝不发送 id，也不进入按 seq 的去重表。
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, raw); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		if ev.Seq < fromSeq || sent[ev.Seq] {
			return true
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, raw); err != nil {
			return false
		}
		flusher.Flush()
		sent[ev.Seq] = true
		return true
	}
	for _, ev := range history {
		if !send(ev) {
			return
		}
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !send(ev) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ──  helpers ────────────────────────────────────────────────────

// startPlan 发起「规划 → 编码」编排（规划层用例；异步，202）。
func (s *Server) startPlan(w http.ResponseWriter, r *http.Request) {
	if s.orch == nil {
		writeError(w, http.StatusNotFound, "规划编排未启用")
		return
	}
	var req project.Request
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := s.orch.Start(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, plan)
}

// getPlan 查询编排状态（按规划会话 ID）。
func (s *Server) getPlan(w http.ResponseWriter, r *http.Request) {
	if s.orch == nil {
		writeError(w, http.StatusNotFound, "规划编排未启用")
		return
	}
	plan, ok := s.orch.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "编排不存在（可能已重启，编排状态不持久化）")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func sessionView(rt *session.Runtime) map[string]any {
	snapshot := rt.SessionSnapshot()
	h := snapshot.Header
	return map[string]any{
		"id": h.ID, "title": h.Title, "cwd": h.Cwd, "mode": h.Mode, "model": h.Model,
		"created_at": h.CreatedAt, "parent_id": h.ParentID,
		"inherited_event_count": h.InheritedEventCount, "running": snapshot.Running,
		"epoch": h.Epoch, "next_seq": snapshot.NextSeq, "active_run_id": snapshot.ActiveRunID,
	}
}

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && err.Error() != "EOF" {
		return fmt.Errorf("请求体不是合法 JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeErrorCode(w, status, httpErrorCode(status), msg)
}

func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": msg, "code": code})
}

func httpErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusRequestEntityTooLarge:
		return "REQUEST_TOO_LARGE"
	default:
		return "INTERNAL_ERROR"
	}
}

// writeErr 按哨兵错误映射 HTTP 状态码与稳定机器码（跨层判断用 errors.Is）。
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, derr.ErrSessionNotFound):
		writeErrorCode(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error())
	case errors.Is(err, derr.ErrRunNotFound):
		writeErrorCode(w, http.StatusNotFound, "RUN_NOT_FOUND", err.Error())
	case errors.Is(err, derr.ErrSessionBusy):
		writeErrorCode(w, http.StatusConflict, "SESSION_BUSY", err.Error())
	case errors.Is(err, derr.ErrRunIDConflict):
		writeErrorCode(w, http.StatusConflict, "RUN_ID_CONFLICT", err.Error())
	case errors.Is(err, derr.ErrStaleGeneration):
		writeErrorCode(w, http.StatusConflict, "STALE_GENERATION", err.Error())
	case errors.Is(err, derr.ErrModelNotFound):
		writeErrorCode(w, http.StatusBadRequest, "BAD_MODEL", err.Error())
	case errors.Is(err, derr.ErrInvalidRun):
		writeErrorCode(w, http.StatusBadRequest, "INVALID_RUN", err.Error())
	case errors.Is(err, derr.ErrNoPendingApproval):
		writeErrorCode(w, http.StatusNotFound, "NO_PENDING_APPROVAL", err.Error())
	case errors.Is(err, derr.ErrForkPointInvalid):
		writeErrorCode(w, http.StatusBadRequest, "FORK_POINT_INVALID", err.Error())
	default:
		writeErrorCode(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}
