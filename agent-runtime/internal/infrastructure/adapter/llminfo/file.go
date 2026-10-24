// Package llminfo 是「LLM 模型信息」资源的本地文件仓储实现
// （port.LLMInfoRepo 的当前实现；后续切换 DB 仓储时本包整体替换，上层零改动）。
//
// 文件格式（models.yaml）：
//
//	models:
//	  - name: deepseek
//	    provider: openai-compat
//	    base_url: https://api.deepseek.com/v1
//	    model: deepseek-chat
//	    api_key_env: DEEPSEEK_API_KEY
//	    default: true
//	  - name: kimi
//	    ...
//
// 写操作（Save/Delete）整体重写文件——模型信息是低频写入资源，
// 简单性与可读性优先；并发写由进程内互斥锁保护。
package llminfo

import (
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
)

// FileRepo 是本地 YAML 文件版 LLMInfoRepo。
type FileRepo struct {
	path string
	mu   sync.Mutex
}

type fileFormat struct {
	Models []entity.LLMInfo `yaml:"models"`
}

// NewFileRepo 打开（必要时用 seed 初始化）模型信息文件。
// seed 在文件不存在时写入，通常来自 agent-runtime.yaml 的 legacy model 段或内置默认。
func NewFileRepo(path string, seed []entity.LLMInfo) (*FileRepo, error) {
	r := &FileRepo{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) && len(seed) > 0 {
		if err := r.write(seed); err != nil {
			return nil, fmt.Errorf("初始化模型信息文件失败: %w", err)
		}
	}
	// 启动即校验：配置错误暴露在启动期，而不是第一次会话时
	if _, err := r.List(); err != nil {
		return nil, err
	}
	return r, nil
}

// List 实现 port.LLMInfoRepo。
func (r *FileRepo) List() ([]entity.LLMInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.read()
}

// Get 实现 port.LLMInfoRepo。
func (r *FileRepo) Get(name string) (entity.LLMInfo, error) {
	models, err := r.List()
	if err != nil {
		return entity.LLMInfo{}, err
	}
	for _, m := range models {
		if m.Name == name {
			return m, nil
		}
	}
	return entity.LLMInfo{}, fmt.Errorf("模型信息不存在: %s（可用: %v）", name, names(models))
}

// Default 实现 port.LLMInfoRepo：取第一个 default: true 的条目；
// 没有显式缺省时取第一条。
func (r *FileRepo) Default() (entity.LLMInfo, error) {
	models, err := r.List()
	if err != nil {
		return entity.LLMInfo{}, err
	}
	if len(models) == 0 {
		return entity.LLMInfo{}, fmt.Errorf("模型信息为空：请在 %s 中至少配置一个模型", r.path)
	}
	for _, m := range models {
		if m.Default {
			return m, nil
		}
	}
	return models[0], nil
}

// Save 实现 port.LLMInfoRepo：按名 upsert（用户自定义模型的写入口）。
func (r *FileRepo) Save(profile entity.LLMInfo) error {
	if profile.Name == "" || profile.BaseURL == "" || profile.Model == "" {
		return fmt.Errorf("模型信息不完整：name / base_url / model 均必填")
	}
	if profile.Provider == "" {
		profile.Provider = "openai-compat"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	models, err := r.read()
	if err != nil {
		return err
	}
	now := time.Now()
	updated := false
	for i, m := range models {
		if m.Name == profile.Name {
			profile.CreatedAt = m.CreatedAt
			profile.UpdatedAt = now
			profile.Builtin = m.Builtin // 内置标记不可被覆盖
			models[i] = profile
			updated = true
			break
		}
	}
	if !updated {
		profile.CreatedAt, profile.UpdatedAt = now, now
		models = append(models, profile)
	}
	return r.writeLocked(models)
}

// Delete 实现 port.LLMInfoRepo。
func (r *FileRepo) Delete(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	models, err := r.read()
	if err != nil {
		return err
	}
	out := models[:0]
	found := false
	for _, m := range models {
		if m.Name == name {
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return fmt.Errorf("模型信息不存在: %s", name)
	}
	if len(out) == 0 {
		return fmt.Errorf("至少保留一个模型")
	}
	return r.writeLocked(out)
}

func (r *FileRepo) read() ([]entity.LLMInfo, error) {
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return nil, fmt.Errorf("读取模型信息 %s: %w", r.path, err)
	}
	var f fileFormat
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析模型信息 %s: %w", r.path, err)
	}
	return f.Models, nil
}

func (r *FileRepo) write(models []entity.LLMInfo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeLocked(models)
}

func (r *FileRepo) writeLocked(models []entity.LLMInfo) error {
	raw, err := yaml.Marshal(fileFormat{Models: models})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(r.path), 0o755); err != nil {
		return err
	}
	// 0600：文件可能含字面 api_key
	return os.WriteFile(r.path, raw, 0o600)
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

func names(models []entity.LLMInfo) []string {
	var out []string
	for _, m := range models {
		out = append(out, m.Name)
	}
	return out
}
