// Package contracttest 提供 domain 层仓储端口的契约测试套件。
//
// 任何 port.LLMInfoRepo 实现（本地文件、DB……）都必须通过同一组行为用例，
// 以此保证切换实现时上层（application / interfaces）行为零差异。
// 用法（在实现包的测试中）：
//
//	func TestLLMInfoRepoContract(t *testing.T) {
//		contracttest.RunLLMInfoRepoContract(t, func(t *testing.T) port.LLMInfoRepo {
//			// 返回一个以 contracttest.SeedModels() 初始化的干净实例
//		})
//	}
package contracttest

import (
	"testing"

	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// SeedModels 是契约统一的前置数据：两条内置模型（deepseek 为缺省）。
// 工厂应以此初始化被测仓储，保证各实现面对相同初始状态。
func SeedModels() []entity.LLMInfo {
	return []entity.LLMInfo{
		{Name: "deepseek", Provider: "openai-compat", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat", APIKeyEnv: "DEEPSEEK_API_KEY", Default: true, Builtin: true},
		{Name: "kimi", Provider: "openai-compat", BaseURL: "https://api.moonshot.cn/v1", Model: "kimi-k2-0905-preview", APIKeyEnv: "MOONSHOT_API_KEY", Builtin: true},
	}
}

// RunLLMInfoRepoContract 对 LLMInfoRepo 实现执行完整契约套件。
// factory 每次必须返回一个以 SeedModels 初始化、与其他用例互不影响的干净实例。
func RunLLMInfoRepoContract(t *testing.T, factory func(t *testing.T) port.LLMInfoRepo) {
	t.Helper()

	t.Run("List 返回全部种子模型", func(t *testing.T) {
		repo := factory(t)
		models, err := repo.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(models) != 2 {
			t.Fatalf("List 返回 %d 条，期望 2", len(models))
		}
		names := map[string]bool{}
		for _, m := range models {
			names[m.Name] = true
		}
		for _, want := range []string{"deepseek", "kimi"} {
			if !names[want] {
				t.Fatalf("List 缺少种子模型 %q", want)
			}
		}
	})

	t.Run("Get 命中与未命中", func(t *testing.T) {
		repo := factory(t)
		m, err := repo.Get("kimi")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if m.Model != "kimi-k2-0905-preview" || !m.Builtin {
			t.Fatalf("Get 内容不符: %+v", m)
		}
		if _, err := repo.Get("nonexistent"); err == nil {
			t.Fatal("Get 不存在的模型应报错")
		}
	})

	t.Run("Default 返回标记缺省的模型", func(t *testing.T) {
		repo := factory(t)
		def, err := repo.Default()
		if err != nil {
			t.Fatalf("Default: %v", err)
		}
		if def.Name != "deepseek" {
			t.Fatalf("Default = %q，期望 deepseek", def.Name)
		}
	})

	t.Run("Save 新增自定义模型", func(t *testing.T) {
		repo := factory(t)
		custom := entity.LLMInfo{Name: "my-local", BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen2.5-coder", APIKey: "ollama"}
		if err := repo.Save(custom); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := repo.Get("my-local")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Provider != "openai-compat" {
			t.Fatalf("Provider 缺省应补全为 openai-compat，实际 %q", got.Provider)
		}
		if got.Builtin {
			t.Fatal("用户自定义模型不应是 Builtin")
		}
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatalf("新增模型的 CreatedAt/UpdatedAt 应非零: %+v", got)
		}
	})

	t.Run("Save 更新已有模型且内置标记不可覆盖", func(t *testing.T) {
		repo := factory(t)
		err := repo.Save(entity.LLMInfo{Name: "kimi", BaseURL: "https://api.moonshot.cn/v1", Model: "kimi-k3", APIKeyEnv: "MOONSHOT_API_KEY", Builtin: false})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := repo.Get("kimi")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Model != "kimi-k3" {
			t.Fatalf("更新未生效: %+v", got)
		}
		if !got.Builtin {
			t.Fatal("内置标记不可被 Save 覆盖")
		}
		if got.UpdatedAt.IsZero() {
			t.Fatal("更新后 UpdatedAt 应非零")
		}
	})

	t.Run("Save 拒绝不完整信息", func(t *testing.T) {
		repo := factory(t)
		cases := []entity.LLMInfo{
			{Name: "only-name"},                  // 缺 base_url / model
			{BaseURL: "http://x/v1", Model: "m"}, // 缺 name
			{Name: "n", BaseURL: "http://x/v1"},  // 缺 model
			{Name: "n", Model: "m"},              // 缺 base_url
		}
		for i, c := range cases {
			if err := repo.Save(c); err == nil {
				t.Fatalf("用例 %d（%+v）应报错", i, c)
			}
		}
	})

	t.Run("Delete 删除、未命中与至少保留一条", func(t *testing.T) {
		repo := factory(t)
		if err := repo.Delete("kimi"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		models, err := repo.List()
		if err != nil || len(models) != 1 {
			t.Fatalf("删除后 List: %v, %d 条", err, len(models))
		}
		if err := repo.Delete("deepseek"); err == nil {
			t.Fatal("删除最后一条应报错（至少保留一个模型）")
		}
		if err := repo.Delete("nonexistent"); err == nil {
			t.Fatal("删除不存在的模型应报错")
		}
	})
}
