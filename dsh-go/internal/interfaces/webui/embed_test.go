package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Web UI 拆分后的服务形态：index/css/js 三个资源可达、内容类型正确、未知路径 404。
func TestHandler(t *testing.T) {
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	cases := []struct {
		path        string
		wantStatus  int
		wantType    string // Content-Type 前缀
		wantContent string
	}{
		{"/", 200, "text/html", `<script src="/app.js">`},
		{"/app.css", 200, "text/css", "--accent"},
		{"/app.js", 200, "javascript", "EventSource"},
		{"/nonexistent", 404, "", ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + c.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("状态码 = %d, 期望 %d", resp.StatusCode, c.wantStatus)
			}
			if c.wantType != "" && !strings.Contains(resp.Header.Get("Content-Type"), c.wantType) {
				t.Fatalf("Content-Type = %q, 期望含 %q", resp.Header.Get("Content-Type"), c.wantType)
			}
			if c.wantContent != "" {
				buf, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(buf), c.wantContent) {
					t.Fatalf("响应体缺少 %q", c.wantContent)
				}
			}
		})
	}
}

// index.html 引用的资源必须真实存在（防拆分后链接漂移）。
func TestIndexReferencesExist(t *testing.T) {
	for _, p := range []string{"/app.css", "/app.js"} {
		req := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("index.html 引用的 %s 不可达: %d", p, rec.Code)
		}
	}
}
