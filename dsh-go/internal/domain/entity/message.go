// Package entity 存放 dsh-go 领域层的协议 DTO 与中立会话事件。
// 只依赖标准库；application 与 infrastructure 都依赖这里，方向不允许反过来。
//
// 设计基线：DeepSeek Harness（dsh）的 append-only SessionEvent 日志 +
// 《Agent 构建设计指引》§2.2 上下文五组件、§4.4 中立轨迹格式。
package entity

import (
	"encoding/json"
	"time"
)

// ── Chat 协议（中立消息结构，四角色）────────────────────────────────

// Message 是 chat completion 上下文里的一条消息。
// 三种典型形态：system/user（仅 Content）、assistant（Content 和/或 ToolCalls）、
// tool（ToolCallID + Content，即工具执行结果，错误也以本形态回注）。
//
// 该结构即本项目的「中立轨迹格式」：不含任何厂商私有字段，
// reasoning 等不可移植内容以 Kind=notice 的普通文本事件另行记录。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
	// IsError 保留工具执行结果的结构化成败标记，供事件回放与 UI 使用。
	IsError bool `json:"is_error,omitempty"`
}

// ToolCall 是 assistant 消息中的一次工具调用请求。
// 按指引 §4.4：只记录名称与参数，ID 在渲染为目标厂商请求时可重新生成。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // 目前固定为 "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 是被调用函数的名字与 JSON 字符串形式的参数。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDefinition 是提供给模型的工具声明（JSON Schema 参数）。
// 按指引 §2.3.2：Description 必须含职责、使用边界、负面清单、失败形态。
type ToolDefinition struct {
	Type     string       `json:"type"` // 固定为 "function"
	Function FunctionSpec `json:"function"`
}

// FunctionSpec 描述一个可调用的函数。
type FunctionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema（object）
}

// ChatResponse 是大模型一次响应中我们关心的部分。
type ChatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`

	// Raw 是 API 返回的原始报文（未重新序列化），仅供审计落盘，不参与序列化。
	Raw json.RawMessage `json:"-"`
}

// ── Append-only 会话事件日志（dsh Session 子系统的 Go 移植）─────────────

// SessionFormatVersion 是会话存储格式版本，写入 header，供将来迁移判定。
const SessionFormatVersion = 1

// EventKind 是事件类型。事件一律 append-only，禁止改写历史；
// 撤销类语义（如 dsh-message-recall 的 tombstone）也以追加新事件表达。
type EventKind string

const (
	// EventMessage 是一条四角色消息（system 状态栏不落地，仅推理前注入）。
	EventMessage EventKind = "message"
	// EventNotice 是 Harness 注入轨迹的提示（活性警告、熔断、截断说明、
	// 审批结果等），以普通文本身份进入上下文，不携带厂商私有结构。
	EventNotice EventKind = "notice"
	// EventMarker 是会话结构标记（创建 / fork 血缘标记等），不进入模型上下文。
	EventMarker EventKind = "marker"
	// EventApproval 是一次高风险操作审批的请求/结果记录，不进入模型上下文。
	EventApproval EventKind = "approval"
	// EventDelta 是只广播、不落盘的模型文本增量，Seq 恒为 -1。
	EventDelta EventKind = "delta"
)

// Event 是追加到会话日志的一条事件。
type Event struct {
	Seq  int       `json:"seq"` // 持久事件连续序号；临时 delta 固定为 -1
	TS   time.Time `json:"ts"`
	Kind EventKind `json:"kind"`
	// RunID 把本回合事件与持久 Run 关联，防止旧 marker 被新回合误收。
	RunID string `json:"run_id,omitempty"`

	// Message：Kind=message 时的消息体。
	Message *Message `json:"message,omitempty"`
	// Note：Kind=notice 时的提示文本。
	Note string `json:"note,omitempty"`
	// Marker：Kind=marker 时的结构标记。
	Marker *Marker `json:"marker,omitempty"`
	// Approval：Kind=approval 时的审批记录。
	Approval *ApprovalRecord `json:"approval,omitempty"`
	// Text / Delta 仅用于临时文本增量；Delta 兼容控制层 DTO，Text 用于 SSE。
	Text  string `json:"text,omitempty"`
	Delta string `json:"delta,omitempty"`
}

// IsDelta 报告事件是否为无持久序号的临时文本增量。
func (e Event) IsDelta() bool { return e.Kind == EventDelta || e.Seq < 0 }

// Marker 是会话结构标记。turn_done / turn_failed 携带结构化回合统计；
// 它们只表示回合逻辑结束，绝不表示执行树已确认停止。
type Marker struct {
	Type             string `json:"type"`
	ParentID         string `json:"parent_id,omitempty"`
	AtSeq            int    `json:"at_seq,omitempty"` // fork 时在父日志中的切点
	Detail           string `json:"detail,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	Iterations       int    `json:"iterations,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	Model            string `json:"model,omitempty"`
	ErrorCode        string `json:"error_code,omitempty"`
}

// ApprovalRecord 是一次审批请求与结果。
type ApprovalRecord struct {
	ID       string `json:"id"`
	ToolName string `json:"tool_name"`
	Summary  string `json:"summary"` // 人类可读的一句话描述
	Detail   string `json:"detail,omitempty"`
	Approved bool   `json:"approved"`
	By       string `json:"by"` // human / policy / timeout
}

// SessionHeader 是会话的持久化元数据（存储关注点，不属于可重放的对话状态）。
// 对应 dsh 的 SessionHeader：格式版本、cwd、血缘与 fork 继承长度。
type SessionHeader struct {
	FormatVersion int       `json:"format_version"`
	ID            string    `json:"id"`
	Title         string    `json:"title,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Cwd           string    `json:"cwd"`             // 工作区根（项目目录）
	Mode          string    `json:"mode"`            // suggest / auto-edit / full-auto
	Model         string    `json:"model,omitempty"` // 选用的模型名（LLMInfoRepo 资源）
	ParentID      string    `json:"parent_id,omitempty"`
	// Epoch 在会话创建或日志重建时生成，用于隔离不同日志世代的序号与 Run。
	Epoch string `json:"epoch"`
	// InheritedEventCount 是 fork 时从父会话继承的前缀事件数。
	InheritedEventCount int `json:"inherited_event_count,omitempty"`
}

// ── 工具执行 ─────────────────────────────────────────────────────

// ToolResult 是工具执行的统一返回。
// 按指引 FR-4/§4.3：工具层错误不终止会话，以 IsError=true 的普通工具结果回注。
type ToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

// RiskLevel 是一次工具调用的风险分级。
type RiskLevel string

const (
	RiskLow  RiskLevel = "low"
	RiskHigh RiskLevel = "high"
)

// ApprovalDecision 是审批结果。
type ApprovalDecision struct {
	Approved bool
	By       string // human / policy / timeout
	Reason   string
}
