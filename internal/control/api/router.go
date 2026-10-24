// Package api exposes the browser-facing HTTP API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/auth"
	"github.com/0226zy/tenon/internal/control/conversation"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/files"
	"github.com/0226zy/tenon/internal/control/llm"
	"github.com/0226zy/tenon/internal/control/preview"
	"github.com/0226zy/tenon/internal/control/run"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/httpx"
	"github.com/0226zy/tenon/pkg/contract"
)

type Dependencies struct {
	Auth          *auth.Service
	Catalog       *llm.Catalog
	Conversations *conversation.Service
	Runs          *run.Service
	Events        *event.Store
	Hub           *event.Hub
	Mapper        event.Mapper
	Files         *files.Service
	Preview       *preview.Service
	Config        *config.Config
}

type Router struct{ deps Dependencies }

type principalKey struct{}

func NewRouter(deps Dependencies) http.Handler {
	r := &Router{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", r.login)
	mux.HandleFunc("POST /api/logout", r.logout)
	mux.Handle("GET /api/me", r.secure(http.HandlerFunc(r.me)))
	mux.Handle("GET /api/models", r.secure(http.HandlerFunc(r.models)))
	mux.Handle("GET /api/conversations", r.secure(http.HandlerFunc(r.listConversations)))
	mux.Handle("POST /api/conversations", r.secure(http.HandlerFunc(r.createConversation)))
	mux.Handle("GET /api/conversations/{conv}", r.secure(http.HandlerFunc(r.getConversation)))
	mux.Handle("PATCH /api/conversations/{conv}", r.secure(http.HandlerFunc(r.updateConversation)))
	mux.Handle("DELETE /api/conversations/{conv}", r.secure(http.HandlerFunc(r.deleteConversation)))
	mux.Handle("GET /api/conversations/{conv}/messages", r.secure(http.HandlerFunc(r.messages)))
	mux.Handle("POST /api/conversations/{conv}/messages", r.secure(http.HandlerFunc(r.accept)))
	mux.Handle("GET /api/conversations/{conv}/runs/{run}", r.secure(http.HandlerFunc(r.getRun)))
	mux.Handle("POST /api/conversations/{conv}/runs/{run}/cancel", r.secure(http.HandlerFunc(r.cancelRun)))
	mux.Handle("GET /api/conversations/{conv}/events/history", r.secure(http.HandlerFunc(r.history)))
	mux.Handle("GET /api/conversations/{conv}/events", r.secure(http.HandlerFunc(r.sse)))
	mux.Handle("GET /api/conversations/{conv}/files/tree", r.secure(http.HandlerFunc(r.fileTree)))
	mux.Handle("GET /api/conversations/{conv}/files/content", r.secure(http.HandlerFunc(r.fileContent)))
	mux.Handle("GET /api/conversations/{conv}/preview", r.secure(http.HandlerFunc(r.previewEntry)))
	mux.Handle("POST /api/conversations/{conv}/preview/restart", r.secure(http.HandlerFunc(r.previewRestart)))
	mux.Handle("GET /api/conversations/{conv}/preview/logs", r.secure(http.HandlerFunc(r.previewLogs)))
	mux.Handle("POST /api/conversations/{conv}/git/push", r.secure(http.HandlerFunc(r.notImplemented)))
	mux.Handle("POST /api/conversations/{conv}/deployments", r.secure(http.HandlerFunc(r.notImplemented)))
	mux.Handle("GET /api/conversations/{conv}/deployments/{deployment}", r.secure(http.HandlerFunc(r.notImplemented)))
	return r.middleware(mux)
}

func (r *Router) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			origin := req.Header.Get("Origin")
			if origin != "" && origin != r.deps.Config.Web.Origin {
				writeError(w, httpx.E(http.StatusForbidden, "CSRF_REJECTED", "request origin is not allowed"))
				return
			}
			if strings.EqualFold(req.Header.Get("Sec-Fetch-Site"), "cross-site") {
				writeError(w, httpx.E(http.StatusForbidden, "CSRF_REJECTED", "cross-site request is not allowed"))
				return
			}
		}
		next.ServeHTTP(w, req)
	})
}

func (r *Router) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, err := r.deps.Auth.Authenticate(req)
		if err != nil {
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), principalKey{}, p)))
	})
}
func principal(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(principalKey{}).(auth.Principal)
	return p
}

