package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogHotReloadsAndKeepsLastGoodConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	writeCatalog(t, path, `models:
  - id: local-demo
    protocol: demo
    demo: true
    default: true
`)
	catalog, err := LoadCatalog(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(catalog.List()); got != 1 {
		t.Fatalf("models=%d", got)
	}

	writeCatalog(t, path, `models:
  - id: local-demo
    protocol: demo
    demo: true
  - id: local-demo-secondary
    protocol: demo
    demo: true
    default: true
`)
	list := catalog.List()
	if len(list) != 2 || list[1].ID != "local-demo-secondary" || !list[1].Default {
		t.Fatalf("hot reload failed: %+v", list)
	}
	if catalog.Default() != "local-demo-secondary" {
		t.Fatalf("default=%q", catalog.Default())
	}

	// 非法配置不应覆盖上一份可用目录。
	writeCatalog(t, path, "models: [")
	if got := len(catalog.List()); got != 2 {
		t.Fatalf("invalid reload changed catalog: %d", got)
	}
	if catalog.Default() != "local-demo-secondary" {
		t.Fatalf("default after invalid reload=%q", catalog.Default())
	}
}

func TestCatalogReadinessFollowsEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	writeCatalog(t, path, `models:
  - id: local-demo
    protocol: demo
    demo: true
    default: true
  - id: deepseek
    name: DeepSeek
    protocol: openai
    base_url: https://api.deepseek.com/v1
    api_key_env: TENON_TEST_DEEPSEEK_KEY
`)
	catalog, err := LoadCatalog(path, true)
	if err != nil {
		t.Fatal(err)
	}
	views := catalog.List()
	if !views[0].Ready {
		t.Fatalf("demo model should always be ready: %+v", views[0])
	}
	if views[1].Ready || views[1].MissingEnv != "TENON_TEST_DEEPSEEK_KEY" {
		t.Fatalf("missing key readiness=%+v", views[1])
	}
	if err := catalog.CheckReady("deepseek"); err == nil || !strings.Contains(err.Error(), "TENON_TEST_DEEPSEEK_KEY") {
		t.Fatalf("CheckReady error=%v", err)
	}
	if _, err := catalog.Resolve("deepseek"); err == nil || !strings.Contains(err.Error(), "TENON_TEST_DEEPSEEK_KEY") {
		t.Fatalf("Resolve error=%v", err)
	}

	t.Setenv("TENON_TEST_DEEPSEEK_KEY", "test-key")
	views = catalog.List()
	if !views[1].Ready || views[1].MissingEnv != "" {
		t.Fatalf("ready after env set=%+v", views[1])
	}
	if err := catalog.CheckReady("deepseek"); err != nil {
		t.Fatalf("CheckReady after env set: %v", err)
	}
	up, err := catalog.Resolve("deepseek")
	if err != nil || up.APIKey != "test-key" {
		t.Fatalf("Resolve after env set: %+v %v", up, err)
	}
}
