package llminfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
	"github.com/0226zy/magents/internal/domain/port/contracttest"
)

// 契约套件：FileRepo 必须满足任何 LLMInfoRepo 实现都应成立的行为。
// 未来 DB 实现在其 own 测试中复用同一套件，保证可替换性。
func TestLLMInfoRepoContract(t *testing.T) {
	contracttest.RunLLMInfoRepoContract(t, func(t *testing.T) port.LLMInfoRepo {
		t.Helper()
		repo, err := NewFileRepo(filepath.Join(t.TempDir(), "models.yaml"), contracttest.SeedModels())
		if err != nil {
			t.Fatalf("NewFileRepo: %v", err)
		}
		return repo
	})
}

// 实现特有：seed 初始化落盘 + 文件权限 0600（可能含字面密钥）。
func TestSeedFileAndPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if _, err := NewFileRepo(path, contracttest.SeedModels()); err != nil {
		t.Fatalf("NewFileRepo: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("seed 应落盘: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("模型信息文件权限 = %o, 期望 600", info.Mode().Perm())
	}
}

// 实现特有：损坏的 YAML 在启动期暴露（而不是第一次会话时）。
func TestCorruptFileFailsAtOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte("models: [not yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileRepo(path, nil); err == nil {
		t.Fatal("损坏的模型信息文件应在打开时报错")
	}
}

// 实现特有：持久化——新实例读到同样的数据（模拟重启）。
func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.yaml")
	repo, err := NewFileRepo(path, contracttest.SeedModels())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(entity.LLMInfo{Name: "custom", BaseURL: "http://x/v1", Model: "m", APIKey: "k"}); err != nil {
		t.Fatal(err)
	}

	repo2, err := NewFileRepo(path, nil) // 无种子：文件已存在
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo2.Get("custom"); err != nil {
		t.Fatalf("重启后自定义模型丢失: %v", err)
	}
}
