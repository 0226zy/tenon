package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/httpx"
)

type ActiveRunAuthorizer interface {
	RequireActiveRun(context.Context, sandbox.Principal) (string, error)
}
type Proxy struct {
	cat   *Catalog
	auth  ActiveRunAuthorizer
	usage *UsageService
	lim   *Limiter
	cfg   config.LLM
	http  *http.Client
}

func NewProxy(cat *Catalog, auth ActiveRunAuthorizer, usage *UsageService, lim *Limiter, cfg config.LLM) *Proxy {
	return &Proxy{cat: cat, auth: auth, usage: usage, lim: lim, cfg: cfg, http: &http.Client{}}
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	IsError    bool       `json:"is_error,omitempty"`
}
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type chatRequest struct {
	Model    string            `json:"model"`
	Messages []chatMessage     `json:"messages"`
	Tools    []json.RawMessage `json:"tools,omitempty"`
	Stream   bool              `json:"stream,omitempty"`
}
type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	principal, ok := sandbox.PrincipalFromRequest(r)
	if !ok {
		httpx.WriteError(w, httpx.E(401, "UNAUTHENTICATED", "invalid sandbox token"))
		return
	}
	runID, err := p.auth.RequireActiveRun(r.Context(), principal)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/llm/v1/models":
		p.models(w)
	case r.Method == "POST" && r.URL.Path == "/llm/v1/chat/completions":
		p.chat(w, r, principal, runID)
	default:
		httpx.WriteError(w, httpx.E(404, "NOT_FOUND", "not found"))
	}
}
func (p *Proxy) models(w http.ResponseWriter) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
		Demo    bool   `json:"demo,omitempty"`
	}
	data := []model{}
	for _, v := range p.cat.List() {
		data = append(data, model{ID: v.ID, Object: "model", OwnedBy: "tenon", Demo: contains(v.Tags, "demo")})
	}
	httpx.JSON(w, 200, map[string]any{"object": "list", "data": data})
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func (p *Proxy) chat(w http.ResponseWriter, r *http.Request, principal sandbox.Principal, runID string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		httpx.WriteError(w, httpx.E(400, "BAD_REQUEST", "invalid request body"))
		return
	}
	var req chatRequest
	if json.Unmarshal(raw, &req) != nil || req.Model == "" || len(req.Messages) == 0 {
		httpx.WriteError(w, httpx.E(400, "BAD_REQUEST", "invalid chat completion request"))
		return
	}
	up, err := p.cat.Resolve(req.Model)
	if err != nil {
		httpx.WriteError(w, httpx.E(503, "UPSTREAM_NOT_CONFIGURED", "model is not configured"))
		return
	}
	promptUsed, complUsed, err := p.usage.SumByRun(r.Context(), runID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if promptUsed+complUsed >= p.cfg.PerRunTokenLimit {
		httpx.WriteError(w, httpx.E(403, "RUN_TOKEN_LIMIT", "Run token limit reached"))
		return
	}
	release, retry, ok := p.lim.Allow(principal.ConvID, principal.UserID)
	if !ok {
		err := httpx.E(429, "RATE_LIMITED", "model request rate limit")
		err.Header = make(http.Header)
		err.Header.Set("Retry-After", fmt.Sprintf("%.0f", maxDuration(retry, time.Second).Seconds()))
		httpx.WriteError(w, err)
		return
	}
	defer release()
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["model"] = up.UpstreamModel
	cap := min(up.MaxTokensCap, p.cfg.MaxTokensCap)
	if cap <= 0 {
		cap = 8192
	}
	if v, ok := doc["max_tokens"].(float64); !ok || int(v) <= 0 || int(v) > cap {
		doc["max_tokens"] = cap
	}
	if req.Stream {
		doc["stream_options"] = map[string]any{"include_usage": true}
	}
	raw, _ = json.Marshal(doc)
	start := time.Now()
	usage := tokenUsage{}
	status := 200
	if up.Demo {
		w.Header().Set("X-Proto-Demo", "true")
		usage = p.serveDemo(w, r, req)
		p.record(principal, runID, req.Model, usage, status, start)
		return
	}
	upstreamCtx, cancel := context.WithTimeout(r.Context(), p.cfg.UpstreamTimeout.Duration)
	defer cancel()
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, "POST", up.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	if up.APIKey != "" {
		upstreamReq.Header.Set("Authorization", "Bearer "+up.APIKey)
	}
	resp, err := p.http.Do(upstreamReq)
	if err != nil {
		p.record(principal, runID, req.Model, usage, 0, start)
		httpx.WriteError(w, httpx.E(502, "UPSTREAM_ERROR", "upstream model request failed"))
		return
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	copyHeaders(w.Header(), resp.Header)
	if !req.Stream {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if readErr != nil {
			status = 0
		}
		var v struct {
			Usage tokenUsage `json:"usage"`
		}
		_ = json.Unmarshal(body, &v)
		usage = v.Usage
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		p.record(principal, runID, req.Model, usage, status, start)
		return
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if strings.HasPrefix(strings.TrimSpace(line), "data:") {
			var v struct {
				Usage tokenUsage `json:"usage"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))), &v) == nil && (v.Usage.PromptTokens != 0 || v.Usage.CompletionTokens != 0 || v.Usage.TotalTokens != 0) {
				usage = v.Usage
			}
		}
		if _, err := io.WriteString(w, line); err != nil {
			status = 0
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
		if readErr != nil {
			if readErr != io.EOF {
				status = 0
			}
			break
		}
	}
	p.record(principal, runID, req.Model, usage, status, start)
}
func (p *Proxy) record(principal sandbox.Principal, runID, model string, u tokenUsage, status int, start time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.usage.Record(ctx, store.UsageRow{ConvID: principal.ConvID, RunID: runID, UserID: principal.UserID, Model: model, PromptTokens: u.PromptTokens, ComplTokens: u.CompletionTokens, CostMS: int(time.Since(start).Milliseconds()), HTTPStatus: status})
}
func copyHeaders(dst, src http.Header) {
	for k, v := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Content-Length", "Transfer-Encoding", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Upgrade":
			continue
		}
		for _, x := range v {
			dst.Add(k, x)
		}
	}
}
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
