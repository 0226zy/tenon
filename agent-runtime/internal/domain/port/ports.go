// Package port 定义 application 层依赖的下游能力端口。
// 接口参数用领域类型（entity），由 infrastructure/adapter 实现，组合根注入。
// 对应 dsh 的八个可替换层中的 Inference / Tools / State / Execution 层。
package port

import (
	"context"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
)

// LLMPort 是模型 chat completion 能力（dsh ctx.llm 的最小等价）。
// 实现：OpenAI 兼容适配器（DeepSeek / Moonshot / OpenAI 均可接入）。
type LLMPort interface {
	// Chat 发起一次对话补全。messages 为完整上下文（静态前缀 + 轨迹），
	// tools 为当前启用的工具声明。实现内部不负责重试，重试由 Harness 恢复路径负责。
	Chat(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition) (*entity.ChatResponse, error)
	// ChatStream 聚合同一次模型响应；文本片段经 onDelta 顺序送达。
	ChatStream(ctx context.Context, messages []entity.Message, tools []entity.ToolDefinition, onDelta func(text string)) (*entity.ChatResponse, error)
	// Model 返回实际使用的模型 id。
	Model() string
}

// Tool 是一件可注册到插件内核的工具（dsh 工具层的最小等价）。
type Tool interface {
	// Definition 返回工具声明。Description 必须满足指引 §2.3.2 四项必备内容。
	Definition() entity.ToolDefinition
	// ConcurrencySafe 声明本工具是否可与其他调用并行执行（默认否，失败安全）。
	ConcurrencySafe() bool
	// Execute 执行一次调用。所有失败都必须以 ToolResult{IsError:true} 返回，
	// 而不是 Go error——错误是模型的输入，不是会话的终点。
	Execute(ctx context.Context, argsJSON string, tctx *ToolContext) entity.ToolResult
}

