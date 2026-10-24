// subagent.go 实现「隔离优于压缩」（指引 §2.2）：task 工具把会产生大量中间观察的
// 任务（批量扫描、长日志分析、大范围调研）委托给独立上下文的子 Agent，
// 子 Agent 的完整轨迹单独落盘供审计，只有结论回到主上下文。
//
// 对应 dsh 的 Agent Teams / Claude Agent SDK 的 Subagent 机制的最小实现：
//   - 子 Agent 复用同一套循环与工具（架构框架完全继承），
//   - 但消息序列独立（主上下文不被中间观察污染），
//   - 高风险操作仍走主会话的审批通道（忠诚对象不变），
//   - 禁止嵌套委派（深度计数器，防死亡螺旋条款①的委派形态）。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

// SubAgentToolName 是委派工具的名字。
const SubAgentToolName = "task"

// depthKey 是委派深度计数器（context value）：子代理上下文中 task 工具被禁用，
// 从机制上杜绝「委派再委派」的递归连锁。
type depthKey struct{}

// SubAgentSystemPrompt 是子代理的 SOUL 层：只交付结论，不复述过程。
const SubAgentSystemPrompt = `你是主 Agent 委派的子代理，在隔离上下文中执行一个自包含任务。
你看不到主会话的对话历史，任务描述就是你掌握的全部背景。
用 bash / read_file / grep / glob 等工具完成任务，最终回答只返回**结论**：
主 Agent 需要的事实、数据汇总、文件清单或判断依据——不要复述执行过程。
任务完成以验证通过为准（指引 FR-8 同样适用于你）。`

// SubAgentTool 是 task 委派工具。子代理沿用父会话的模型（经 tctx.Model 解析）。
type SubAgentTool struct {
	llmFor    port.LLMResolver
	tools     port.ToolRegistryPort
	hooks     []port.Hook
	statusBar port.StatusBarPort
	limits    Limits
	classify  ErrorClassifier
	maxOutput int
	seq       atomic.Int64
}

// NewSubAgentTool 构造委派工具。limits 应为比主循环更紧的预算（由配置决定）。
func NewSubAgentTool(llmFor port.LLMResolver, tools port.ToolRegistryPort, hooks []port.Hook, statusBar port.StatusBarPort, limits Limits, classify ErrorClassifier, maxOutput int) *SubAgentTool {
	return &SubAgentTool{llmFor: llmFor, tools: tools, hooks: hooks, statusBar: statusBar, limits: limits, classify: classify, maxOutput: maxOutput}
}

// Definition 实现 port.Tool（描述遵循指引 §2.3.2：职责/边界/负面清单/失败形态）。
func (t *SubAgentTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: SubAgentToolName,
			Description: `把任务委派给独立上下文的子 Agent 执行，只把结论带回来。
何时使用：会产生大量中间观察的任务——批量文件扫描、长日志分析、大范围代码调研、需要几十次工具调用才能得出结论的探索。这些观察留在子代理的上下文里，你的上下文只收到结论。
何时不要用：一两次工具调用就能完成的事自己做；需要精确保留中间产物（如逐文件修改）的任务自己做——子代理只回传结论文本。
参数：task 必须自包含（子代理看不到本会话历史），写清目标、相关路径、期望的结论形式。
失败形态：子代理触发保护性终止（轮数/预算）时返回错误与已完成部分的说明；子代理轨迹全文落盘，路径在返回值中给出，可用 read_file 查看。`,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"task": map[string]any{
						"type":        "string",
						"description": "自包含的任务描述，例如 \"统计 internal/ 下所有 Go 文件的导出函数数量，按包分组返回表格\"",
					},
				},
				"required": []string{"task"},
			},
		},
	}
}

// ConcurrencySafe 实现 port.Tool：子代理内部会跑有状态工具，不与其他调用并行。
func (t *SubAgentTool) ConcurrencySafe() bool { return false }

type taskArgs struct {
	Task string `json:"task"`
}

