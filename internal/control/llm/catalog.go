// Package llm provides the model catalog, OpenAI-compatible proxy and usage ledger.
package llm

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/tenon/pkg/contract"
	"gopkg.in/yaml.v3"
)

type Model struct {
	ID            string   `yaml:"id"`
	Name          string   `yaml:"name"`
	Tags          []string `yaml:"tags"`
	Desc          string   `yaml:"desc"`
	Default       bool     `yaml:"default"`
	Protocol      string   `yaml:"protocol"`
	BaseURL       string   `yaml:"base_url"`
	APIKeyEnv     string   `yaml:"api_key_env"`
	UpstreamModel string   `yaml:"upstream_model"`
	MaxTokens     int      `yaml:"max_tokens"`
	Demo          bool     `yaml:"demo"`
}
type Upstream struct {
	BaseURL, APIKey, UpstreamModel string
	MaxTokensCap                   int
	Demo                           bool
}

// Catalog 是模型目录：启动时严格加载一次，此后每次读取都会检查文件
// mtime/大小，变化时重新加载；解析失败保留上一份可用配置并记录日志。
// 这不要求重启服务，也不会因为缺少 API Key 而影响启动。
type Catalog struct {
	mu        sync.RWMutex
	reloadMu  sync.Mutex
	path      string
	local     bool
	models    map[string]Model
	order     []string
	defaultID string
	modTime   time.Time
	size      int64
	reloadErr error
}

func LoadCatalog(path string, local bool) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	models, order, defaultID, err := parseCatalog(raw, local)
	if err != nil {
		return nil, err
	}
	c := &Catalog{path: path, local: local, models: models, order: order, defaultID: defaultID}
	if info, err := os.Stat(path); err == nil {
		c.modTime, c.size = info.ModTime(), info.Size()
	}
	return c, nil
}

func parseCatalog(raw []byte, local bool) (map[string]Model, []string, string, error) {
	var doc struct {
		Models []Model `yaml:"models"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, "", err
	}
	if len(doc.Models) == 0 {
		return nil, nil, "", fmt.Errorf("model catalog is empty")
	}
	models := make(map[string]Model, len(doc.Models))
	order := make([]string, 0, len(doc.Models))
	defaultID := ""
	for _, m := range doc.Models {
		if m.ID == "" || models[m.ID].ID != "" {
			return nil, nil, "", fmt.Errorf("invalid or duplicate model id %q", m.ID)
		}
		if m.Protocol == "" {
			m.Protocol = "openai"
		}
		if m.Name == "" {
			m.Name = m.ID
		}
		if m.UpstreamModel == "" {
			m.UpstreamModel = m.ID
		}
		if m.Demo || m.Protocol == "demo" {
			if !local {
				return nil, nil, "", fmt.Errorf("demo model %s is only allowed in local mode", m.ID)
			}
			m.Demo = true
			m.Protocol = "demo"
		} else if m.Protocol != "openai" || m.BaseURL == "" {
			return nil, nil, "", fmt.Errorf("model %s must use an OpenAI-compatible base_url", m.ID)
		}
		if m.Default && defaultID != "" {
			return nil, nil, "", fmt.Errorf("multiple default models")
		}
		if m.Default {
			defaultID = m.ID
		}
		models[m.ID] = m
		order = append(order, m.ID)
	}
	if defaultID == "" {
		return nil, nil, "", fmt.Errorf("catalog needs a default model")
	}
	return models, order, defaultID, nil
}

// refresh 串行检查并热加载目录；任何失败都保留上一份可用配置。
func (c *Catalog) refresh() {
	c.reloadMu.Lock()
	defer c.reloadMu.Unlock()
	info, err := os.Stat(c.path)
	if err != nil {
		c.recordError(err)
		return
	}
	c.mu.RLock()
	unchanged := info.ModTime().Equal(c.modTime) && info.Size() == c.size
	c.mu.RUnlock()
	if unchanged {
		return
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		c.recordError(err)
		return
	}
	models, order, defaultID, err := parseCatalog(raw, c.local)
	if err != nil {
		c.recordError(err)
		return
	}
	c.mu.Lock()
	c.models, c.order, c.defaultID = models, order, defaultID
	c.modTime, c.size = info.ModTime(), info.Size()
	c.reloadErr = nil
	c.mu.Unlock()
	slog.Info("model catalog reloaded", "path", c.path, "models", len(order))
}

// recordError 只在错误内容变化时打日志，避免每请求刷屏。
func (c *Catalog) recordError(err error) {
	c.mu.Lock()
	changed := c.reloadErr == nil || c.reloadErr.Error() != err.Error()
	c.reloadErr = err
	c.mu.Unlock()
	if changed {
		slog.Error("model catalog reload failed; keep previous config", "path", c.path, "error", err)
	}
}

// snapshot 返回当前配置引用；刷新时整份替换而不原地修改，读取方无需持锁。
func (c *Catalog) snapshot() (map[string]Model, []string, string) {
	c.refresh()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.models, c.order, c.defaultID
}

func readiness(m Model) (bool, string) {
	if m.Demo || m.Protocol == "demo" {
		return true, ""
	}
	if m.APIKeyEnv == "" {
		return true, ""
	}
	if os.Getenv(m.APIKeyEnv) == "" {
		return false, m.APIKeyEnv
	}
	return true, ""
}

func (c *Catalog) List() []contract.ModelView {
	models, order, _ := c.snapshot()
	out := make([]contract.ModelView, 0, len(order))
	for _, id := range order {
		m := models[id]
		tags := m.Tags
		if tags == nil {
			tags = []string{}
		}
		ready, missing := readiness(m)
		out = append(out, contract.ModelView{
			ID: m.ID, Name: m.Name, Tags: tags, Desc: m.Desc, Default: m.Default,
			Ready: ready, MissingEnv: missing,
		})
	}
	return out
}

func (c *Catalog) Default() string {
	_, _, defaultID := c.snapshot()
	return defaultID
}

func (c *Catalog) Validate(id string) error {
	models, _, _ := c.snapshot()
	if _, ok := models[id]; !ok {
		return fmt.Errorf("unknown model")
	}
	return nil
}

// CheckReady 只做“能否发起对话”的轻量检查：模型存在且凭证可用。
func (c *Catalog) CheckReady(id string) error {
	models, _, _ := c.snapshot()
	m, ok := models[id]
	if !ok {
		return fmt.Errorf("模型 %q 不存在", id)
	}
	if ready, missing := readiness(m); !ready {
		return fmt.Errorf("模型 %s 未配置：缺少环境变量 %s", id, missing)
	}
	return nil
}

func (c *Catalog) Resolve(id string) (Upstream, error) {
	models, _, _ := c.snapshot()
	m, ok := models[id]
	if !ok {
		return Upstream{}, fmt.Errorf("unknown model")
	}
	cap := m.MaxTokens
	if cap <= 0 {
		cap = 8192
	}
	if m.Demo {
		return Upstream{UpstreamModel: m.UpstreamModel, MaxTokensCap: cap, Demo: true}, nil
	}
	key := ""
	if m.APIKeyEnv != "" {
		key = os.Getenv(m.APIKeyEnv)
		if key == "" {
			return Upstream{}, fmt.Errorf("模型 %s 未配置：缺少环境变量 %s", id, m.APIKeyEnv)
		}
	}
	return Upstream{BaseURL: strings.TrimRight(m.BaseURL, "/"), APIKey: key, UpstreamModel: m.UpstreamModel, MaxTokensCap: cap}, nil
}
