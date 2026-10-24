package preview

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/httpx"
)

const CookieName = "proto_preview"

type Handler struct {
	st        *store.Store
	sb        *sandbox.Manager
	ac        *agentclient.Client
	tickets   *Tickets
	master    []byte
	cfg       *config.Config
	transport http.RoundTripper
}

func NewHandler(st *store.Store, sb *sandbox.Manager, ac *agentclient.Client, tickets *Tickets, master []byte, cfg *config.Config) http.Handler {
	return &Handler{st: st, sb: sb, ac: ac, tickets: tickets, master: master, cfg: cfg, transport: http.DefaultTransport}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := h.previewKey(r.Host)
	if !ok {
		httpx.WriteError(w, httpx.E(404, "NOT_FOUND", "preview not found"))
		return
	}
	c, err := h.st.ConversationByPreviewKey(r.Context(), key)
	if err != nil {
		httpx.WriteError(w, httpx.E(404, "NOT_FOUND", "preview not found"))
		return
	}
	if r.URL.Path == "/__enter" {
		h.enter(w, r, c)
		return
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		httpx.WriteError(w, httpx.E(401, "UNAUTHENTICATED", "preview authorization required"))
		return
	}
	convID, err := verifyCookie(h.master, cookie.Value, r.Host)
	if err != nil || convID != c.ConvID {
		httpx.WriteError(w, httpx.E(401, "UNAUTHENTICATED", "preview authorization required"))
		return
	}
	h.proxy(w, r, c.ConvID)
}
func (h *Handler) previewKey(hostport string) (string, bool) {
	host := hostport
	if v, _, err := net.SplitHostPort(hostport); err == nil {
		host = v
	}
	suffix := ".preview." + h.cfg.Preview.Domain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	key := strings.TrimSuffix(host, suffix)
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(key) {
		return "", false
	}
	return key, true
}
func (h *Handler) enter(w http.ResponseWriter, r *http.Request, c *store.Conversation) {
	convID, err := h.tickets.Consume(r.URL.Query().Get("t"), r.Host)
	if err != nil || convID != c.ConvID {
		httpx.WriteError(w, httpx.E(401, "UNAUTHENTICATED", "invalid or consumed preview ticket"))
		return
	}
	sameSite := http.SameSiteLaxMode
	if h.cfg.Preview.CookieSecure {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: signCookie(h.master, c.ConvID, r.Host, h.cfg.Preview.CookieTTL.Duration), Path: "/", HttpOnly: true, Secure: h.cfg.Preview.CookieSecure, SameSite: sameSite})
	http.Redirect(w, r, "/", http.StatusFound)
}
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, convID string) {
	handle, status, err := h.sb.Lookup(r.Context(), convID)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if status != 2 {
		h.sb.EnsureAsync(convID)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Refresh", "5")
		w.WriteHeader(503)
		_, _ = fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>正在唤醒</title><p>沙箱正在唤醒，页面将在 5 秒后刷新。</p>")
		return
	}
	h.sb.Touch(convID)
	state, err := h.ac.PreviewStatus(r.Context(), handle)
	if err != nil {
		httpx.WriteError(w, mapError(err))
		return
	}
	targetHost := net.JoinHostPort(handle.Host(), "3000")
	if state.LocalPort > 0 {
		targetHost = net.JoinHostPort("127.0.0.1", fmt.Sprint(state.LocalPort))
	}
	target := &url.URL{Scheme: "http", Host: targetHost}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = h.transport
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.Host = targetHost
		stripCookie(req)
		for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
			req.Header.Del(k)
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		cookies := resp.Header.Values("Set-Cookie")
		resp.Header.Del("Set-Cookie")
		for _, v := range cookies {
			resp.Header.Add("Set-Cookie", regexp.MustCompile(`(?i);\s*Domain=[^;]*`).ReplaceAllString(v, ""))
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		httpx.WriteError(w, httpx.E(502, "PREVIEW_UNAVAILABLE", "preview is unavailable"))
	}
	proxy.ServeHTTP(w, r)
}
func stripCookie(r *http.Request) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	parts := []string{}
	for _, c := range cookies {
		if c.Name != CookieName {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if len(parts) > 0 {
		r.Header.Set("Cookie", strings.Join(parts, "; "))
	}
}
