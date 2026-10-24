// Package agent 实现 Agent 核心循环（dsh Control 层的等价物，指引 §2.1）。
//
// 关键不变量：
//   - 上下文 = 静态前缀（系统提示词 + 工具定义，逐字节稳定）+ 状态栏（追加在末尾）+ 轨迹；
//   - 每个 tool_call 必有配对的 tool result 消息（含错误形态）；
//   - 循环唯一正常终止条件是模型不再发起工具调用；
//     轮数 / 预算 / 活性 / 熔断均为 Harness 保护性终止；
//   - 工具层错误不终止会话，作为普通工具结果回注（指引 §4.3）。
package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/0226zy/magents/internal/domain/derr"
	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// Limits 是循环的保护性终止与恢复阈值，全部来自配置。
type Limits struct {
	MaxIterations          int
	TokenBudget            int // 每一个 Run 独立计算，不跨回合累计。
	LivelockRepeat         int
	MaxRetries             int
	CircuitBreakerFailures int
}

// TurnResult 是一个 Run 的结构化执行结果。即使返回 error，已经产生的统计也保留。
type TurnResult struct {
	FinalText        string `json:"final_text"`
	Iterations       int    `json:"iterations"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Model            string `json:"model"`
	ErrorCode        string `json:"error_code,omitempty"`
}

// ErrorCodeOf 把内部保护性终止映射为稳定的跨进程错误码。
func ErrorCodeOf(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, derr.ErrMaxIterations):
		return "ITER_LIMIT"
	case errors.Is(err, derr.ErrBudgetExhausted):
		return "TOKEN_BUDGET"
	case errors.Is(err, derr.ErrLivelock):
		return "LIVELOCK"
	case errors.Is(err, derr.ErrCircuitOpen):
		return "CIRCUIT_OPEN"
	case errors.Is(err, context.Canceled):
		return "CANCELLED"
	default:
		return "LLM_ERROR"
	}
}

// ErrorClassifier 判别模型调用错误是否可重试，并给出服务端建议等待时长。
type ErrorClassifier func(err error) (retryable bool, retryAfter time.Duration)

// Loop 是 Agent 循环驱动器。本身无会话状态，可并发服务多个会话。
type Loop struct {
	llmFor       port.LLMResolver
	tools        port.ToolRegistryPort
	hooks        []port.Hook
	statusBar    port.StatusBarPort
	systemPrompt string
	limits       Limits
	classify     ErrorClassifier
	streaming    atomic.Bool
}

// NewLoop 构造循环驱动器。
func NewLoop(llmFor port.LLMResolver, tools port.ToolRegistryPort, hooks []port.Hook, statusBar port.StatusBarPort, systemPrompt string, limits Limits, classify ErrorClassifier) *Loop {
	return &Loop{llmFor: llmFor, tools: tools, hooks: hooks, statusBar: statusBar, systemPrompt: systemPrompt, limits: limits, classify: classify}
}

// EnableStreaming 在运行时原子切换流式推理。
func (l *Loop) EnableStreaming(enabled bool) { l.streaming.Store(enabled) }

// SetStreaming 是 EnableStreaming 的兼容别名。
func (l *Loop) SetStreaming(enabled bool) { l.EnableStreaming(enabled) }

// RunTurn 执行一个 ReAct 回合。文本增量只广播临时事件，完整 assistant 消息仍按原轨迹落盘。
func (l *Loop) RunTurn(ctx context.Context, sess port.SessionRuntime, userText string) (result TurnResult, retErr error) {
	defer func() {
		if retErr != nil {
			result.ErrorCode = ErrorCodeOf(retErr)
		}
	}()

	runID := sess.RunID()
	if _, err := sess.AppendEvent(entity.Event{RunID: runID, Kind: entity.EventMessage,
		Message: &entity.Message{Role: "user", Content: userText}}); err != nil {
		return result, err
	}

	llm, err := l.llmFor(sess.Model())
	if err != nil {
		l.notice(sess, fmt.Sprintf("模型解析失败: %v", err))
		return result, err
	}
	result.Model = llm.Model()

	var lastFP string
	consecutiveFP := 0
	consecutiveModelFailures := 0
	turnTokens := 0

	for iter := 1; ; iter++ {
		if iter > l.limits.MaxIterations {
			l.notice(sess, fmt.Sprintf("已达最大迭代轮数 %d，回合被保护性终止。如需继续，请发送新消息让 Agent 接着做。", l.limits.MaxIterations))
			return result, derr.ErrMaxIterations
		}
		result.Iterations = iter

		messages := l.buildContext(ctx, sess)
		resp, err := l.chatWithRecovery(ctx, sess, llm, messages, &consecutiveModelFailures)
		if err != nil {
			l.notice(sess, fmt.Sprintf("模型调用失败（已尝试自动恢复）: %v", err))
			return result, err
		}
		consecutiveModelFailures = 0
		if resp == nil || len(resp.Choices) == 0 {
			return result, errors.New("模型响应没有 choices")
		}
		assistant := resp.Choices[0].Message
		if _, err := sess.AppendEvent(entity.Event{RunID: runID, Kind: entity.EventMessage, Message: &assistant}); err != nil {
			return result, err
		}

		result.PromptTokens += resp.Usage.PromptTokens
		result.CompletionTokens += resp.Usage.CompletionTokens
		used := resp.Usage.TotalTokens
		if used <= 0 {
			used = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
		}
		turnTokens += used
		total := sess.AddTokens(used)
		sess.ObservePrompt(resp.Usage.PromptTokens)
		_, _ = sess.AppendEvent(entity.Event{RunID: runID, Kind: entity.EventMarker, Marker: &entity.Marker{
			Type: "progress", RunID: runID, PromptTokens: resp.Usage.PromptTokens,
			Detail: fmt.Sprintf("第 %d 轮 · 模型 %s · 本次 %d tokens · 会话累计 %d", iter, llm.Model(), used, total)}})
		if l.limits.TokenBudget > 0 && turnTokens > l.limits.TokenBudget {
			l.notice(sess, fmt.Sprintf("本回合 token 累计 %d，超出预算 %d，回合被保护性终止。", turnTokens, l.limits.TokenBudget))
			return result, derr.ErrBudgetExhausted
		}

		if len(assistant.ToolCalls) == 0 {
			result.FinalText = assistant.Content
			return result, nil
		}

		results := l.executeCalls(ctx, sess, assistant.ToolCalls)
		for i, tc := range assistant.ToolCalls {
			if _, err := sess.AppendEvent(entity.Event{RunID: runID, Kind: entity.EventMessage,
				Message: &entity.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name,
					Content: results[i].Content, IsError: results[i].IsError}}); err != nil {
				return result, err
			}

			fp := fingerprint(tc)
			if fp == lastFP {
				consecutiveFP++
			} else {
				consecutiveFP = 1
				lastFP = fp
			}
		}
		if consecutiveFP == l.limits.LivelockRepeat {
			l.notice(sess, fmt.Sprintf(
				"[Harness 警告] 检测到相同工具调用已连续出现 %d 次，疑似无进展循环。请改变策略：换工具、换参数，或先总结已知信息再决定下一步。",
				consecutiveFP))
		}
		if consecutiveFP > l.limits.LivelockRepeat+1 {
			l.notice(sess, "无进展循环未自行纠正，回合被强制打断。")
			return result, derr.ErrLivelock
		}
	}
}

// buildContext 组装上下文：静态前缀（系统提示词）+ 状态栏 + 轨迹。
func (l *Loop) buildContext(ctx context.Context, sess port.SessionRuntime) []entity.Message {
	messages := make([]entity.Message, 0, 64)
	messages = append(messages, entity.Message{Role: "system", Content: l.systemPrompt})
	if l.statusBar != nil {
		messages = append(messages, entity.Message{Role: "system", Content: l.statusBar.Snapshot(ctx, sess.Cwd())})
	}
	return append(messages, sess.Messages()...)
}

// chatWithRecovery 是模型调用的恢复路径：指数退避 + 抖动，尊重 Retry-After。
func (l *Loop) chatWithRecovery(ctx context.Context, sess port.SessionRuntime, llm port.LLMPort, messages []entity.Message, consecutiveFailures *int) (*entity.ChatResponse, error) {
	base := time.Second
	for attempt := 0; ; attempt++ {
		var resp *entity.ChatResponse
		var err error
		if l.streaming.Load() {
			resp, err = llm.ChatStream(ctx, messages, l.tools.Definitions(), func(text string) {
				if text == "" {
					return
				}
				sess.EmitTemp(entity.Event{Seq: -1, Kind: entity.EventDelta, RunID: sess.RunID(), Text: text, Delta: text})
			})
		} else {
			resp, err = llm.Chat(ctx, messages, l.tools.Definitions())
		}
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		retryable, hint := false, time.Duration(0)
		if l.classify != nil {
			retryable, hint = l.classify(err)
		}
		if !retryable {
			return nil, fmt.Errorf("%w: %v", derr.ErrNotRetryable, err)
		}
		*consecutiveFailures = *consecutiveFailures + 1
		if *consecutiveFailures >= l.limits.CircuitBreakerFailures {
			return nil, fmt.Errorf("%w（模型调用连续失败 %d 次）: %v", derr.ErrCircuitOpen, *consecutiveFailures, err)
		}
		if attempt >= l.limits.MaxRetries {
			return nil, fmt.Errorf("静默重试 %d 次后仍失败: %w", attempt, err)
		}

		wait := base << uint(attempt)
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		wait += time.Duration(rand.Int63n(int64(wait/4 + 1)))
		if hint > wait {
			wait = hint
		}
		l.notice(sess, fmt.Sprintf("模型调用遇到可重试错误，%s 后自动重试（第 %d/%d 次）……", wait.Round(time.Second), attempt+1, l.limits.MaxRetries))

		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		}
	}
}

// executeCalls 执行一批工具调用：全部声明并发安全时并行执行，否则串行。
func (l *Loop) executeCalls(ctx context.Context, sess port.SessionRuntime, calls []entity.ToolCall) []entity.ToolResult {
	results := make([]entity.ToolResult, len(calls))
	parallel := len(calls) > 1
	for _, tc := range calls {
		tool, ok := l.tools.Lookup(tc.Function.Name)
		if !ok || !tool.ConcurrencySafe() {
			parallel = false
			break
		}
	}

	var wg sync.WaitGroup
	for i, tc := range calls {
		if parallel {
			wg.Add(1)
			go func(i int, tc entity.ToolCall) {
				defer wg.Done()
				results[i] = l.executeOne(ctx, sess, tc)
			}(i, tc)
		} else {
			results[i] = l.executeOne(ctx, sess, tc)
		}
	}
	wg.Wait()
	return results
}

// executeOne 把幻觉调用、参数畸形、钩子拦截与执行异常全部转成工具结果。
func (l *Loop) executeOne(ctx context.Context, sess port.SessionRuntime, tc entity.ToolCall) entity.ToolResult {
	runID := sess.RunID()
	tctx := &port.ToolContext{
		SessionID: sess.ID(),
		RunID:     runID,
		Cwd:       sess.Cwd,
		Model:     sess.Model(),
		Approve: func(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision {
			return sess.Approve(ctx, toolName, summary, detail)
		},
		Spill: func(toolName, content string) string { return sess.Spill(toolName, content) },
		Emit: func(ev entity.Event) {
			if ev.RunID == "" {
				ev.RunID = runID
			}
			_, _ = sess.AppendEvent(ev)
		},
	}

	tool, ok := l.tools.Lookup(tc.Function.Name)
	if !ok {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf(
			"工具 %q 不存在。可用工具：%v。请改用上述工具之一。", tc.Function.Name, l.tools.Names())}
	}
	for _, h := range l.hooks {
		if reason := h.PreToolUse(ctx, tc.Function.Name, tc.Function.Arguments, tctx); reason != "" {
			return entity.ToolResult{IsError: true, Content: reason}
		}
		if ctx.Err() != nil {
			return entity.ToolResult{IsError: true, Content: "回合已被取消"}
		}
	}

	start := time.Now()
	result := tool.Execute(ctx, tc.Function.Arguments, tctx)
	l.progress(sess, fmt.Sprintf("工具 %s · 耗时 %s", tc.Function.Name, time.Since(start).Round(time.Millisecond)))
	for _, h := range l.hooks {
		h.PostToolUse(ctx, tc.Function.Name, tc.Function.Arguments, tctx, &result)
	}
	return result
}

func fingerprint(tc entity.ToolCall) string {
	sum := sha1.Sum([]byte(tc.Function.Name + "\x00" + tc.Function.Arguments))
	return hex.EncodeToString(sum[:8])
}

func (l *Loop) notice(sess port.SessionRuntime, text string) {
	_, _ = sess.AppendEvent(entity.Event{RunID: sess.RunID(), Kind: entity.EventNotice, Note: text})
}

func (l *Loop) progress(sess port.SessionRuntime, text string) {
	_, _ = sess.AppendEvent(entity.Event{RunID: sess.RunID(), Kind: entity.EventMarker,
		Marker: &entity.Marker{Type: "progress", Detail: text, RunID: sess.RunID()}})
}
