// Package agentproxy exposes only the control-facing dshd API surface.
package agentproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

type Route struct {
	Method  string
	Pattern string
}

func DefaultRoutes() []Route {
	return []Route{
		{Method: http.MethodPost, Pattern: "/agent/sessions"},
		{Method: http.MethodGet, Pattern: "/agent/sessions/{id}"},
		{Method: http.MethodPost, Pattern: "/agent/sessions/{id}/model"},
		{Method: http.MethodGet, Pattern: "/agent/sessions/{id}/events"},
		{Method: http.MethodGet, Pattern: "/agent/sessions/{id}/stream"},
		{Method: http.MethodPost, Pattern: "/agent/sessions/{id}/runs"},
		{Method: http.MethodGet, Pattern: "/agent/sessions/{id}/runs/{rid}"},
		{Method: http.MethodPost, Pattern: "/agent/sessions/{id}/runs/{rid}/start"},
		{Method: http.MethodPost, Pattern: "/agent/sessions/{id}/runs/{rid}/cancel"},
	}
}

func New(target *url.URL, dshdToken string, allow []Route) http.Handler {
	if allow == nil {
		allow = DefaultRoutes()
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		// A negative interval flushes every upstream write, including SSE frames.
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = "/v1/" + strings.TrimPrefix(r.In.URL.Path, "/agent/")
			r.Out.URL.RawPath = ""
			r.Out.Host = target.Host
			r.Out.Header = r.In.Header.Clone()
			r.Out.Header.Del("Authorization")
			r.Out.Header.Set("Authorization", "Bearer "+dshdToken)
			r.Out.Header.Set("Accept-Encoding", "identity")
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadGateway, "AGENT_UNAVAILABLE", "agent runtime is unavailable")
		},
		ModifyResponse: func(resp *http.Response) error {
			if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
				resp.Header.Set("Cache-Control", "no-cache")
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Del("Content-Length")
			}
			return nil
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dshdToken == "" || !allowed(allow, r.Method, r.URL.Path) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "route not found")
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func allowed(routes []Route, method, requestPath string) bool {
	if !strings.HasPrefix(requestPath, "/agent/") || strings.Contains(requestPath, "//") {
		return false
	}
	actual := strings.Split(strings.TrimPrefix(requestPath, "/agent/"), "/")
	for _, part := range actual {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, route := range routes {
		if route.Method != method {
			continue
		}
		expected := strings.Split(strings.TrimPrefix(route.Pattern, "/agent/"), "/")
		if len(actual) != len(expected) {
			continue
		}
		matched := true
		for i := range expected {
			if strings.HasPrefix(expected[i], "{") && strings.HasSuffix(expected[i], "}") {
				continue
			}
			if expected[i] != actual[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