func (r *Router) login(w http.ResponseWriter, req *http.Request) {
	var in contract.LoginRequest
	if err := httpx.Decode(w, req, &in); err != nil {
		writeError(w, err)
		return
	}
	raw, exp, err := r.deps.Auth.Login(req.Context(), in.Username, in.Password, auth.RemoteIP(req))
	if err != nil {
		writeError(w, err)
		return
	}
	cfg := r.deps.Config.Auth
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName, Value: raw, Path: "/", HttpOnly: true, Secure: cfg.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: max(1, int(time.Until(exp).Seconds()))})
	w.WriteHeader(http.StatusNoContent)
}
func (r *Router) logout(w http.ResponseWriter, req *http.Request) {
	cfg := r.deps.Config.Auth
	if c, err := req.Cookie(cfg.CookieName); err == nil {
		_ = r.deps.Auth.Logout(req.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cfg.CookieName, Value: "", Path: "/", HttpOnly: true, Secure: cfg.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: time.Unix(1, 0), MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
func (r *Router) me(w http.ResponseWriter, req *http.Request) {
	p := principal(req.Context())
	httpx.JSON(w, http.StatusOK, contract.UserView{UserID: p.UserID, Name: p.Name})
}
func (r *Router) models(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, r.deps.Catalog.List())
}

func (r *Router) createConversation(w http.ResponseWriter, req *http.Request) {
	var in contract.CreateConversationRequest
	if err := httpx.Decode(w, req, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := r.deps.Conversations.Create(req.Context(), principal(req.Context()).UserID, in)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}
func (r *Router) listConversations(w http.ResponseWriter, req *http.Request) {
	limit, err := intQuery(req, "limit", 30, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := r.deps.Conversations.List(req.Context(), principal(req.Context()).UserID, req.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
func (r *Router) getConversation(w http.ResponseWriter, req *http.Request) {
	out, err := r.deps.Conversations.Get(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"))
	writeResult(w, http.StatusOK, out, err)
}
func (r *Router) updateConversation(w http.ResponseWriter, req *http.Request) {
	var in contract.UpdateConversationRequest
	if err := httpx.Decode(w, req, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := r.deps.Conversations.Update(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), in)
	writeResult(w, http.StatusOK, out, err)
}
func (r *Router) deleteConversation(w http.ResponseWriter, req *http.Request) {
	if err := r.deps.Conversations.Delete(req.Context(), principal(req.Context()).UserID, req.PathValue("conv")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (r *Router) messages(w http.ResponseWriter, req *http.Request) {
	limit, err := intQuery(req, "limit", 50, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := r.deps.Conversations.Messages(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), req.URL.Query().Get("before"), limit)
	writeResult(w, http.StatusOK, out, err)
}
func (r *Router) accept(w http.ResponseWriter, req *http.Request) {
	var in contract.AcceptRequest
	if err := httpx.Decode(w, req, &in); err != nil {
		writeError(w, err)
		return
	}
	out, err := r.deps.Runs.Accept(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), in)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}
func (r *Router) getRun(w http.ResponseWriter, req *http.Request) {
	out, err := r.deps.Runs.Get(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), req.PathValue("run"))
	writeResult(w, http.StatusOK, out, err)
}
func (r *Router) cancelRun(w http.ResponseWriter, req *http.Request) {
	out, err := r.deps.Runs.Cancel(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), req.PathValue("run"))
	writeResult(w, http.StatusOK, out, err)
}

func (r *Router) history(w http.ResponseWriter, req *http.Request) {
	userID, convID := principal(req.Context()).UserID, req.PathValue("conv")
	if _, err := r.deps.Conversations.GetOwned(req.Context(), userID, convID); err != nil {
		writeError(w, err)
		return
	}
	limit, err := intQuery(req, "limit", 200, 999)
	if err != nil {
		writeError(w, err)
		return
	}
	query := req.URL.Query()
	if query.Get("after") != "" && query.Get("before") != "" {
		writeError(w, httpx.E(http.StatusBadRequest, "BAD_CURSOR", "after and before are mutually exclusive"))
		return
	}
	out := contract.Page[contract.UIFrame]{Items: []contract.UIFrame{}}
	if query.Get("after") != "" {
		after, err := strconv.ParseInt(query.Get("after"), 10, 64)
		if err != nil || after < 0 {
			writeError(w, httpx.E(http.StatusBadRequest, "BAD_CURSOR", "invalid after cursor"))
			return
		}
		events, err := r.deps.Events.St.ListEventsAfter(req.Context(), convID, after, limit+1)
		if err != nil {
			writeError(w, err)
			return
		}
		more := len(events) > limit
		if more {
			events = events[:limit]
		}
		for _, e := range events {
			if frame, ok := r.deps.Mapper.ToUI(e); ok {
				out.Items = append(out.Items, frame)
			}
		}
		if more && len(events) > 0 {
			out.NextCursor = strconv.FormatInt(events[len(events)-1].Seq, 10)
		}
	} else {
		var before int64
		if query.Get("before") != "" {
			before, err = strconv.ParseInt(query.Get("before"), 10, 64)
			if err != nil || before <= 0 {
				writeError(w, httpx.E(http.StatusBadRequest, "BAD_CURSOR", "invalid before cursor"))
				return
			}
		}
		events, err := r.deps.Events.St.ListEventsBefore(req.Context(), convID, before, limit+1)
		if err != nil {
			writeError(w, err)
			return
		}
		more := len(events) > limit
		if more {
			events = events[1:]
		}
		for _, e := range events {
			if frame, ok := r.deps.Mapper.ToUI(e); ok {
				out.Items = append(out.Items, frame)
			}
		}
		if more && len(events) > 0 {
			out.NextCursor = strconv.FormatInt(events[0].Seq, 10)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (r *Router) sse(w http.ResponseWriter, req *http.Request) {
	userID, convID := principal(req.Context()).UserID, req.PathValue("conv")
	if _, err := r.deps.Conversations.GetOwned(req.Context(), userID, convID); err != nil {
		writeError(w, err)
		return
	}
	var after int64
	var err error
	if value := req.URL.Query().Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
	} else if value := req.Header.Get("Last-Event-ID"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
	}
	if err != nil || after < 0 {
		writeError(w, httpx.E(http.StatusBadRequest, "BAD_CURSOR", "invalid event cursor"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, httpx.E(http.StatusInternalServerError, "INTERNAL", "streaming is unavailable"))
		return
	}
	sub := r.deps.Hub.Subscribe(convID)
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	write := func(name string, id int64, frame contract.UIFrame) error {
		if timeout := r.deps.Config.Event.WriteTimeout.Duration; timeout > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(timeout))
		}
		raw, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if name != "" {
			if _, err = fmt.Fprintf(w, "event: %s\n", name); err != nil {
				return err
			}
		}
		if id > 0 {
			if _, err = fmt.Fprintf(w, "id: %d\n", id); err != nil {
				return err
			}
		}
		if _, err = fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if _, err = fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	flusher.Flush()
	flushPersistent := func() error {
		for {
			events, err := r.deps.Events.St.ListEventsAfter(req.Context(), convID, after, 200)
			if err != nil {
				return err
			}
			if len(events) == 0 {
				return nil
			}
			for _, e := range events {
				after = e.Seq
				if frame, ok := r.deps.Mapper.ToUI(e); ok {
					if err := write("ui", e.Seq, frame); err != nil {
						return err
					}
				}
			}
		}
	}
	if err := flushPersistent(); err != nil {
		return
	}
	heartbeat := time.NewTicker(r.deps.Config.Event.SSEHeartbeat.Duration)
	defer heartbeat.Stop()
	for {
		select {
		case <-req.Context().Done():
			return
		case <-sub.Notify:
			if err := flushPersistent(); err != nil {
				return
			}
		case frame := <-sub.Temp:
			if err := write("temp", 0, frame); err != nil {
				return
			}
		case <-heartbeat.C:
			if timeout := r.deps.Config.Event.WriteTimeout.Duration; timeout > 0 {
				_ = controller.SetWriteDeadline(time.Now().Add(timeout))
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (r *Router) fileTree(w http.ResponseWriter, req *http.Request) {
	out, err := r.deps.Files.Tree(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), req.URL.Query().Get("path"))
	writeResult(w, http.StatusOK, out, err)
}
func (r *Router) fileContent(w http.ResponseWriter, req *http.Request) {
	out, notModified, err := r.deps.Files.Content(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), req.URL.Query().Get("path"), req.Header.Get("If-None-Match"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", out.ETag)
	if notModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
func (r *Router) previewEntry(w http.ResponseWriter, req *http.Request) {
	out, err := r.deps.Preview.Entry(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"))
	writeResult(w, http.StatusOK, out, err)
}
func (r *Router) previewRestart(w http.ResponseWriter, req *http.Request) {
	if err := r.deps.Preview.Restart(req.Context(), principal(req.Context()).UserID, req.PathValue("conv")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (r *Router) previewLogs(w http.ResponseWriter, req *http.Request) {
	tail, err := intQuery(req, "tail", 200, 2000)
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := r.deps.Preview.Logs(req.Context(), principal(req.Context()).UserID, req.PathValue("conv"), tail)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
func (r *Router) notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, httpx.E(http.StatusNotImplemented, "NOT_IMPLEMENTED", "即将开放"))
}

func intQuery(req *http.Request, name string, fallback, maximum int) (int, error) {
	value := req.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 || n > maximum {
		return 0, httpx.E(http.StatusBadRequest, "BAD_REQUEST", "invalid "+name)
	}
	return n, nil
}
func writeResult(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, status, value)
}
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		err = httpx.E(http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, store.ErrActiveRunExists), errors.Is(err, store.ErrConflict):
		err = httpx.E(http.StatusConflict, "RUN_IN_PROGRESS", "conversation has an active Run")
	case errors.Is(err, store.ErrDuplicate):
		err = httpx.E(http.StatusConflict, "CONFLICT", "resource already exists")
	case errors.Is(err, sandbox.ErrDriverNotConfigured):
		err = httpx.E(http.StatusServiceUnavailable, "SANDBOX_NOT_CONFIGURED", "sandbox driver is not configured")
	}
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		status := ae.Status
		if status < 400 || status > 599 {
			status = http.StatusServiceUnavailable
		}
		code := ae.Code
		if code == "" {
			code = "SANDBOX_UNAVAILABLE"
		}
		err = httpx.E(status, code, "sandbox request failed")
	}
	httpx.WriteError(w, err)
}
