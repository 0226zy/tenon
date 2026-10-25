package boot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0226zy/tenon/pkg/contract"
	"gopkg.in/yaml.v3"
)

type modelsFile struct {
	Models []modelEntry `yaml:"models"`
}

type modelEntry struct {
	Name      string `yaml:"name"`
	Provider  string `yaml:"provider"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`
	Default   bool   `yaml:"default,omitempty"`
	Builtin   bool   `yaml:"builtin"`
}

func WriteModelsConfig(dir string, cfg contract.SandboxConfig, cpInternalURL string) error {
	if len(cfg.Models) == 0 {
		return errors.New("sandbox config contains no models")
	}
	models := modelsFile{Models: make([]modelEntry, 0, len(cfg.Models))}
	seen := make(map[string]bool)
	hasDefault := false
	for _, model := range cfg.Models {
		if model.ID == "" || seen[model.ID] {
			return errors.New("sandbox model IDs must be non-empty and unique")
		}
		seen[model.ID] = true
		hasDefault = hasDefault || model.Default
		models.Models = append(models.Models, modelEntry{
			Name: model.ID, Provider: "openai-compat", BaseURL: strings.TrimRight(cpInternalURL, "/") + "/llm/v1",
			Model: model.ID, APIKeyEnv: "LLM_PROXY_TOKEN", Default: model.Default, Builtin: true,
		})
	}
	if !hasDefault {
		models.Models[0].Default = true
	}
	raw, err := yaml.Marshal(models)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "models.yaml"), raw, 0o600)
}

// WriteAgentRuntimeConfig writes no bearer token. agentRuntimeToken is required
// and deliberately used only as a startup invariant; MAGENTS_AUTH_TOKEN remains process-only.
func WriteAgentRuntimeConfig(dir string, cfg contract.SandboxConfig, agentRuntimeToken string) error {
	return writeAgentRuntimeConfig(dir, cfg, agentRuntimeToken, "/opt/agent-runtime/skills", productionAgentRuntimeAddress)
}

func writeAgentRuntimeConfig(dir string, cfg contract.SandboxConfig, agentRuntimeToken, skillsDir, listenAddress string) error {
	if agentRuntimeToken == "" {
		return errors.New("agent-runtime token must be generated before writing config")
	}
	workspace := filepath.Dir(dir)
	agent := cfg.Agent
	if agent.MaxIterations <= 0 {
		agent.MaxIterations = 80
	}
	if agent.TokenBudgetPerTurn <= 0 {
		agent.TokenBudgetPerTurn = 800000
	}
	if agent.ContextCompressAtTokens <= 0 {
		agent.ContextCompressAtTokens = 96000
	}
	config := map[string]any{
		"server": map[string]any{
			"listen": listenAddress, "disable_webui": true, "disable_model_api": true,
		},
		"security":             map[string]any{"allowed_cwd_roots": []string{filepath.Join(workspace, "app")}},
		"models_file":          filepath.Join(dir, "models.yaml"),
		"allow_dynamic_models": true,
		"skills_dir":           skillsDir,
		"storage":              map[string]any{"data_dir": filepath.Join(dir, "data")},
		"agent": map[string]any{
			"max_iterations":             agent.MaxIterations,
			"token_budget_per_turn":      agent.TokenBudgetPerTurn,
			"context_compress_at_tokens": agent.ContextCompressAtTokens,
			"context_keep_recent":        16,
			"default_mode":               "full-auto",
			"system_prompt_file":         filepath.Join(dir, "system_prompt.md"),
			"subagent_max_iterations":    25,
		},
		"tools": map[string]any{
			"bash_timeout_seconds": 300, "test_timeout_seconds": 600, "max_output_bytes": 65536,
			"env_allowlist": []string{"PATH", "HOME", "LANG", "TERM", "GOPROXY", "GOFLAGS", "GOMODCACHE", "GOWORK", "npm_config_registry", "npm_config_cache"},
		},
	}
	raw, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "agent-runtime.yaml"), raw, 0o600)
}

func writeSystemPrompt(basePath, appendPath, extra, output string) error {
	parts := make([]string, 0, 3)
	base, err := os.ReadFile(basePath)
	if err != nil {
		return fmt.Errorf("read base system prompt: %w", err)
	}
	parts = append(parts, strings.TrimSpace(string(base)))
	if appendPath != "" {
		appendRaw, err := os.ReadFile(appendPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if len(appendRaw) > 0 {
			parts = append(parts, strings.TrimSpace(string(appendRaw)))
		}
	}
	if strings.TrimSpace(extra) != "" {
		parts = append(parts, strings.TrimSpace(extra))
	}
	return writeAtomic(output, []byte(strings.Join(parts, "\n\n")+"\n"), 0o600)
}

func writeAtomic(path string, raw []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".wsd-config-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(perm); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
