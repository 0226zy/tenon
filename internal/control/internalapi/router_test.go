package internalapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/internal/control/llm"
	"github.com/0226zy/proto-platform/internal/control/quota"
	"github.com/0226zy/proto-platform/internal/control/sandbox"
	"github.com/0226zy/proto-platform/internal/control/store"
	"github.com/0226zy/proto-platform/internal/kit/config"
	"github.com/0226zy/proto-platform/internal/kit/idgen"
	"github.com/0226zy/proto-platform/internal/kit/secret"
	"github.com/0226zy/proto-platform/pkg/contract"
)

func TestSandboxConfigBuildAndLLMAuthorization(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}); err != nil {
		t.Fatal(err)
	}
	conv := &store.Conversation{ConvID: "conv-1", UserID: "alice", Model: "local-demo"}
	if err := st.CreateConversation(ctx, st.DB(), conv); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC()
	if err := st.PutSandbox(ctx, st.DB(), &store.Sandbox{ConvID: conv.ConvID, InstID: "inst-1", Status: 2, TokenHash: secret.Hash("sandbox-token"), TokenExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Quota.Build.Global = 1
	cfg.Quota.Build.PerUser = 1
	quotas := quota.New(cfg.Quota)
	manager := sandbox.NewManager(st, sandbox.DockerUnavailable{}, quotas, cfg, idgen.New(), []byte("0123456789abcdef0123456789abcdef"))
	catalogPath := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(catalogPath, []byte("models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := llm.LoadCatalog(catalogPath, true)
	if err != nil {
		t.Fatal(err)
	}
	proxy := llm.NewProxy(catalog, manager, &llm.UsageService{St: st}, llm.NewLimiter(cfg.LLM.PerConvConcurrency, cfg.LLM.PerUserQPS), cfg.LLM)
	handler := NewRouter(manager, quotas, catalog, proxy, cfg)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	for _, token := range []string{"", "wrong-token"} {
		if got := request(http.MethodGet, "/internal/sandbox/config", "", token); got.Code != http.StatusUnauthorized {
			t.Fatalf("config token=%q status=%d body=%s", token, got.Code, got.Body.String())
		}
	}
	configured := request(http.MethodGet, "/internal/sandbox/config", "", "sandbox-token")
	if configured.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%s", configured.Code, configured.Body.String())
	}
	var sandboxConfig contract.SandboxConfig
	if err := json.Unmarshal(configured.Body.Bytes(), &sandboxConfig); err != nil {
		t.Fatal(err)
	}
	if len(sandboxConfig.Models) != 1 || sandboxConfig.Models[0].ID != "local-demo" || !sandboxConfig.Models[0].Default || sandboxConfig.Agent.MaxIterations != cfg.Sandbox.Agent.MaxIterations {
		t.Fatalf("sandbox config=%+v", sandboxConfig)
	}

	if got := request(http.MethodPost, "/internal/build/acquire", `{"purpose":"other"}`, "sandbox-token"); got.Code != http.StatusBadRequest {
		t.Fatalf("bad build purpose=%d %s", got.Code, got.Body.String())
	}
	acquired := request(http.MethodPost, "/internal/build/acquire", `{"purpose":"preview"}`, "sandbox-token")
	if acquired.Code != http.StatusOK {
		t.Fatalf("build acquire=%d %s", acquired.Code, acquired.Body.String())
	}
	var slot contract.BuildAcquireResult
	if err := json.Unmarshal(acquired.Body.Bytes(), &slot); err != nil || slot.SlotID == "" {
		t.Fatalf("slot=%+v err=%v", slot, err)
	}
	busy := request(http.MethodPost, "/internal/build/acquire", `{"purpose":"preview"}`, "sandbox-token")
	if busy.Code != http.StatusTooManyRequests || busy.Header().Get("Retry-After") != "5" {
		t.Fatalf("busy=%d retry=%q body=%s", busy.Code, busy.Header().Get("Retry-After"), busy.Body.String())
	}
	if got := request(http.MethodPost, "/internal/build/release", `{"slot_id":`+strconvQuote(slot.SlotID)+`}`, "sandbox-token"); got.Code != http.StatusNoContent {
		t.Fatalf("build release=%d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, "/internal/build/acquire", `{"purpose":"preview"}`, "sandbox-token"); got.Code != http.StatusOK {
		t.Fatalf("build reacquire=%d %s", got.Code, got.Body.String())
	}

	if got := request(http.MethodGet, "/llm/v1/models", "", "sandbox-token"); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "RUN_NOT_ACTIVE") {
		t.Fatalf("LLM without running Run=%d %s", got.Code, got.Body.String())
	}
	r := &store.Run{RunID: "run-1", ConvID: conv.ConvID, UserID: "alice", RequestID: "request-1", InputHash: "hash", Model: "local-demo", Status: contract.RunQueued}
	if err := st.CreateRun(ctx, st.DB(), r); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/llm/v1/models", "", "sandbox-token"); got.Code != http.StatusForbidden {
		t.Fatalf("LLM with queued Run=%d %s", got.Code, got.Body.String())
	}
	if err := st.MarkRunning(ctx, st.DB(), r.RunID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/llm/v1/models", "", "sandbox-token"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "local-demo") {
		t.Fatalf("LLM with running Run=%d %s", got.Code, got.Body.String())
	}
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestSandboxTokenLLMRevokedWhenRunLeavesRunning(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 20}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	quotas := quota.New(cfg.Quota)
	manager := sandbox.NewManager(st, sandbox.DockerUnavailable{}, quotas, cfg, idgen.New(), []byte("0123456789abcdef0123456789abcdef"))
	catalogPath := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(catalogPath, []byte("models:\n  - id: local-demo\n    protocol: demo\n    demo: true\n    default: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := llm.LoadCatalog(catalogPath, true)
	if err != nil {
		t.Fatal(err)
	}
	proxy := llm.NewProxy(catalog, manager, &llm.UsageService{St: st}, llm.NewLimiter(cfg.LLM.PerConvConcurrency, cfg.LLM.PerUserQPS), cfg.LLM)
	handler := NewRouter(manager, quotas, catalog, proxy, cfg)

	for _, terminal := range []contract.RunStatus{contract.RunFinished, contract.RunFailed, contract.RunCancelled, contract.RunFinalizing} {
		t.Run(string(terminal), func(t *testing.T) {
			name := string(terminal)
			conv := &store.Conversation{ConvID: "conv-" + name, UserID: "alice", Model: "local-demo", PreviewKey: "preview-" + name}
			if err := st.CreateConversation(ctx, st.DB(), conv); err != nil {
				t.Fatal(err)
			}
			token := "sandbox-token-" + name
			expires := time.Now().Add(time.Hour).UTC()
			if err := st.PutSandbox(ctx, st.DB(), &store.Sandbox{ConvID: conv.ConvID, InstID: "inst-" + name, Status: 2, TokenHash: secret.Hash(token), TokenExpiresAt: &expires}); err != nil {
				t.Fatal(err)
			}
			r := &store.Run{RunID: "run-" + name, ConvID: conv.ConvID, UserID: "alice", RequestID: "request-" + name, InputHash: "hash", Model: "local-demo", Status: contract.RunRunning, Generation: 1}
			if err := st.CreateRun(ctx, st.DB(), r); err != nil {
				t.Fatal(err)
			}
			if err := st.MarkRunning(ctx, st.DB(), r.RunID, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			request := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/llm/v1/models", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, req)
				return recorder
			}
			// Two successful calls warm the positive authorization cache. Authorization
			// must nevertheless be revoked immediately when the Run leaves running.
			for i := 0; i < 2; i++ {
				if got := request(); got.Code != http.StatusOK {
					t.Fatalf("running LLM call %d=%d %s", i, got.Code, got.Body.String())
				}
			}
			if terminal == contract.RunFinalizing {
				if err := st.WithTx(ctx, func(tx *store.Tx) error {
					ok, err := st.TransitionRun(ctx, tx, r.RunID, []contract.RunStatus{contract.RunRunning}, contract.RunFinalizing)
					if err == nil && !ok {
						t.Fatal("Run did not enter finalizing")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				current, err := st.Run(ctx, st.DB(), r.RunID)
				if err != nil {
					t.Fatal(err)
				}
				ok, err := st.FinalizeRun(ctx, st.DB(), current, terminal, "done", "commit", "", "", 0, 0, time.Now().UTC())
				if err != nil || !ok {
					t.Fatalf("FinalizeRun ok=%v err=%v", ok, err)
				}
			}
			for i := 0; i < 2; i++ {
				if got := request(); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "RUN_NOT_ACTIVE") {
					t.Fatalf("LLM after %s call %d=%d %s", terminal, i, got.Code, got.Body.String())
				}
			}
		})
	}
}
