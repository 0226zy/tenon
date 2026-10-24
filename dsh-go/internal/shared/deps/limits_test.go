package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0226zy/magents/internal/domain/port"
	"github.com/0226zy/magents/internal/infrastructure/config"
)

func TestBuildPropagatesFileAndSearchByteLimits(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "big.txt"), []byte(strings.Repeat("x", 128)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WSD_URL", "")
	t.Setenv("WSD_TOKEN", "")
	cfg := &config.Config{
		Model: config.ModelConfig{
			Provider: "openai-compat", BaseURL: "http://127.0.0.1", APIKey: "test", Model: "test",
		},
		ModelsFile: filepath.Join(root, "models.yaml"),
		Storage:    config.StorageConfig{DataDir: filepath.Join(root, "data")},
		Tools: config.ToolsConfig{
			MaxOutputBytes: 1024,
			ReadMaxBytes:   64,
			SearchMaxBytes: 1,
			GrepMaxMatches: 10,
			GlobMaxResults: 10,
		},
		MCP:    config.MCPConfig{Disabled: true},
		Deploy: config.DeployConfig{Disabled: true},
	}
	built, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if built.CloseFunc != nil {
		t.Cleanup(built.CloseFunc)
	}
	tctx := &port.ToolContext{
		Cwd:   func() string { return work },
		Spill: func(string, string) string { return filepath.Join(root, "spill.log") },
	}

	readTool, ok := built.Registry.Lookup("read_file")
	if !ok {
		t.Fatal("read_file 未注册")
	}
	res := readTool.Execute(context.Background(), `{"path":"big.txt"}`, tctx)
	if !res.IsError || !strings.Contains(res.Content, "read_max_bytes") || !strings.Contains(res.Content, "64") {
		t.Fatalf("Build 未传递 ReadMaxBytes: %+v", res)
	}

	grepTool, ok := built.Registry.Lookup("grep")
	if !ok {
		t.Fatal("grep 未注册")
	}
	res = grepTool.Execute(context.Background(), `{"pattern":"x"}`, tctx)
	if res.IsError || !strings.Contains(res.Content, "search_max_bytes") {
		t.Fatalf("Build 未向 grep 传递 SearchMaxBytes: %+v", res)
	}

	globTool, ok := built.Registry.Lookup("glob")
	if !ok {
		t.Fatal("glob 未注册")
	}
	res = globTool.Execute(context.Background(), `{"pattern":"*.txt"}`, tctx)
	if res.IsError || strings.Contains(res.Content, "big.txt") || !strings.Contains(res.Content, "search_max_bytes") {
		t.Fatalf("Build 未向 glob 传递 SearchMaxBytes: %+v", res)
	}
}
