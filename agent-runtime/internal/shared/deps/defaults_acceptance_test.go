package deps

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/config"
)

func TestBuildDefaultModeIsUsedWhenCreatingSession(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "agent-runtime.yaml")
	yaml := fmt.Sprintf(`model:
  provider: openai-compat
  base_url: http://127.0.0.1
  api_key: test
  model: test-model
models_file: %q
skills_dir: %q
storage:
  data_dir: %q
agent:
  default_mode: suggest
  max_iterations: 5
  token_budget_per_turn: 100
mcp:
  disabled: true
deploy:
  disabled: true
`, filepath.Join(root, "models.yaml"), filepath.Join(root, "no-skills"), filepath.Join(root, "data"))
	if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WSD_URL", "")
	t.Setenv("WSD_TOKEN", "")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	built, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if built.CloseFunc != nil {
		t.Cleanup(built.CloseFunc)
	}
	rt, err := built.Manager.Create(work, "", "default mode acceptance", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Header().Mode; got != "suggest" {
		t.Fatalf("created session mode = %q, want suggest from agent.default_mode", got)
	}
	if got := built.Manager.ModeOf(rt.ID()); got != "suggest" {
		t.Fatalf("policy-facing ModeOf = %q, want suggest", got)
	}
}
