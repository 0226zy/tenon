// Package internalapi exposes the sandbox-only configuration, build and LLM APIs.
package internalapi

import (
	"context"
	"net/http"

	"github.com/0226zy/protoforge/internal/control/llm"
	"github.com/0226zy/protoforge/internal/control/quota"
	"github.com/0226zy/protoforge/internal/control/sandbox"
	"github.com/0226zy/protoforge/internal/kit/config"
	"github.com/0226zy/protoforge/internal/kit/httpx"
	"github.com/0226zy/protoforge/pkg/contract"
)

type Authorizer interface {
	Authenticate(context.Context, string) (sandbox.Principal, error)
}
type Router struct {
	auth  Authorizer
	q     *quota.Manager
	cat   *llm.Catalog
	proxy http.Handler
	cfg   *config.Config
}

func NewRouter(auth Authorizer, q *quota.Manager, cat *llm.Catalog, proxy http.Handler, cfg *config.Config) http.Handler {
	r := &Router{auth: auth, q: q, cat: cat, proxy: proxy, cfg: cfg}
	mux := http.NewServeMux()
	mux.Handle("GET /internal/sandbox/config", r.secure(http.HandlerFunc(r.config)))
	mux.Handle("POST /internal/build/acquire", r.secure(http.HandlerFunc(r.acquire)))
	mux.Handle("POST /internal/build/release", r.secure(http.HandlerFunc(r.release)))
	mux.Handle("/llm/v1/", r.secure(proxy))
	return mux
}
func (r *Router) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, err := r.auth.Authenticate(req.Context(), httpx.Bearer(req))
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		next.ServeHTTP(w, sandbox.WithPrincipal(req, p))
	})
}
func (r *Router) config(w http.ResponseWriter, req *http.Request) {
	out := contract.SandboxConfig{}
	for _, m := range r.cat.List() {
		out.Models = append(out.Models, contract.SandboxModel{ID: m.ID, Default: m.Default})
	}
	out.Agent.MaxIterations = r.cfg.Sandbox.Agent.MaxIterations
	out.Agent.TokenBudgetPerTurn = r.cfg.Sandbox.Agent.TokenBudgetPerTurn
	out.Agent.ContextCompressAtTokens = r.cfg.Sandbox.Agent.ContextCompressAtTokens
	out.SystemPromptAppend = r.cfg.Sandbox.SystemPromptAppend
	out.Preview.Mode = r.cfg.Preview.Mode
	out.Preview.BuildTimeoutS = int(r.cfg.Preview.BuildTimeout.Seconds())
	out.Preview.MaxRestarts = r.cfg.Preview.MaxRestarts
	out.Preview.LogLines = r.cfg.Preview.LogLines
	out.Disk.LimitBytes = r.cfg.Sandbox.DiskLimit
	out.Disk.HighWatermark = r.cfg.Preview.DiskHighWatermark
	httpx.JSON(w, 200, out)
}
func (r *Router) acquire(w http.ResponseWriter, req *http.Request) {
	var in contract.BuildAcquireRequest
	if err := httpx.Decode(w, req, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	if in.Purpose != "preview" {
		httpx.WriteError(w, httpx.E(400, "BAD_REQUEST", "unsupported build purpose"))
		return
	}
	p, _ := sandbox.PrincipalFromRequest(req)
	slot, ok := r.q.TryAcquire(quota.KindBuild, p.UserID, p.ConvID)
	if !ok {
		w.Header().Set("Retry-After", "5")
		httpx.WriteError(w, httpx.E(429, "QUOTA_EXCEEDED", "build capacity is busy"))
		return
	}
	httpx.JSON(w, 200, contract.BuildAcquireResult{SlotID: slot.ID})
}
func (r *Router) release(w http.ResponseWriter, req *http.Request) {
	var in contract.BuildReleaseRequest
	if err := httpx.Decode(w, req, &in); err != nil {
		httpx.WriteError(w, err)
		return
	}
	r.q.Release(in.SlotID)
	w.WriteHeader(204)
}
