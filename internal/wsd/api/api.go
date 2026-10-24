// Package api exposes the authenticated wsd HTTP contract.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0226zy/proto-platform/internal/wsd/bus"
	"github.com/0226zy/proto-platform/internal/wsd/disk"
	workspacefs "github.com/0226zy/proto-platform/internal/wsd/fs"
	"github.com/0226zy/proto-platform/internal/wsd/gate"
	gitservice "github.com/0226zy/proto-platform/internal/wsd/git"
	"github.com/0226zy/proto-platform/internal/wsd/preview"
	"github.com/0226zy/proto-platform/internal/wsd/proc"
	"github.com/0226zy/proto-platform/pkg/contract"
)

type Deps struct {
	Token     string
	SandboxID string
	Agent     http.Handler
	Gate      *gate.Gate
	Proc      *proc.Registry
	FS        *workspacefs.Service
	Preview   *preview.Supervisor
	Git       *gitservice.Service
	Bus       *bus.Bus
	Health    func() contract.Health
	DiskUsage func() (disk.Usage, error)
	Logger    *slog.Logger
}

func NewServer(d Deps) *http.Server {
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{}))
	}
	mux := http.NewServeMux()
	handle := func(pattern string, fn func(http.ResponseWriter, *http.Request)) {
		mux.HandleFunc(pattern, fn)
	}
	handle("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		health := contract.Health{API: contract.WSDAPI, OK: true}
		if d.Health != nil {
			health = d.Health()
		}
		status := http.StatusOK
		if !health.OK {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, health)
	})
	handle("POST /gate/acquire", func(w http.ResponseWriter, r *http.Request) {
		var req contract.GateAcquireRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if d.Gate == nil || req.Holder != "run" || req.RunID == "" || req.Generation < 0 || req.WaitMS < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "holder, run_id, generation or wait_ms is invalid")
			return
		}
		if req.WaitMS > 10000 {
			req.WaitMS = 10000
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.WaitMS)*time.Millisecond)
		defer cancel()
		grant, err := d.Gate.AcquireRun(ctx, req.RunID, req.Generation)
		if err != nil {
			writeGateError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, grant)
	})
	handle("POST /gate/release", func(w http.ResponseWriter, r *http.Request) {
		var req contract.GateReleaseRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if d.Gate == nil || req.RunID == "" {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "run_id is required")
			return
		}
		if err := d.Gate.ReleaseRun(req.RunID); err != nil {
			writeGateError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d.Gate.State())
	})
	handle("GET /gate", func(w http.ResponseWriter, _ *http.Request) {
		if d.Gate == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "gate is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, d.Gate.State())
	})
	handle("POST /exec/reap", func(w http.ResponseWriter, r *http.Request) {
		var req contract.ReapRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if d.Gate == nil || d.Proc == nil || req.RunID == "" {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "run_id is required")
			return
		}
		if !d.Gate.HeldByOrIdle(req.RunID) {
			writeError(w, http.StatusConflict, "NOT_HOLDER", "workspace gate is held by another run")
			return
		}
		killed, remaining, err := d.Proc.Reap(r.Context(), 3*time.Second)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "EXEC_REAP_FAILED", "orphan process cleanup did not complete")
			return
		}
		writeJSON(w, http.StatusOK, contract.ReapResult{Killed: killed, Remaining: remaining})
	})
	handle("GET /fs/tree", func(w http.ResponseWriter, r *http.Request) {
		if d.FS == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "file service is unavailable")
			return
		}
		depth, ok := queryInt(w, r, "depth", 1, 1, 16)
		if !ok {
			return
		}
		entries, err := d.FS.Tree(r.URL.Query().Get("path"), depth)
		if err != nil {
			writeFSError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, entries)
	})
	handle("GET /fs/file", func(w http.ResponseWriter, r *http.Request) {
		if d.FS == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "file service is unavailable")
			return
		}
		max64, ok := queryInt64(w, r, "max", 0, 0, 1<<20)
		if !ok {
			return
		}
		content, err := d.FS.File(r.URL.Query().Get("path"), max64)
		if err != nil {
			writeFSError(w, err)
			return
		}
		w.Header().Set("ETag", `"`+content.ETag+`"`)
		if etagMatches(r.Header.Get("If-None-Match"), content.ETag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, content)
	})
	handle("GET /events", func(w http.ResponseWriter, r *http.Request) {
		serveEvents(w, r, d)
	})
	handle("POST /preview/ensure", func(w http.ResponseWriter, r *http.Request) {
		if d.Preview == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "preview is unavailable")
			return
		}
		var body contract.PreviewEnsureRequest
		if !decodeJSON(w, r, &body) {
			return
		}
		waitSeconds, ok := queryInt(w, r, "wait", 90, 0, 90)
		if !ok {
			return
		}
		if body.Generation < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "generation is invalid")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(waitSeconds+10)*time.Second)
		defer cancel()
		state, err := d.Preview.Ensure(ctx, preview.EnsureReq{
			Generation: body.Generation, Commit: body.Commit, Wait: time.Duration(waitSeconds) * time.Second,
		}, r.Header.Get("X-Run-Id"))
		if err != nil {
			writePreviewError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, state)
	})
	handle("POST /preview/restart", func(w http.ResponseWriter, r *http.Request) {
		if d.Preview == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "preview is unavailable")
			return
		}
		if err := d.Preview.Restart(r.Context()); err != nil {
			writePreviewError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, d.Preview.Status())
	})
	handle("GET /preview/status", func(w http.ResponseWriter, _ *http.Request) {
		if d.Preview == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "preview is unavailable")
			return
		}
		writeJSON(w, http.StatusOK, d.Preview.Status())
	})
	handle("GET /preview/logs", func(w http.ResponseWriter, r *http.Request) {
		if d.Preview == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "preview is unavailable")
			return
		}
		tail, ok := queryInt(w, r, "tail", 200, 1, 2000)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(d.Preview.Logs(tail))
	})
	handle("POST /git/commit", func(w http.ResponseWriter, r *http.Request) {
		if d.Git == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "git service is unavailable")
			return
		}
		var req contract.CommitRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		result, err := d.Git.Commit(r.Context(), req)
		if err != nil {
			writeGateError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	handle("GET /git/log", func(w http.ResponseWriter, r *http.Request) {
		if d.Git == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "git service is unavailable")
			return
		}
		limit, ok := queryInt(w, r, "limit", 20, 1, 100)
		if !ok {
			return
		}
		entries, err := d.Git.Log(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "GIT_LOG_FAILED", "could not read git history")
			return
		}
		writeJSON(w, http.StatusOK, entries)
	})
	handle("POST /git/push", notImplemented)
	handle("GET /git/archive", notImplemented)
	handle("GET /disk/usage", func(w http.ResponseWriter, _ *http.Request) {
		if d.DiskUsage == nil {
			writeError(w, http.StatusServiceUnavailable, "NOT_READY", "disk measurement is unavailable")
			return
		}
		usage, err := d.DiskUsage()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DISK_USAGE_FAILED", "could not measure workspace usage")
			return
		}
		writeJSON(w, http.StatusOK, usage)
	})
	if d.Agent != nil {
		mux.Handle("/agent/", d.Agent)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
	})

	var handler http.Handler = mux
	handler = strictBearer(handler, d.Token)
	handler = recoverer(logger, d.SandboxID, handler)
	handler = requestLogger(logger, d.SandboxID, handler)
	return &http.Server{
		Addr: "0.0.0.0:7000", Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
}

