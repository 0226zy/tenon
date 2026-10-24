package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/auth"
	"github.com/0226zy/tenon/internal/control/conversation"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/files"
	"github.com/0226zy/tenon/internal/control/llm"
	"github.com/0226zy/tenon/internal/control/preview"
	"github.com/0226zy/tenon/internal/control/quota"
	"github.com/0226zy/tenon/internal/control/run"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/idgen"
	"github.com/0226zy/tenon/pkg/contract"
)

type apiFixture struct {
	st            *store.Store
	cfg           *config.Config
	auth          *auth.Service
	conversations *conversation.Service
	runs          *run.Service
	hub           *event.Hub
	events        *event.Store
	mapper        event.Mapper
	handler       http.Handler
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	st, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Mode = "local"
	cfg.Web.Origin = "http://127.0.0.1:8080"
	cfg.Auth.CookieSecure = false
	cfg.Auth.Argon2 = config.Argon2{Time: 1, Memory: 1024, Threads: 1}
	cfg.Run.MaxDuration = config.Duration{Duration: 2 * time.Second}
	cfg.Run.StopConfirmTimeout = config.Duration{Duration: 100 * time.Millisecond}
	cfg.Run.PrepareRetry = 1
	cfg.Quota.Queue.Timeout = config.Duration{Duration: time.Second}
	cfg.Event.SSEHeartbeat = config.Duration{Duration: 20 * time.Millisecond}
	cfg.Event.WriteTimeout = config.Duration{Duration: time.Second}
	catalogPath := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(catalogPath, []byte("models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := llm.LoadCatalog(catalogPath, true)
	if err != nil {
		t.Fatal(err)
	}
	ids := idgen.New()
	quotas := quota.New(cfg.Quota)
	sandboxes := sandbox.NewManager(st, sandbox.DockerUnavailable{}, quotas, cfg, ids, []byte("0123456789abcdef0123456789abcdef"))
	agent := agentclient.New(cfg.AgentClient)
	hub := event.NewHub()
	mapper := event.Mapper{ToolArgsMax: 100, ToolResultMax: 100}
	events := event.NewStore(st, hub)
	runs := run.NewService(st, events, hub, ids, catalog, sandboxes, agent, nil, quotas, cfg)
	conversations := conversation.New(st, events, ids, catalog)
	conversations.SetHooks(runs, sandboxes)
	workspaceFiles := files.New(st, sandboxes, agent, cfg.Files.MaxBytes)
	tickets := preview.NewTickets([]byte("0123456789abcdef0123456789abcdef"), time.Minute)
	previews := preview.NewService(st, sandboxes, agent, cfg, tickets)
	users := auth.New(st, ids, cfg.Auth)
	handler := NewRouter(Dependencies{Auth: users, Catalog: catalog, Conversations: conversations, Runs: runs, Events: events, Hub: hub, Mapper: mapper, Files: workspaceFiles, Preview: previews, Config: cfg})
	f := &apiFixture{st: st, cfg: cfg, auth: users, conversations: conversations, runs: runs, hub: hub, events: events, mapper: mapper, handler: handler}
	t.Cleanup(func() {
		runs.Close()
		_ = sandboxes.Close()
		_ = st.Close()
	})
	return f
}

func (f *apiFixture) createUser(t *testing.T, userID string) {
	t.Helper()
	if err := f.auth.CreateUser(context.Background(), userID, userID, "password-"+userID, 20); err != nil {
		t.Fatal(err)
	}
}

func (f *apiFixture) login(t *testing.T, userID string) *http.Cookie {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, userID, "password-"+userID)
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body))
	req.Header.Set("Origin", f.cfg.Web.Origin)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("login status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == f.cfg.Auth.CookieName {
			return cookie
		}
	}
	t.Fatal("session cookie was not set")
	return nil
}

func (f *apiFixture) request(t *testing.T, cookie *http.Cookie, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", f.cfg.Web.Origin)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, req)
	return recorder
}

