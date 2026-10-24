package session

import (
	"fmt"

	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
)

// 上下文投影压缩（评审 P1：上下文压缩，与 spill、task 互补）。
//
// 设计要点：
//   - 压缩是「投影策略」而非「日志改写」：append-only 事件日志永远完整
//    （fork/replay/search/审计不受影响），只有派生模型上下文时压缩。
//   - 触发条件：最近一次请求的 prompt_tokens 达到组合根注入的阈值（compressAt，0 = 禁用）。
//   - 动作：较早的长工具输出 / 长 assistant 文本替换为「头部摘录 + spill 全文路径」，
//     原文经现有 spill 机制落盘，模型需要时可用 read 工具取回——信息不丢失，只是离场。
//   - 保护：首条 user 消息（原始需求）与最近 keepRecent 条消息（当前工作集）逐字保留；
//     tool_call 配对结构（role/ToolCallID/Name）不动，轨迹完整性修复不受影响。
//   - 幂等：压缩结果按事件 seq 缓存，重复投影不会重复 spill。

const (
	// compressMinToolLen 是可压缩 tool 消息的最小正文长度（更短的不值得压）。
	compressMinToolLen = 240
	// compressMinAssistantLen 是可压缩 assistant 纯文本的最小长度。
	compressMinAssistantLen = 800
	// compressHeadKeep 是压缩后保留的头部摘录长度。
	compressHeadKeep = 160
)

// indexedMessage 把投影消息与其来源事件 seq 绑定（压缩缓存的键）。
type indexedMessage struct {
	seq int
	msg entity.Message
}

// compressionConfig 是 Manager 上的压缩策略（组合根启动期注入）。
type compressionConfig struct {
	at         int // 最近一次 prompt_tokens 阈值；0 = 禁用
	keepRecent int // 尾部保留 verbatim 的消息条数
}

// SetCompression 注入上下文压缩策略（组合根调用；at=0 表示禁用）。
func (m *Manager) SetCompression(atTokens, keepRecent int) {
	m.compression = compressionConfig{at: atTokens, keepRecent: keepRecent}
}

// compressActive 报告本会话当前是否应压缩投影。
func (r *Runtime) compressActive() bool {
	cfg := r.manager.compression
	if cfg.at <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastPrompt >= cfg.at
}

// compressIndexed 对原始投影（修复前）做压缩；返回压缩条数。
// 只在 compressActive 为真时调用；首次激活时向轨迹注入一条说明 notice
// （告诉模型旧输出去了哪里、如何取回），并广播 progress marker 供 UI 展示。
func (r *Runtime) compressIndexed(raw []indexedMessage) int {
	cfg := r.manager.compression
	keep := cfg.keepRecent
	if keep < 0 {
		keep = 0
	}

	// 尾部保护窗：最后 keep 条不压
	cutoff := len(raw) - keep
	if cutoff < 0 {
		cutoff = 0
	}

	compressed := 0
	for i := range raw {
		if i >= cutoff {
			break
		}
		m := &raw[i].msg
		// 首条 user 消息（原始需求）逐字保留
		if m.Role == "user" && i == firstUserIndex(raw) {
			continue
		}
		switch {
		case m.Role == "tool" && len(m.Content) > compressMinToolLen:
			m.Content = r.compressedStub(raw[i].seq, m.Name, m.Content)
			compressed++
		case m.Role == "assistant" && len(m.ToolCalls) == 0 && len(m.Content) > compressMinAssistantLen:
			m.Content = r.compressedStub(raw[i].seq, "assistant", m.Content)
			compressed++
		}
	}

	if compressed > 0 {
		r.notifyCompressionOnce(compressed)
	}
	return compressed
}

// compressedStub 生成（并缓存）一条消息的压缩替身：头部摘录 + spill 全文路径。
func (r *Runtime) compressedStub(seq int, name, content string) string {
	r.mu.Lock()
	if stub, ok := r.compressCache[seq]; ok {
		r.mu.Unlock()
		return stub
	}
	r.mu.Unlock()

	if name == "" {
		name = "message"
	}
	path := r.Spill(name, content)
	head := content
	if len(head) > compressHeadKeep {
		head = head[:compressHeadKeep]
	}
	stub := fmt.Sprintf("%s\n…[上下文压缩：原文 %d 字符已离场，全文可用 read 工具查看 %s]", head, len(content), path)

	r.mu.Lock()
	r.compressCache[seq] = stub
	r.mu.Unlock()
	return stub
}

// notifyCompressionOnce 在压缩首次激活时注入一条说明 notice（进入模型上下文）
// 与一条 progress marker（仅供 UI）；重复投影不重复通知。
func (r *Runtime) notifyCompressionOnce(n int) {
	r.mu.Lock()
	if r.compressNoticed {
		r.mu.Unlock()
		return
	}
	r.compressNoticed = true
	r.mu.Unlock()

	_, _ = r.AppendEvent(entity.Event{Kind: entity.EventNotice, Note: "[Harness] 上下文投影压缩已激活：较早的长工具输出已替换为「摘录 + 全文路径」" +
		"（用 read 工具可取回原文）；会话事件日志完整保留，未做任何删除。"})
	_, _ = r.AppendEvent(entity.Event{Kind: entity.EventMarker, Marker: &entity.Marker{
		Type: "progress", Detail: fmt.Sprintf("上下文压缩已激活 · 本轮压缩 %d 条早期消息", n),
	}})
}

// firstUserIndex 返回首条 user 消息的下标（无则 -1）。
func firstUserIndex(raw []indexedMessage) int {
	for i := range raw {
		if raw[i].msg.Role == "user" {
			return i
		}
	}
	return -1
}

// 压缩状态初始化（load 时调用）。
func newCompressCache() map[int]string { return map[int]string{} }