// ToolContext 是一次工具调用的运行上下文：会话身份、工作区、审批通道。
type ToolContext struct {
	SessionID string
	// RunID 把 shell 与子代理执行树登记到同一持久 Run；旧接口可为空。
	RunID string
	// Cwd 返回会话工作目录（bash 持久会话的当前目录，随 cd 漂移）。
	Cwd func() string
	// Approve 发起一次高风险操作审批，阻塞直到人类/策略裁决或超时。
	Approve func(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision
	// Spill 把超长输出全文落盘，返回可供 read_file 查看的路径。
	Spill func(toolName string, content string) string
	// Emit 把一条事件追加进会话日志（可空；子代理用它上报生命周期 marker）。
	Emit func(ev entity.Event)
	// Model 是发起调用的会话所选模型名（子代理沿用同一模型）。
	Model string
}

// ToolRegistryPort 是工具注册表的只读视图（application 层看到的工具层）。
type ToolRegistryPort interface {
	// Definitions 返回当前启用的全部工具声明（静态前缀组成部分，必须稳定）。
	Definitions() []entity.ToolDefinition
	// Lookup 按名字取工具；不存在时 ok=false（幻觉调用走结构化错误回注）。
	Lookup(name string) (Tool, bool)
	// Names 返回全部已注册工具名（注册顺序，保证静态前缀逐字节稳定）。
	Names() []string
}

// ExecTracker 跟踪一个 Run 的进程组。实现必须只清理登记到该 Run 的进程，
// 不得以会话名模糊匹配或终止预览等独立进程。
type ExecTracker interface {
	Register(runID string, pgid int)
	Kill(ctx context.Context, runID string, grace time.Duration) (remaining int, err error)
	Alive(runID string) int
}

// RunStore 持久化 Run 接收与执行状态。
type RunStore interface {
	Put(sid string, rec entity.RunRecord) error
	Get(sid, runID string) (entity.RunRecord, error)
	List(sid string) ([]entity.RunRecord, error)
	ListActive() ([]entity.RunRecord, error)
	MaxGeneration(sid string) (int64, error)
}

// SessionStorePort 是 append-only 会话日志的持久化能力（dsh State 层）。
type SessionStorePort interface {
	// Create 落盘一个新会话的 header，并写入 created marker 事件。
	Create(header entity.SessionHeader) error
	// Append 追加一条事件，分配连续 seq 并持久化后返回。
	Append(sessionID string, ev entity.Event) (entity.Event, error)
	// Load 读取 header 与完整事件日志（resume / replay / fork 的数据源）。
	Load(sessionID string) (entity.SessionHeader, []entity.Event, error)
	// UpdateHeader 更新会话元数据（如切换审批模式）。事件日志不受影响。
	UpdateHeader(header entity.SessionHeader) error
	// Fork 从 parentID 的前 atSeq 条事件派生新会话（atSeq<0 表示当前末尾），
	// 复制前缀事件、写入血缘 header、追加 forked marker。
	Fork(parentID, newID string, atSeq int) (entity.SessionHeader, error)
	// List 返回全部会话 header（按创建时间倒序）。
	List() ([]entity.SessionHeader, error)
}

// Hook 是约束与验证的挂载点（指引 §3.4：PreToolUse / PostToolUse）。
// 危险命令在 Pre 拦截，写文件后的即时反馈在 Post 触发。
type Hook interface {
	// PreToolUse 在工具执行前运行。返回非空字符串表示拒绝执行，
	// 该文本作为普通工具错误结果回注给模型（指引 §4.3：错误即输入）。
	PreToolUse(ctx context.Context, toolName, argsJSON string, tctx *ToolContext) (blockReason string)
	// PostToolUse 在工具执行后运行，可改写/补充结果（如追加 lint 反馈）。
	PostToolUse(ctx context.Context, toolName, argsJSON string, tctx *ToolContext, result *entity.ToolResult)
}

// StatusBarPort 是动态环境状态注入能力（指引 §2.2 状态栏条款：
// 动态信息追加在上下文末尾，不得改写静态前缀）。
type StatusBarPort interface {
	Snapshot(ctx context.Context, cwd string) string
}

// ── 模型信息资源（DDD 仓储模式）────────────────────────────────────

// LLMInfoRepo 是「可选模型信息」资源的仓储端口。
// 当前实现是本地文件（infrastructure/adapter/llminfo），
// 后续切换 DB 实现时 application 与接口层零改动。
type LLMInfoRepo interface {
	// List 返回全部可选模型（内置 + 用户自定义）。
	List() ([]entity.LLMInfo, error)
	// Get 按名取模型信息。
	Get(name string) (entity.LLMInfo, error)
	// Default 返回缺省模型（会话未指定时使用）。
	Default() (entity.LLMInfo, error)
	// Save 新增或更新一条模型信息（用户自定义的写入口）。
	Save(profile entity.LLMInfo) error
	// Delete 删除一条模型信息（内置条目可删，由实现决定是否允许）。
	Delete(name string) error
}

// LLMResolver 按模型名解析出可用的 LLM 适配器（组合根注入，
// 内部经 LLMInfoRepo 取配置、按 Provider 构造/缓存适配器实例）。
type LLMResolver func(modelName string) (LLMPort, error)

// SessionRuntime 是 Agent 循环看到的会话运行时（application/session 实现）。
// 循环只通过本接口触碰会话状态与日志，不感知存储与传输细节。
type SessionRuntime interface {
	// ID 返回会话身份。
	ID() string
	// RunID 返回当前回合的 Run；无回合时为空。
	RunID() string
	// Cwd 返回当前工作目录（随 bash 会话漂移）。
	Cwd() string
	// Model 返回本会话选用的模型名（LLMInfoRepo 中的 Name）。
	Model() string
	// AppendEvent 追加事件到 append-only 日志（持久化 + 广播订阅者）。
	AppendEvent(ev entity.Event) (entity.Event, error)
	// EmitTemp 只广播无 seq 的临时事件，绝不写入 JSONL。
	EmitTemp(ev entity.Event)
	// Messages 从事件日志派生模型上下文的消息序列（marker/approval 不进入），
	// 并修复轨迹完整性：缺配对的 tool result 以合成消息补齐（指引 §4.2）。
	Messages() []entity.Message
	// AddTokens 累加会话 token 消耗，仅用于统计。
	AddTokens(n int) int
	// ObservePrompt 记录最近一次请求的 prompt tokens，驱动上下文压缩。
	ObservePrompt(promptTokens int)
	// Approve 发起高风险操作审批，阻塞至裁决或超时。
	Approve(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision
	// Spill 把超长工具输出全文落盘，返回路径。
	Spill(toolName, content string) string
}
