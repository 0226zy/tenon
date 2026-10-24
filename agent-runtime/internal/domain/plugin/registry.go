// Package plugin 是 agent-runtime 的插件内核：dsh「一切皆插件」理念的 Go 落地形态。
//
// dsh 用 Cordis 微内核做依赖注入与可逆注册；Go 没有运行期加载 .so 的生态习惯，
// 这里采用「注册表 + 组合根」的等价形态：
//
//   - 所有能力（LLM 适配器、工具、钩子、状态栏）都是命名插件，注册进 Registry；
//   - 启用哪些插件由配置文件（agent-runtime.yaml 的 profile）决定，而非硬编码在循环里；
//   - Agent 循环本身只依赖 domain/port 接口，任何插件都可被替换实现；
//   - 注册顺序即工具声明顺序，保证静态前缀（工具定义）逐字节稳定（KV Cache 友好）。
//
// 与 dsh 的对应：Inference 层 = LLMPort 插件；Tools 层 = Tool 插件；
// State 层 = SessionStorePort；Control 层 = Agent 循环（只依赖端口）；
// Framework 层 = 本注册表。
package plugin

import (
	"fmt"
	"sort"
	"sync"

	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/port"
)

// Registry 是插件注册表。并发读安全；注册只允许在组合根启动阶段进行。
type Registry struct {
	mu        sync.RWMutex
	llms      map[string]port.LLMPort
	tools     map[string]port.Tool
	toolOrder []string // 注册顺序，工具声明顺序必须稳定
	hooks     []port.Hook
	statusBar port.StatusBarPort
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{
		llms:  map[string]port.LLMPort{},
		tools: map[string]port.Tool{},
	}
}

// ── 注册（组合根启动阶段调用）──────────────────────────────────────

// RegisterLLM 注册一个模型适配器插件（如 "openai-compat"）。
func (r *Registry) RegisterLLM(name string, llm port.LLMPort) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.llms[name]; dup {
		return fmt.Errorf("LLM 插件重复注册: %s", name)
	}
	r.llms[name] = llm
	return nil
}

// RegisterTool 注册一个工具插件。
func (r *Registry) RegisterTool(t port.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := t.Definition().Function.Name
	if _, dup := r.tools[name]; dup {
		return fmt.Errorf("工具插件重复注册: %s", name)
	}
	r.tools[name] = t
	r.toolOrder = append(r.toolOrder, name)
	return nil
}

// RegisterHook 挂载一个 PreToolUse / PostToolUse 钩子（按注册顺序执行）。
func (r *Registry) RegisterHook(h port.Hook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, h)
}

// SetStatusBar 设置状态栏插件（环境动态信息注入）。
func (r *Registry) SetStatusBar(sb port.StatusBarPort) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusBar = sb
}

// ── 查询 ─────────────────────────────────────────────────────────

// LLM 按名字取模型适配器；profile 中配置的名字不存在时返回错误。
func (r *Registry) LLM(name string) (port.LLMPort, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	llm, ok := r.llms[name]
	if !ok {
		return nil, fmt.Errorf("LLM 插件未注册: %s（已注册: %v）", name, r.LLMNames())
	}
	return llm, nil
}

// LLMNames 返回已注册的 LLM 插件名。
func (r *Registry) LLMNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.llms))
	for n := range r.llms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Hooks 返回全部钩子（执行顺序 = 注册顺序）。
func (r *Registry) Hooks() []port.Hook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]port.Hook(nil), r.hooks...)
}

// StatusBar 返回状态栏插件，可为 nil（不注入）。
func (r *Registry) StatusBar() port.StatusBarPort {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.statusBar
}

// ── ToolRegistryPort 实现 ────────────────────────────────────────

// Definitions 按注册顺序返回工具声明（静态前缀组成部分，逐字节稳定）。
func (r *Registry) Definitions() []entity.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]entity.ToolDefinition, 0, len(r.toolOrder))
	for _, name := range r.toolOrder {
		defs = append(defs, r.tools[name].Definition())
	}
	return defs
}

// Lookup 按名字取工具。
func (r *Registry) Lookup(name string) (port.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names 返回注册顺序的工具名列表。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.toolOrder...)
}

// Info 是插件清单条目，供 GET /v1/plugins 自省（对齐 dsh --dump-config）。
type Info struct {
	Kind string `json:"kind"` // llm / tool / hook / statusbar
	Name string `json:"name"`
}

// List 返回全部已注册插件清单。
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Info
	names := make([]string, 0, len(r.llms))
	for n := range r.llms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, Info{Kind: "llm", Name: n})
	}
	for _, n := range r.toolOrder {
		out = append(out, Info{Kind: "tool", Name: n})
	}
	for i := range r.hooks {
		out = append(out, Info{Kind: "hook", Name: fmt.Sprintf("hook#%d", i)})
	}
	if r.statusBar != nil {
		out = append(out, Info{Kind: "statusbar", Name: "env-status"})
	}
	return out
}
