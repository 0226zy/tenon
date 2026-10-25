// Package deps 是组合根（dsh Composition 层的等价物）：
// 在这里把插件注册进内核、按配置装配依赖，main 只负责调用。
package deps

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/tenon/agent-runtime/internal/application/agent"
	"github.com/0226zy/tenon/agent-runtime/internal/application/project"
	"github.com/0226zy/tenon/agent-runtime/internal/application/session"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/derr"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/plugin"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/port"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/llminfo"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/mcp"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/openai"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/policy"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/skill"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/statusbar"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/storejsonl"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/adapter/tools"
	"github.com/0226zy/tenon/agent-runtime/internal/infrastructure/config"
)

// Context 是装配完成的服务上下文。
type Context struct {
	Config   *config.Config
	Registry *plugin.Registry
	Manager  *session.Manager
	Runs     *session.RunService
	Models   port.LLMInfoRepo
	// Orchestrator 是规划层编排用例（规划会话 → fork 编码会话）。
	Orchestrator *project.Orchestrator
	// CloseFunc 释放组合根持有的外部资源（MCP server 子进程），可空。
	CloseFunc func()
}

// Build 按配置组装全部插件与服务。
// 装配顺序：模型资源仓储 → Inference 解析器 → State 层存储 → Tools 层（含审批钩子）
// → Control 层循环 → 会话管理器。
func Build(cfg *config.Config) (*Context, error) {
	registry := plugin.NewRegistry()

	// ── LLM 模型信息资源层：LLMInfoRepo（当前为本地文件实现，后续可换 DB 实现）──
	repo, err := llminfo.NewFileRepo(cfg.ModelsFile, legacySeed(cfg))
	if err != nil {
		return nil, err
	}

	// ── Inference 层：按模型名解析 LLM 适配器（实例按名缓存）──
	resolver := newLLMResolver(repo, cfg.AllowDynamicModels)
	def, err := repo.Default()
	if err != nil {
		return nil, err
	}
	defLLM, err := resolver(def.Name)
	if err != nil {
		return nil, fmt.Errorf("缺省模型 %q 不可用: %w", def.Name, err)
	}
	if err := registry.RegisterLLM(def.Provider, defLLM); err != nil {
		return nil, err
	}

	// ── State 层：append-only 会话日志与持久 Run 记录 ──
	store, err := storejsonl.New(cfg.Storage.DataDir)
	if err != nil {
		return nil, err
	}
	runStore, err := storejsonl.NewRunStore(cfg.Storage.DataDir)
	if err != nil {
		return nil, err
	}
	// 会话工作区根存于 header，bash 会话据此启动（避免 Manager 与 BashTool 循环依赖）。
	// 子代理会话 ID 形如 <parent>__subN，共享父会话的工作区根。
	rootOf := func(sessionID string) string {
		base := sessionID
		if i := strings.Index(base, "__sub"); i > 0 {
			base = base[:i]
		}
		header, _, err := store.Load(base)
		if err != nil {
			return "."
		}
		return header.Cwd
	}

	// ── Tools 层：最小核心工具集（指引 §2.3.1）──
	bashTool := tools.NewBashTool(time.Duration(cfg.Tools.BashTimeoutSeconds)*time.Second, cfg.Tools.MaxOutputBytes, rootOf, cfg.Tools.EnvAllowlist)
	bashTool.SetOutputBounds(cfg.Tools.BashHeadBytes, cfg.Tools.BashTailBytes)
	bashTool.SetProcessListener(func(sessionID, runID string, pgid int) error {
		base := sessionID
		if i := strings.Index(base, "__sub"); i > 0 {
			base = base[:i]
		}
		err := runStore.AddRunPGID(base, runID, pgid)
		// 旧 messages 与项目编排兼容入口没有持久 Run 记录；新 Run API 必须登记成功。
		if errors.Is(err, derr.ErrRunNotFound) {
			return nil
		}
		return err
	})
	if err := registry.RegisterTool(bashTool); err != nil {
		return nil, err
	}
	if err := registry.RegisterTool(tools.NewReadFileTool(cfg.Tools.MaxOutputBytes, cfg.Tools.ReadMaxBytes)); err != nil {
		return nil, err
	}
	if err := registry.RegisterTool(tools.NewWriteFileTool()); err != nil {
		return nil, err
	}
	if err := registry.RegisterTool(tools.NewEditFileTool()); err != nil {
		return nil, err
	}
	if err := registry.RegisterTool(tools.NewGlobTool(cfg.Tools.GlobMaxResults, cfg.Tools.SearchMaxBytes)); err != nil {
		return nil, err
	}
	if err := registry.RegisterTool(tools.NewGrepTool(cfg.Tools.GrepMaxMatches, cfg.Tools.SearchMaxBytes)); err != nil {
		return nil, err
	}
	// 自验证层（Fix Loop 验证入口）：run_tests 复用 bash 持久会话执行。
	if err := registry.RegisterTool(tools.NewRunTestsTool(bashTool, time.Duration(cfg.Tools.TestTimeoutSeconds)*time.Second)); err != nil {
		return nil, err
	}
	// 预览由 wsd 执行；仅配置 WSD_URL 时注册，WSD_TOKEN 不进入 bash 环境。
	if err := registerPreviewFromEnv(registry); err != nil {
		return nil, err
	}
	// 部署与运维层：deploy 工具（配置决定启用；命令模板固化）。
	// 强制审批名单在 policyHook 创建后登记（见下文）。
	if !cfg.Deploy.Disabled && len(cfg.Deploy.Targets) > 0 {
		targets := make([]tools.DeployTarget, 0, len(cfg.Deploy.Targets))
		for _, t := range cfg.Deploy.Targets {
			targets = append(targets, tools.DeployTarget{Name: t.Name, Description: t.Description, CommandTemplate: t.Command})
		}
		if err := registry.RegisterTool(tools.NewDeployTool(bashTool, targets, time.Duration(cfg.Tools.BashTimeoutSeconds)*time.Second)); err != nil {
			return nil, err
		}
	}

	// ── 状态栏插件（指引 §2.2 状态栏条款）──
	registry.SetStatusBar(statusbar.New())

	// ── SOUL 层：系统提示词 ──
	systemPrompt := defaultSystemPrompt
	if cfg.Agent.SystemPromptFile != "" {
		raw, err := os.ReadFile(cfg.Agent.SystemPromptFile)
		if err != nil {
			return nil, fmt.Errorf("读取系统提示词文件 %s: %w", cfg.Agent.SystemPromptFile, err)
		}
		systemPrompt = string(raw)
	}

	// ── 工程技能库（agentskills.io 兼容；目录不存在/为空 = 不启用）──
	// 目录清单并入系统提示词静态前缀（启动期稳定，KV Cache 友好），
	// 正文经 use_skill 工具按需加载。
	if cfg.SkillsDir != "" {
		skills, err := skill.LoadDir(cfg.SkillsDir)
		if err != nil {
			return nil, err
		}
		if len(skills) > 0 {
			systemPrompt += skill.Catalog(skills)
			if err := registry.RegisterTool(tools.NewUseSkillTool(skills, cfg.Tools.MaxOutputBytes)); err != nil {
				return nil, err
			}
		}
	}

	// ── 会话管理器（先建，审批钩子需要 ModeOf 回调）──
	limits := agent.Limits{
		MaxIterations:          cfg.Agent.MaxIterations,
		TokenBudget:            cfg.Agent.TokenBudgetPerTurn,
		LivelockRepeat:         cfg.Agent.LivelockRepeat,
		MaxRetries:             cfg.Agent.MaxRetries,
		CircuitBreakerFailures: cfg.Agent.CircuitBreakerFailures,
	}
	classifier := func(err error) (bool, time.Duration) {
		return openai.Retryable(err), openai.RetryAfterHint(err)
	}
	spillDir := filepath.Join(cfg.Storage.DataDir, "spill")

	// Control 层循环在审批钩子注册后再构造，保证拿到完整 hooks 视图。
	var manager *session.Manager
	policyHook := policy.NewHook(func(sessionID string) string {
		if manager == nil {
			return ""
		}
		base := sessionID
		if i := strings.Index(base, "__sub"); i > 0 {
			base = base[:i] // 子代理继承父会话的审批模式
		}
		return manager.ModeOf(base)
	})
	registry.RegisterHook(policyHook)
	if !cfg.Deploy.Disabled && len(cfg.Deploy.Targets) > 0 {
		// deploy 触达外部系统：任何审批模式（含 full-auto）都强制人工裁决。
		policyHook.AddForceApproveTools("deploy")
	}
	loop := agent.NewLoop(resolver, registry, registry.Hooks(), registry.StatusBar(), systemPrompt, limits, classifier)
	loop.EnableStreaming(cfg.Agent.Streaming)
	manager = session.NewManager(store, loop, bashTool.CwdFor, spillDir,
		time.Duration(cfg.Agent.ApprovalTimeoutSeconds)*time.Second)
	manager.SetDefaultMode(cfg.Agent.DefaultMode)

	// ── 安全边界：工作区白名单（评审 P0-4；空 = 不限制）──
	if len(cfg.Security.AllowedCwdRoots) > 0 {
		roots := make([]string, 0, len(cfg.Security.AllowedCwdRoots))
		for _, r := range cfg.Security.AllowedCwdRoots {
			abs, err := filepath.Abs(r)
			if err != nil {
				return nil, fmt.Errorf("security.allowed_cwd_roots 含非法路径 %q: %w", r, err)
			}
			roots = append(roots, filepath.Clean(abs))
		}
		manager.SetCwdGuard(func(cwd string) error {
			for _, root := range roots {
				if cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator)) {
					return nil
				}
			}
			return fmt.Errorf("工作区 %s 不在允许的根目录内（security.allowed_cwd_roots: %s）", cwd, strings.Join(roots, ", "))
		})
	}

	// ── 上下文投影压缩（0 = 禁用）──
	// Load 已完成新旧键优先级与百分比折算，这里只使用绝对 prompt_tokens 阈值。
	if cfg.Agent.ContextCompressAtTokens > 0 {
		manager.SetCompression(cfg.Agent.ContextCompressAtTokens, cfg.Agent.ContextKeepRecent)
	}

	// ── 子代理委派工具（task）：隔离优于压缩（指引 §2.2）──
	// 在主循环之后注册：registry 是动态视图，Definitions() 每次推理时取，
	// 启动期完成全部注册即可保证静态前缀稳定。
	subLimits := agent.Limits{
		MaxIterations:          cfg.Agent.SubagentMaxIterations,
		TokenBudget:            cfg.Agent.TokenBudgetPerTurn,
		LivelockRepeat:         cfg.Agent.LivelockRepeat,
		MaxRetries:             cfg.Agent.MaxRetries,
		CircuitBreakerFailures: cfg.Agent.CircuitBreakerFailures,
	}
	if err := registry.RegisterTool(agent.NewSubAgentTool(resolver, registry, registry.Hooks(), registry.StatusBar(), subLimits, classifier, cfg.Tools.MaxOutputBytes)); err != nil {
		return nil, err
	}

	// ── MCP 工具生态（配置决定启用；只读 server 的工具进免审批名单）──
	var closeMCP func()
	if !cfg.MCP.Disabled && len(cfg.MCP.Servers) > 0 {
		cfgs := make([]mcp.ServerConfig, 0, len(cfg.MCP.Servers))
		for _, s := range cfg.MCP.Servers {
			cfgs = append(cfgs, mcp.ServerConfig{
				Name: s.Name, Command: s.Command, Args: s.Args, Env: s.Env,
				EnvAllowlist: cfg.Tools.EnvAllowlist,
				ReadOnly:     s.ReadOnly, TimeoutSeconds: s.TimeoutSeconds,
			})
		}
		closeFn, readOnlyNames, err := mcp.RegisterServers(registry, cfgs, cfg.Tools.MaxOutputBytes)
		if err != nil {
			return nil, fmt.Errorf("MCP server 接入失败: %w", err)
		}
		closeMCP = closeFn
		policyHook.AddReadOnlyTools(readOnlyNames...)
	}

	runs := session.NewRunService(manager, runStore, bashTool, session.RunOptions{
		StopGrace:          time.Duration(cfg.Runs.StopGraceSeconds) * time.Second,
		StopConfirmTimeout: time.Duration(cfg.Runs.StopConfirmSeconds) * time.Second,
		Models:             repo,
		AllowDynamicModels: cfg.AllowDynamicModels,
	})
	closeFunc := func() {
		if closeMCP != nil {
			closeMCP()
		}
		_ = store.Close()
	}
	return &Context{Config: cfg, Registry: registry, Manager: manager, Runs: runs, Models: repo,
		Orchestrator: project.NewOrchestrator(manager, 0), CloseFunc: closeFunc}, nil
}