// Execute 实现 port.Tool：起独立上下文跑子循环，结论回传，轨迹落盘。
func (t *SubAgentTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	// 防递归连锁：子代理上下文里禁止再次委派
	if depth, _ := ctx.Value(depthKey{}).(int); depth >= 1 {
		return entity.ToolResult{IsError: true, Content: "子代理上下文中不允许再次委派 task（防递归连锁）。请直接用现有工具完成任务。"}
	}
	var args taskArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 task: string）：%v", err)}
	}
	if strings.TrimSpace(args.Task) == "" {
		return entity.ToolResult{IsError: true, Content: "参数 task 不能为空；必须是自包含的任务描述（子代理看不到本会话历史）"}
	}

	n := t.seq.Add(1)
	subID := fmt.Sprintf("%s__sub%d", tctx.SessionID, n)
	emit := func(typ, detail string) {
		if tctx.Emit != nil {
			tctx.Emit(entity.Event{Kind: entity.EventMarker, Marker: &entity.Marker{Type: typ, Detail: detail}})
		}
	}
	emit("subagent_start", fmt.Sprintf("#%d %s", n, firstLine(args.Task)))

	sess := &subSession{
		id:      subID,
		runID:   tctx.RunID,
		model:   tctx.Model,
		cwd:     tctx.Cwd,
		approve: tctx.Approve,
		spill:   tctx.Spill,
	}
	subLoop := NewLoop(t.llmFor, excludeTool(t.tools, SubAgentToolName), t.hooks, t.statusBar, SubAgentSystemPrompt, t.limits, t.classify)

	turn, err := subLoop.RunTurn(context.WithValue(ctx, depthKey{}, 1), sess, args.Task)

	// 轨迹全文落盘：主上下文只见结论，审计者可经路径查看完整过程（可观测 NFR-2）
	trajPath := tctx.Spill(SubAgentToolName, sess.dumpJSONL())

	if err != nil {
		emit("subagent_done", fmt.Sprintf("#%d 失败: %v（轨迹 %s）", n, err, trajPath))
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf(
			"子代理未完成任务: %v\n子代理轨迹已保存到 %s（可用 read_file 查看其已完成的部分）。\n请换更小的任务分片重试，或自己直接执行。", err, trajPath)}
	}
	emit("subagent_done", fmt.Sprintf("#%d 完成（轨迹 %s）", n, trajPath))

	conclusion := turn.FinalText
	if t.maxOutput > 0 && len(conclusion) > t.maxOutput {
		conclusion = conclusion[:t.maxOutput] + fmt.Sprintf("\n…… [结论过长已截断，完整轨迹见 %s]", trajPath)
	}
	return entity.ToolResult{Content: fmt.Sprintf("[子代理结论]\n%s\n[完整轨迹: %s]", conclusion, trajPath)}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// ── 子代理会话运行时（内存版 port.SessionRuntime）──────────────────

// subSession 是子代理的轨迹载体：事件只存在于内存与 spill 落盘，
// 绝不进入主会话日志——这是「隔离」的落点。
type subSession struct {
	id      string
	runID   string
	model   string
	cwd     func() string
	approve func(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision
	spill   func(toolName, content string) string
	events  []entity.Event
	tokens  int
}

func (s *subSession) ID() string    { return s.id }
func (s *subSession) RunID() string { return s.runID }
func (s *subSession) Model() string { return s.model }
func (s *subSession) Cwd() string   { return s.cwd() }

func (s *subSession) AppendEvent(ev entity.Event) (entity.Event, error) {
	ev.Seq = len(s.events)
	ev.RunID = s.runID
	s.events = append(s.events, ev)
	return ev, nil
}

// EmitTemp 不把子代理的临时增量泄漏到主会话；完整轨迹仍由 spill 保存。
func (s *subSession) EmitTemp(entity.Event) {}

// ObservePrompt 保留接口语义；子代理没有投影压缩策略。
func (s *subSession) ObservePrompt(int) {}

// Messages 派生子代理的消息序列（单进程单回合，无需轨迹修复）。
func (s *subSession) Messages() []entity.Message {
	var msgs []entity.Message
	for _, ev := range s.events {
		switch {
		case ev.Message != nil:
			msgs = append(msgs, *ev.Message)
		case ev.Kind == entity.EventNotice:
			msgs = append(msgs, entity.Message{Role: "system", Content: ev.Note})
		}
	}
	return msgs
}

func (s *subSession) AddTokens(n int) int { s.tokens += n; return s.tokens }

func (s *subSession) Approve(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision {
	if s.approve == nil {
		return entity.ApprovalDecision{Approved: false, By: "policy", Reason: "子代理无审批通道"}
	}
	// 高风险操作升级给主会话的审批通道裁决（忠诚对象不变）
	return s.approve(ctx, toolName, "[子代理] "+summary, detail)
}

func (s *subSession) Spill(toolName, content string) string {
	if s.spill == nil {
		return "(无 spill 通道)"
	}
	return s.spill(toolName, content)
}

// dumpJSONL 把子代理完整轨迹序列化为 JSONL（中立格式，与主会话日志同构）。
func (s *subSession) dumpJSONL() string {
	var b strings.Builder
	fmt.Fprintf(&b, "{\"kind\":\"subagent_header\",\"id\":%q,\"events\":%d,\"tokens\":%d}\n", s.id, len(s.events), s.tokens)
	for _, ev := range s.events {
		raw, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// ── 工具视图过滤 ─────────────────────────────────────────────────

// excludeTool 返回去掉指定工具后的注册表视图（子代理看不到 task 工具，
// 与深度计数器构成双重防护）。
func excludeTool(base port.ToolRegistryPort, names ...string) port.ToolRegistryPort {
	ex := map[string]bool{}
	for _, n := range names {
		ex[n] = true
	}
	return &filteredRegistry{base: base, exclude: ex}
}

type filteredRegistry struct {
	base    port.ToolRegistryPort
	exclude map[string]bool
}

func (r *filteredRegistry) Definitions() []entity.ToolDefinition {
	var defs []entity.ToolDefinition
	for _, d := range r.base.Definitions() {
		if !r.exclude[d.Function.Name] {
			defs = append(defs, d)
		}
	}
	return defs
}

func (r *filteredRegistry) Lookup(name string) (port.Tool, bool) {
	if r.exclude[name] {
		return nil, false
	}
	return r.base.Lookup(name)
}

func (r *filteredRegistry) Names() []string {
	var names []string
	for _, n := range r.base.Names() {
		if !r.exclude[n] {
			names = append(names, n)
		}
	}
	return names
}
