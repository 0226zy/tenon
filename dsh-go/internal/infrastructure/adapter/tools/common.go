// Package tools 实现 Coding Agent 最小核心工具集（指引 §2.3.1）：
// bash（持久终端会话）、read_file（行号前缀）、write_file（即时语法反馈）、
// edit_file（old_string→new_string）、glob、grep。
//
// 通用约定：
//   - 一切失败都以 ToolResult{IsError:true} 返回——错误是模型的输入（指引 §4.3）；
//   - 输出超长时保留头尾、中间以提示行替代，全文经 ToolContext.Spill 落盘（指引 §2.4）；
//   - 只读工具（read_file / glob / grep）声明 ConcurrencySafe，可并行执行；
//   - 工具描述遵循指引 §2.3.2：职责 + 使用边界 + 负面清单 + 参数示例 + 失败形态。
package tools

import (
	"fmt"
	"strings"
)

// TruncateOutput 长输出截断：保留头部（错误上下文）与尾部（错误总结），
// 中间以提示行替代；全文经 spill 落盘并告知路径，模型可用 read_file 按需查看。
func TruncateOutput(s string, maxBytes int, spill func() string) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	head := maxBytes * 2 / 5
	tail := maxBytes * 2 / 5
	path := ""
	if spill != nil {
		path = spill()
	}
	marker := fmt.Sprintf("\n…… [中间省略 %d 字节；完整输出已保存到 %s，可用 read_file 分段查看] ……\n",
		len(s)-head-tail, path)
	return s[:head] + marker + s[len(s)-tail:]
}

// absPath 把可能为相对路径的 p 解析为绝对路径（相对 cwd）。
func absPath(cwd, p string) string {
	if p == "" {
		return cwd
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	return cwd + "/" + p
}

// schema 小助手：构造 JSON Schema object。
// required 缺省时必须是 [] 而非 null——部分模型 API（如 DeepSeek）校验
// "required" 字段类型为 array，null 会直接 400。
func schema(props map[string]any, required ...string) map[string]any {
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