// registerPreviewFromEnv 使用 wsd 注入的环境配置注册 preview；空 URL 表示不启用。
func registerPreviewFromEnv(registry *plugin.Registry) error {
	baseURL := strings.TrimSpace(os.Getenv("WSD_URL"))
	if baseURL == "" {
		return nil
	}
	return registry.RegisterTool(tools.NewPreviewTool(baseURL, os.Getenv("WSD_TOKEN")))
}

// legacySeed 在 models 文件缺失时，用 agent-runtime.yaml 的 legacy model 段生成种子配置。
func legacySeed(cfg *config.Config) []entity.LLMInfo {
	if cfg.Model.Model == "" {
		return nil
	}
	seed := entity.LLMInfo{
		Name:           "default",
		Provider:       cfg.Model.Provider,
		BaseURL:        cfg.Model.BaseURL,
		Model:          cfg.Model.Model,
		APIKey:         cfg.Model.APIKey,
		Temperature:    cfg.Model.Temperature,
		MaxTokens:      cfg.Model.MaxTokens,
		TimeoutSeconds: cfg.Model.TimeoutSeconds,
		Default:        true,
		Builtin:        true,
	}
	return []entity.LLMInfo{seed}
}

// newLLMResolver 按模型名解析 LLM 适配器：经 LLMInfoRepo 取配置，
// 按 Provider 构造适配器并按名缓存（空名 = 缺省模型）。
func newLLMResolver(repo port.LLMInfoRepo, allowDynamicModels bool) port.LLMResolver {
	var mu sync.Mutex
	cache := map[string]port.LLMPort{}
	return func(name string) (port.LLMPort, error) {
		var profile entity.LLMInfo
		var err error
		if name == "" {
			profile, err = repo.Default()
		} else {
			profile, err = repo.Get(name)
		}
		if err != nil && name != "" && allowDynamicModels {
			// 控制层可能刚热加载了模型目录，而本沙箱的 models.yaml 还是启动时的快照。
			// 所有模型都经控制层 /llm/v1 代理访问，回退到默认配置并把模型名换成请求名，
			// 由控制层按名字解析真正的上游；这样新模型无需重启沙箱即可使用。
			if fallback, fallbackErr := repo.Default(); fallbackErr == nil {
				profile = fallback
				profile.Name = name
				profile.Model = name
				err = nil
			}
		}
		if err != nil {
			return nil, err
		}
		mu.Lock()
		defer mu.Unlock()
		if llm, ok := cache[profile.Name]; ok {
			return llm, nil
		}
		if profile.Provider != "openai-compat" {
			return nil, fmt.Errorf("模型 %q 的 provider %q 未注册（当前支持: openai-compat）", profile.Name, profile.Provider)
		}
		apiKey := profile.APIKey
		if profile.APIKeyEnv != "" {
			apiKey = os.Getenv(profile.APIKeyEnv)
		}
		if apiKey == "" {
			return nil, fmt.Errorf("模型 %q 缺少 API 密钥：请设置环境变量 %s 或在模型信息中填写 api_key", profile.Name, profile.APIKeyEnv)
		}
		llm := openai.New(apiKey, profile.BaseURL, profile.Model,
			profile.Temperature, profile.MaxTokens, time.Duration(profile.TimeoutSeconds)*time.Second)
		cache[profile.Name] = llm
		return llm, nil
	}
}

const defaultSystemPrompt = `你是一个运行在服务器上的 Coding Agent，经 bash 与文件工具直接维护用户的项目。任务完成以验证通过为准（构建、测试、运行），而非代码写完。`