func TestAuthConversationRunAndOwner404(t *testing.T) {
	f := newAPIFixture(t)
	f.createUser(t, "alice")
	f.createUser(t, "bob")
	alice, bob := f.login(t, "alice"), f.login(t, "bob")
	if got := f.request(t, alice, http.MethodGet, "/api/me", ""); got.Code != 200 || !strings.Contains(got.Body.String(), "alice") {
		t.Fatalf("me=%d %s", got.Code, got.Body.String())
	}
	if got := f.request(t, alice, http.MethodGet, "/api/models", ""); got.Code != 200 || !strings.Contains(got.Body.String(), "local-demo") {
		t.Fatalf("models=%d %s", got.Code, got.Body.String())
	}
	created := f.request(t, alice, http.MethodPost, "/api/conversations", `{}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", created.Code, created.Body.String())
	}
	var conversation contract.ConversationView
	if err := json.Unmarshal(created.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}
	base := "/api/conversations/" + conversation.ID
	acceptBody := `{"client_request_id":"request-1","content":"hello"}`
	accepted := f.request(t, alice, http.MethodPost, base+"/messages", acceptBody)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept=%d %s", accepted.Code, accepted.Body.String())
	}
	var first contract.AcceptResult
	if err := json.Unmarshal(accepted.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	replayed := f.request(t, alice, http.MethodPost, base+"/messages", acceptBody)
	var second contract.AcceptResult
	if err := json.Unmarshal(replayed.Body.Bytes(), &second); err != nil || replayed.Code != 202 || second.RunID != first.RunID || second.MessageID != first.MessageID {
		t.Fatalf("idempotent replay=%d %+v err=%v", replayed.Code, second, err)
	}
	conflict := f.request(t, alice, http.MethodPost, base+"/messages", `{"client_request_id":"request-1","content":"different"}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	if got := f.request(t, alice, http.MethodGet, base+"/runs/"+first.RunID, ""); got.Code != 200 {
		t.Fatalf("owned Run=%d %s", got.Code, got.Body.String())
	}
	for _, path := range []string{
		base, base + "/messages", base + "/events/history", base + "/events",
		base + "/files/tree?path=", base + "/files/content?path=README.md",
		base + "/preview", base + "/preview/logs", base + "/runs/" + first.RunID,
	} {
		if got := f.request(t, bob, http.MethodGet, path, ""); got.Code != http.StatusNotFound {
			t.Errorf("bob GET %s status=%d body=%s", path, got.Code, got.Body.String())
		}
	}
	for _, path := range []string{base + "/preview/restart", base + "/runs/" + first.RunID + "/cancel"} {
		if got := f.request(t, bob, http.MethodPost, path, `{}`); got.Code != http.StatusNotFound {
			t.Errorf("bob POST %s status=%d body=%s", path, got.Code, got.Body.String())
		}
	}
	if got := f.request(t, bob, http.MethodDelete, base, ""); got.Code != 404 {
		t.Fatalf("bob delete=%d", got.Code)
	}
	if got := f.request(t, alice, http.MethodGet, base, ""); got.Code != 200 {
		t.Fatalf("conversation was deleted by cross-owner request: %d", got.Code)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, base + "/git/push"},
		{http.MethodPost, base + "/deployments"},
		{http.MethodGet, base + "/deployments/deployment-1"},
	} {
		if got := f.request(t, alice, route.method, route.path, `{}`); got.Code != http.StatusNotImplemented {
			t.Errorf("reserved route %s %s=%d %s", route.method, route.path, got.Code, got.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{}`))
	req.AddCookie(alice)
	req.Header.Set("Origin", "https://evil.example")
	csrf := httptest.NewRecorder()
	f.handler.ServeHTTP(csrf, req)
	if csrf.Code != http.StatusForbidden || !strings.Contains(csrf.Body.String(), "CSRF_REJECTED") {
		t.Fatalf("cross-origin status=%d body=%s", csrf.Code, csrf.Body.String())
	}
	if got := f.request(t, bob, http.MethodPost, "/api/logout", ""); got.Code != 204 {
		t.Fatalf("logout=%d", got.Code)
	}
	if got := f.request(t, bob, http.MethodGet, "/api/me", ""); got.Code != 401 {
		t.Fatalf("me after logout=%d", got.Code)
	}
}

func TestEventHistoryPagination(t *testing.T) {
	f := newAPIFixture(t)
	f.createUser(t, "alice")
	cookie := f.login(t, "alice")
	created, err := f.conversations.Create(context.Background(), "alice", contract.CreateConversationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := f.st.WithTx(context.Background(), func(tx *store.Tx) error {
			_, err := f.events.AppendTx(context.Background(), tx, event.Control(created.ID, "", "notice", map[string]any{"text": strconv.Itoa(i)}))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/conversations/" + created.ID + "/events/history"
	var pages [][]int64
	before := ""
	for {
		target := base + "?limit=2"
		if before != "" {
			target += "&before=" + before
		}
		response := f.request(t, cookie, http.MethodGet, target, "")
		if response.Code != 200 {
			t.Fatalf("history=%d %s", response.Code, response.Body.String())
		}
		var page contract.Page[contract.UIFrame]
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		var seqs []int64
		for _, frame := range page.Items {
			if frame.Seq == nil {
				t.Fatal("persistent history frame has no sequence")
			}
			seqs = append(seqs, *frame.Seq)
		}
		pages = append(pages, seqs)
		if page.NextCursor == "" {
			break
		}
		before = page.NextCursor
	}
	if fmt.Sprint(pages) != "[[4 5] [2 3] [1]]" {
		t.Fatalf("history pages=%v", pages)
	}
}

type sseFrame struct {
	name, id, data string
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) sseFrame {
	t.Helper()
	for {
		var frame sseFrame
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read SSE: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			switch {
			case strings.HasPrefix(line, "event:"):
				frame.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "id:"):
				frame.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			case strings.HasPrefix(line, "data:"):
				frame.data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if frame.data != "" {
			return frame
		}
	}
}

func TestSSEPersistentReplayAndTemporaryFrames(t *testing.T) {
	f := newAPIFixture(t)
	f.createUser(t, "alice")
	cookie := f.login(t, "alice")
	created, err := f.conversations.Create(context.Background(), "alice", contract.CreateConversationRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.WithTx(context.Background(), func(tx *store.Tx) error {
		_, err := f.events.AppendTx(context.Background(), tx, event.Control(created.ID, "run-1", "notice", map[string]any{"text": "persisted"}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(f.handler)
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/conversations/"+created.ID+"/events?after=0", nil)
	req.AddCookie(cookie)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status=%d body=%s", response.StatusCode, raw)
	}
	reader := bufio.NewReader(response.Body)
	persistent := readSSEFrame(t, reader)
	if persistent.name != "ui" || persistent.id != "1" || !strings.Contains(persistent.data, "persisted") {
		t.Fatalf("persistent frame=%+v", persistent)
	}
	temporary, _ := f.mapper.TempToUI("run-1", "progress", "working")
	f.hub.PublishTemp(created.ID, temporary)
	temp := readSSEFrame(t, reader)
	if temp.name != "temp" || temp.id != "" || !strings.Contains(temp.data, "working") || strings.Contains(temp.data, `"seq"`) {
		t.Fatalf("temporary frame=%+v", temp)
	}
}

func TestConversationPaginationCursorUsesSingleQueryEncoding(t *testing.T) {
	f := newAPIFixture(t)
	f.createUser(t, "alice")
	cookie := f.login(t, "alice")
	for i := 0; i < 2; i++ {
		if _, err := f.conversations.Create(context.Background(), "alice", contract.CreateConversationRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	firstResponse := f.request(t, cookie, http.MethodGet, "/api/conversations?limit=1", "")
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	var first contract.Page[contract.ConversationView]
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" || strings.Contains(first.NextCursor, "%") {
		t.Fatalf("first page=%+v; cursor must be raw and non-empty", first)
	}
	secondResponse := f.request(t, cookie, http.MethodGet, "/api/conversations?limit=1&cursor="+url.QueryEscape(first.NextCursor), "")
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s cursor=%q", secondResponse.Code, secondResponse.Body.String(), first.NextCursor)
	}
	var second contract.Page[contract.ConversationView]
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID || second.NextCursor != "" {
		t.Fatalf("pagination first=%+v second=%+v", first, second)
	}
}

func TestNonGETRequiresAllowedOrigin(t *testing.T) {
	f := newAPIFixture(t)
	f.createUser(t, "alice")
	cookie := f.login(t, "alice")
	req := httptest.NewRequest(http.MethodPost, "/api/conversations", bytes.NewBufferString(`{}`))
	req.AddCookie(cookie)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site status=%d", response.Code)
	}
}