func strictBearer(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		headers := r.Header.Values("Authorization")
		valid := token != "" && len(headers) == 1 && strings.HasPrefix(headers[0], "Bearer ")
		provided := ""
		if valid {
			provided = strings.TrimPrefix(headers[0], "Bearer ")
			valid = provided != "" && !strings.ContainsAny(provided, " \t\r\n") &&
				subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
		}
		if !valid {
			w.Header().Set("WWW-Authenticate", `Bearer realm="wsd"`)
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(logger *slog.Logger, sandboxID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		attrs := []any{"method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds()}
		if sandboxID != "" {
			attrs = append(attrs, "sandbox_id", sandboxID)
		}
		if runID := r.Header.Get("X-Run-Id"); runID != "" {
			attrs = append(attrs, "run_id", runID)
		}
		logger.Info("wsd request", attrs...)
	})
}

func recoverer(logger *slog.Logger, sandboxID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				// ReverseProxy uses this sentinel when a streaming client disconnects.
				// Re-panic so net/http can finish its normal abort path; writing a 500
				// here would corrupt an already-started SSE response.
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				logger.Error("wsd panic", "sandbox_id", sandboxID, "panic", fmt.Sprint(recovered))
				writeError(w, http.StatusInternalServerError, "INTERNAL", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func serveEvents(w http.ResponseWriter, r *http.Request, d Deps) {
	if d.Bus == nil {
		writeError(w, http.StatusServiceUnavailable, "NOT_READY", "event bus is unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAM_UNSUPPORTED", "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if d.Preview != nil {
		state := d.Preview.Status()
		writeSSE(w, flusher, bus.Msg{Type: "preview_status", Data: mustJSON(map[string]any{
			"status": state.Status, "generation": state.Generation, "commit": state.Commit,
			"restarts": state.Restarts, "error_code": state.ErrorCode,
		})})
	}
	if d.DiskUsage != nil {
		if usage, err := d.DiskUsage(); err == nil {
			writeSSE(w, flusher, bus.Msg{Type: "disk_usage", Data: mustJSON(usage)})
		}
	}
	messages, cancel := d.Bus.Subscribe()
	defer cancel()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-messages:
			if !open {
				return
			}
			writeSSE(w, flusher, msg)
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, msg bus.Msg) {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Type, msg.Data); err != nil {
		return
	}
	flusher.Flush()
}

func mustJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func notImplemented(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "this endpoint is reserved for P1")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must be a valid JSON object")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must contain exactly one JSON object")
		return false
	}
	return true
}

