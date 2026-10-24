package boot

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0226zy/tenon/internal/wsd/proc"
	"github.com/0226zy/tenon/pkg/contract"
)

func TestLoadEnvProductionAndTrustedLocal(t *testing.T) {
	setRequiredEnv(t)
	env, err := LoadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if env.Local || env.Listen != "0.0.0.0:7000" || env.WorkspaceRoot != "/workspace" || env.PreviewPort != 3000 {
		t.Fatalf("production defaults = %+v", env)
	}
	t.Setenv("WSD_PREVIEW_PORT", "0")
	if _, err := LoadEnv(); err == nil {
		t.Fatal("production accepted dynamic preview port")
	}
	t.Setenv("WSD_LOCAL", "true")
	t.Setenv("WSD_LISTEN", "127.0.0.1:7100")
	env, err = LoadEnv()
	if err != nil || !env.Local || env.PreviewPort != 0 || env.AgentWSDURL() != "http://127.0.0.1:7100" {
		t.Fatalf("local env = %+v, %v", env, err)
	}
	t.Setenv("WSD_TOKEN", "")
	if _, err := LoadEnv(); err == nil {
		t.Fatal("local mode accepted an empty WSD token")
	}
}

func TestInitWorkspaceAndGeneratedConfigs(t *testing.T) {
	template := t.TempDir()
	if err := os.MkdirAll(filepath.Join(template, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "start.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(template, "web", "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	created, err := InitWorkspace(root, template)
	if err != nil || !created {
		t.Fatalf("init = %v, %v", created, err)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "start.sh")); err != nil {
		t.Fatalf("template was not copied as contents: %v", err)
	}
	cmd := exec.Command("git", "log", "-1", "--format=%an")
	cmd.Dir = filepath.Join(root, "app")
	author, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(author)) != "platform-bot" {
		t.Fatalf("initial commit author = %q, %v", author, err)
	}
	created, err = InitWorkspace(root, template)
	if err != nil || created {
		t.Fatalf("second init = %v, %v", created, err)
	}

	var cfg contract.SandboxConfig
	cfg.Models = []contract.SandboxModel{{ID: "model-a"}, {ID: "model-b", Default: true}}
	cfg.Agent.MaxIterations = 80
	cfg.Agent.TokenBudgetPerTurn = 800000
	cfg.Agent.ContextCompressAtTokens = 96000
	agentRuntimeDir := filepath.Join(root, ".agent-runtime")
	if err := WriteModelsConfig(agentRuntimeDir, cfg, "http://control:8082"); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(agentRuntimeDir, "models.yaml")
	models, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(models), "base_url: http://control:8082/llm/v1") || !strings.Contains(string(models), "api_key_env: LLM_PROXY_TOKEN") || strings.Contains(string(models), "sandbox-secret") {
		t.Fatalf("models config = %s", models)
	}
	info, err := os.Stat(modelsPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("models mode = %v, %v", info.Mode(), err)
	}
	base := filepath.Join(t.TempDir(), "base.md")
	appendFile := filepath.Join(t.TempDir(), "append.md")
	_ = os.WriteFile(base, []byte("base prompt"), 0o600)
	_ = os.WriteFile(appendFile, []byte("platform prompt"), 0o600)
	if err := writeSystemPrompt(base, appendFile, "control prompt", filepath.Join(agentRuntimeDir, "system_prompt.md")); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentRuntimeConfig(agentRuntimeDir, cfg, "agent-runtime-secret", filepath.Join(agentRuntimeDir, "skills"), "127.0.0.1:34567"); err != nil {
		t.Fatal(err)
	}
	agentRuntimeConfig, err := os.ReadFile(filepath.Join(agentRuntimeDir, "agent-runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(agentRuntimeConfig)
	for _, expected := range []string{"listen: 127.0.0.1:34567", "token_budget_per_turn: 800000", "context_compress_at_tokens: 96000", "disable_webui: true", "default_mode: full-auto", "- GOWORK"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("agent-runtime config lacks %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "agent-runtime-secret") || strings.Contains(text, "auth_token:") {
		t.Fatalf("agent-runtime config leaked token: %s", text)
	}
}

func TestProcessEnvironmentIsolatesGeneratedGoWorkspace(t *testing.T) {
	values := baseProcessEnvironment(Env{}, t.TempDir())
	if values["GOWORK"] != "off" {
		t.Fatalf("generated application must ignore an outer go.work: %+v", values)
	}
}

func TestWriteAgentRuntimeConfigKeepsDefaultAddress(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAgentRuntimeConfig(dir, contract.SandboxConfig{}, "agent-runtime-secret"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent-runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "listen: 127.0.0.1:3080") {
		t.Fatalf("default agent-runtime config address changed:\n%s", raw)
	}
}

func TestAgentRuntimeAddressSelectionAndOccupiedPrecheck(t *testing.T) {
	productionAddress, err := allocateAgentRuntimeAddress(false)
	if err != nil || productionAddress != "127.0.0.1:3080" {
		t.Fatalf("production address = %q, %v", productionAddress, err)
	}
	localAddress, err := allocateAgentRuntimeAddress(true)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(localAddress)
	if err != nil || host != "127.0.0.1" || port == "0" {
		t.Fatalf("local address = %q, %v", localAddress, err)
	}
	listener, err := net.Listen("tcp", localAddress)
	if err != nil {
		t.Fatalf("selected local address is not free: %v", err)
	}
	defer listener.Close()

	supervisor := newAgentRuntimeSupervisor(exec.Command("must-not-start"), proc.New(), localAddress)
	if supervisor.healthURL != "http://"+localAddress+"/healthz" {
		t.Fatalf("health URL = %q for %q", supervisor.healthURL, localAddress)
	}
	if err := supervisor.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "agent-runtime address "+localAddress+" is unavailable") {
		t.Fatalf("occupied address precheck = %v", err)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SANDBOX_ID", "sandbox-1")
	t.Setenv("CP_INTERNAL_URL", "http://127.0.0.1:8082")
	t.Setenv("LLM_PROXY_TOKEN", "sandbox-secret")
	t.Setenv("WSD_TOKEN", "wsd-secret")
	for _, key := range []string{"WSD_LOCAL", "WSD_LISTEN", "WORKSPACE_ROOT", "WSD_TEMPLATE_DIR", "WSD_AGENT_RUNTIME_BIN", "WSD_IMAGE_VER", "WSD_PREVIEW_PORT"} {
		t.Setenv(key, "")
	}
}
