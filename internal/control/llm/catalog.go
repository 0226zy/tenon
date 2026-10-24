// Package llm provides the model catalog, OpenAI-compatible proxy and usage ledger.
package llm

import (
	"bytes"
	"fmt"
	"os"
	"strings"

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
type Catalog struct {
	models    map[string]Model
	order     []string
	defaultID string
	local     bool
}

func LoadCatalog(path string, local bool) (*Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Models []Model `yaml:"models"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Models) == 0 {
		return nil, fmt.Errorf("model catalog is empty")
	}
	c := &Catalog{models: map[string]Model{}, local: local}
	for _, m := range doc.Models {
		if m.ID == "" || c.models[m.ID].ID != "" {
			return nil, fmt.Errorf("invalid or duplicate model id %q", m.ID)
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
				return nil, fmt.Errorf("demo model %s is only allowed in local mode", m.ID)
			}
			m.Demo = true
			m.Protocol = "demo"
		} else if m.Protocol != "openai" || m.BaseURL == "" {
			return nil, fmt.Errorf("model %s must use an OpenAI-compatible base_url", m.ID)
		}
		if m.Default && c.defaultID != "" {
			return nil, fmt.Errorf("multiple default models")
		}
		if m.Default {
			c.defaultID = m.ID
		}
		c.models[m.ID] = m
		c.order = append(c.order, m.ID)
	}
	if c.defaultID == "" {
		return nil, fmt.Errorf("catalog needs a default model")
	}
	return c, nil
}
func (c *Catalog) List() []contract.ModelView {
	out := make([]contract.ModelView, 0, len(c.order))
	for _, id := range c.order {
		m := c.models[id]
		tags := m.Tags
		if tags == nil {
			tags = []string{}
		}
		out = append(out, contract.ModelView{ID: m.ID, Name: m.Name, Tags: tags, Desc: m.Desc, Default: m.Default})
	}
	return out
}
func (c *Catalog) Default() string { return c.defaultID }
func (c *Catalog) Validate(id string) error {
	if _, ok := c.models[id]; !ok {
		return fmt.Errorf("unknown model")
	}
	return nil
}
func (c *Catalog) Resolve(id string) (Upstream, error) {
	m, ok := c.models[id]
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
			return Upstream{}, fmt.Errorf("model credential %s is not configured", m.APIKeyEnv)
		}
	}
	return Upstream{BaseURL: strings.TrimRight(m.BaseURL, "/"), APIKey: key, UpstreamModel: m.UpstreamModel, MaxTokensCap: cap}, nil
}