func queryInt(w http.ResponseWriter, r *http.Request, key string, fallback, min, max int) (int, bool) {
	value, ok := queryInt64(w, r, key, int64(fallback), int64(min), int64(max))
	return int(value), ok
}

func queryInt64(w http.ResponseWriter, r *http.Request, key string, fallback, min, max int64) (int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < min || value > max {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", key+" query parameter is invalid")
		return 0, false
	}
	return value, true
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == "*" || candidate == etag || candidate == `"`+etag+`"` {
			return true
		}
	}
	return false
}

func writeFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspacefs.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "file or directory not found")
	case errors.Is(err, workspacefs.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", "file request exceeds the 1 MB service limit")
	default:
		writeError(w, http.StatusBadRequest, "BAD_PATH", "path is invalid or outside the workspace")
	}
}

func writeGateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gate.ErrStaleGeneration):
		writeError(w, http.StatusConflict, "STALE_GENERATION", "generation is older than the current gate generation")
	case errors.Is(err, gate.ErrNotHolder):
		writeError(w, http.StatusConflict, "NOT_HOLDER", "run does not hold the workspace gate")
	case errors.Is(err, gate.ErrBusy), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeError(w, http.StatusConflict, "GATE_BUSY", "workspace gate is busy")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL", "workspace operation failed")
	}
}

func writePreviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, preview.ErrNoPreview):
		writeError(w, http.StatusConflict, "NO_PREVIEW", "no preview has been requested")
	default:
		writeGateError(w, err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, contract.ErrorResponse{Error: contract.ErrorBody{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
