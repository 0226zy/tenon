package preview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/quota"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/idgen"
)

func TestHandlerRejectsMissingInvalidReplayedAndCrossSessionAuthorization(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}); err != nil {
		t.Fatal(err)
	}
	keyOne := "abcdefghijklmnopqrstuvwxyz"
	keyTwo := "zyxwvutsrqponmlkjihgfedcba"
	convOne := &store.Conversation{ConvID: "abcdefghij12", UserID: "alice", Model: "local-demo", PreviewKey: keyOne}
	convTwo := &store.Conversation{ConvID: "abcdefghij34", UserID: "alice", Model: "local-demo", PreviewKey: keyTwo}
	for _, conversation := range []*store.Conversation{convOne, convTwo} {
		if err := st.CreateConversation(ctx, st.DB(), conversation); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Preview.Domain = "localhost"
	cfg.Preview.CookieSecure = false
	cfg.Preview.CookieTTL = config.Duration{Duration: time.Minute}
	master := []byte("0123456789abcdef0123456789abcdef")
	manager := sandbox.NewManager(st, sandbox.DockerUnavailable{}, quota.New(cfg.Quota), cfg, idgen.New(), master)
	tickets := NewTickets(master, time.Minute)
	handler := NewHandler(st, manager, agentclient.New(cfg.AgentClient), tickets, master, cfg)
	hostOne := keyOne + ".preview.localhost:8081"
	hostTwo := keyTwo + ".preview.localhost:8081"
	request := func(target, host string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = host
		if cookie != nil {
			req.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	if got := request("/", hostOne, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("missing preview authorization=%d %s", got.Code, got.Body.String())
	} else if got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("preview missing nosniff header")
	} else if got.Header().Get("Content-Security-Policy") != "frame-ancestors "+cfg.Web.Origin {
		t.Fatalf("preview frame-ancestors=%q", got.Header().Get("Content-Security-Policy"))
	}
	if got := request("/", hostOne, &http.Cookie{Name: CookieName, Value: "invalid"}); got.Code != http.StatusUnauthorized {
		t.Fatalf("invalid preview cookie=%d %s", got.Code, got.Body.String())
	}
	if got := request("/__enter?t=invalid", hostOne, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("invalid preview ticket=%d %s", got.Code, got.Body.String())
	}
	ticket, err := tickets.Issue(convOne.ConvID, hostOne)
	if err != nil {
		t.Fatal(err)
	}
	enter := "/__enter?t=" + url.QueryEscape(ticket)
	if got := request(enter, "not-a-preview.invalid", nil); got.Code != http.StatusNotFound {
		t.Fatalf("wrong preview Host=%d %s", got.Code, got.Body.String())
	}
	if got := request(enter, hostTwo, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("ticket on another preview Host=%d %s", got.Code, got.Body.String())
	}
	entered := request(enter, hostOne, nil)
	if entered.Code != http.StatusFound {
		t.Fatalf("valid preview ticket=%d %s", entered.Code, entered.Body.String())
	}
	var previewCookie *http.Cookie
	for _, cookie := range entered.Result().Cookies() {
		if cookie.Name == CookieName {
			previewCookie = cookie
		}
	}
	if previewCookie == nil {
		t.Fatal("valid ticket did not establish a preview cookie")
	}
	if got := request(enter, hostOne, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("replayed preview ticket=%d %s", got.Code, got.Body.String())
	}
	if got := request("/", hostTwo, previewCookie); got.Code != http.StatusUnauthorized {
		t.Fatalf("cross-session preview cookie=%d %s", got.Code, got.Body.String())
	}
}
