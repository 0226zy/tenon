package entity

import "time"

// LLMInfo 是一份「LLM 模型信息」资源：一个可用模型的全部接入信息
// （厂商端点、模型 id、密钥引用、采样参数）。
//
// 当前由本地文件仓储管理（见 infrastructure/adapter/llminfo），
// 后续切换为 DB 仓储时实体不变——资源的持久化介质对领域透明。
//
// 安全约定：APIKey 与 APIKeyEnv 二选一；优先 APIKeyEnv（环境变量引用），
// 避免密钥落盘进版本库。文件仓储本身不应提交真实密钥。
type LLMInfo struct {
	// Name 是资源唯一标识（如 "deepseek" / "kimi"），会话按名引用。
	Name string `json:"name" yaml:"name"`
	// Provider 是插件内核里注册的 LLM 插件名（如 "openai-compat"）。
	Provider string `json:"provider" yaml:"provider"`
	BaseURL  string `json:"base_url" yaml:"base_url"`
	Model    string `json:"model" yaml:"model"` // 厂商侧模型 id
	// APIKeyEnv 指向存放密钥的环境变量名（推荐）。
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	// APIKey 是字面密钥（不推荐提交进版本库；DB 仓储后可加密存储）。
	APIKey string `json:"api_key,omitempty" yaml:"api_key,omitempty"`

	Temperature    float64 `json:"temperature" yaml:"temperature"`
	MaxTokens      int     `json:"max_tokens" yaml:"max_tokens"`
	TimeoutSeconds int     `json:"timeout_seconds" yaml:"timeout_seconds"`

	// Default 为 true 的条目是会话未指定模型时的缺省（至多一条，多条取第一条）。
	Default bool `json:"default,omitempty" yaml:"default,omitempty"`
	// Builtin 为 true 表示随系统预置（deepseek/kimi），用户自定义条目为 false。
	Builtin bool `json:"builtin,omitempty" yaml:"builtin,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}
