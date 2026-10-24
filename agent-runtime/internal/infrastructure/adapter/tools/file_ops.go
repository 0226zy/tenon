package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

// ── read_file ────────────────────────────────────────────────────

// ReadFileTool 读取文件内容，带真实行号前缀（指引 §2.2 行号标注条款：
// 使模型可精确引用位置，为编辑工具的可靠性奠基）。
type ReadFileTool struct {
	maxOutput int
	maxRead   int
}

// NewReadFileTool 的 maxOutput 控制结果截断；可选 readMax 控制磁盘读取上限（默认 1 MiB）。
func NewReadFileTool(maxOutput int, readMax ...int) *ReadFileTool {
	maxRead := 1 << 20
	if len(readMax) > 0 && readMax[0] > 0 {
		maxRead = readMax[0]
	}
	return &ReadFileTool{maxOutput: maxOutput, maxRead: maxRead}
}

func (t *ReadFileTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "read_file",
			Description: `读取文件内容，每行带真实行号前缀（如 "  42→..."），行号可直接用于引用与编辑定位。
何时使用：查看源码、配置、日志、任何文本文件的内容。
何时不要用：只想定位文件用 glob；只想找包含某关键词的位置用 grep；读目录列表用 bash 的 ls。
参数：path 必填（绝对路径，或相对当前工作目录）；offset 为起始行号（1 起，默认 1）；limit 为读取行数（默认 400，最大 2000）。
失败形态：文件不存在、超过读取上限、为二进制或不可读时返回 IsError=true 与原因；内容超长时截断并给出 spill 文件路径。`,
			Parameters: schema(map[string]any{
				"path":   strProp("文件路径，例如 \"internal/agent/loop.go\" 或 \"/abs/path/config.yaml\""),
				"offset": intProp("起始行号（1 起），默认 1"),
				"limit":  intProp("读取行数，默认 400，最大 2000"),
			}, "path"),
		},
	}
}

func (t *ReadFileTool) ConcurrencySafe() bool { return true }

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *ReadFileTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args readFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 path: string）：%v", err)}
	}
	if args.Path == "" {
		return entity.ToolResult{IsError: true, Content: "参数 path 不能为空"}
	}
	if args.Offset <= 0 {
		args.Offset = 1
	}
	if args.Limit <= 0 {
		args.Limit = 400
	}
	if args.Limit > 2000 {
		args.Limit = 2000
	}

	p := absPath(tctx.Cwd(), args.Path)
	raw, tooLarge, err := readFileBounded(p, int64(t.maxRead))
	if err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("读取文件失败: %v", err)}
	}
	if tooLarge {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("文件 %s 超过 read_max_bytes 上限 %d 字节，已拒绝读取", p, t.maxRead)}
	}
	if isBinary(raw) {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("%s 是二进制文件，无法用 read_file 查看；如需处理请用 bash 中的专用工具（file/xxd 等）", p)}
	}

	lines := strings.Split(string(raw), "\n")
	total := len(lines)
	if args.Offset > total {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("offset %d 超出文件总行数 %d", args.Offset, total)}
	}
	end := args.Offset + args.Limit - 1
	if end > total {
		end = total
	}

	var b strings.Builder
	for i := args.Offset; i <= end; i++ {
		fmt.Fprintf(&b, "%6d→%s\n", i, lines[i-1])
	}
	if end < total {
		fmt.Fprintf(&b, "…… [共 %d 行，已显示 %d-%d；用 offset=%d 继续读取] ……\n", total, args.Offset, end, end+1)
	}
	content := TruncateOutput(b.String(), t.maxOutput, func() string { return tctx.Spill("read_file", b.String()) })
	return entity.ToolResult{Content: content}
}

// isBinary 检查前 8KB 是否含 NUL 字节。
func isBinary(raw []byte) bool {
	n := len(raw)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if raw[i] == 0 {
			return true
		}
	}
	return false
}

// readFileBounded 先按 stat 拒绝超限文件，再以 limit+1 有界读处理读取期间增长的文件。
func readFileBounded(path string, maxBytes int64) (raw []byte, tooLarge bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	if info.Size() > maxBytes {
		return nil, true, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	raw, err = io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, true, nil
	}
	return raw, false, nil
}

// ── write_file ───────────────────────────────────────────────────

// WriteFileTool 创建或整体重写文件（指引 §2.3.1 #4：
// 写入后自动触发语法检查，结果并入返回值——即时反馈）。
type WriteFileTool struct{}

func NewWriteFileTool() *WriteFileTool { return &WriteFileTool{} }

func (t *WriteFileTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "write_file",
			Description: `创建新文件或整体重写已有文件，写入后自动做语法检查（go→gofmt、python→py_compile、shell→bash -n、js→node --check），检查结果并入返回值。
何时使用：创建新文件；或对小文件做整体重写。
何时不要用：局部修改已有文件请用 edit_file（old_string→new_string），整文件重写既浪费 token 又容易引入意外改动。
参数：path 与 content（完整文件内容，多行内容直接作为结构化参数传递，不要转义拼接）。
失败形态：路径不可写时返回 IsError=true 与原因；语法检查未通过时写入仍成功，但返回值包含检查输出，须据此修正。`,
			Parameters: schema(map[string]any{
				"path":    strProp("文件路径，例如 \"cmd/server/main.go\""),
				"content": strProp("完整文件内容"),
			}, "path", "content"),
		},
	}
}

func (t *WriteFileTool) ConcurrencySafe() bool { return false }

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *WriteFileTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args writeFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 path/content: string）：%v", err)}
	}
	if args.Path == "" {
		return entity.ToolResult{IsError: true, Content: "参数 path 不能为空"}
	}
	p := absPath(tctx.Cwd(), args.Path)
	if dir := filepath.Dir(p); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return entity.ToolResult{IsError: true, Content: fmt.Sprintf("创建父目录失败: %v", err)}
		}
	}
	if err := os.WriteFile(p, []byte(args.Content), 0o644); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("写入文件失败: %v", err)}
	}
	result := fmt.Sprintf("已写入 %s（%d 字节）", p, len(args.Content))
	if check := syntaxCheck(p); check != "" {
		result += "\n[语法检查] " + check
	}
	return entity.ToolResult{Content: result}
}

// syntaxCheck 是写文件后的即时反馈（指引 §2.3.1 #4 / §3.4 PostToolUse 挂点）。
// 检查器不存在时静默跳过；检查失败返回输出，成功返回空串。
func syntaxCheck(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		cmd = exec.CommandContext(ctx, "gofmt", "-l", path)
	case ".py":
		cmd = exec.CommandContext(ctx, "python3", "-m", "py_compile", path)
	case ".sh":
		cmd = exec.CommandContext(ctx, "bash", "-n", path)
	case ".js", ".mjs", ".cjs":
		if _, err := exec.LookPath("node"); err != nil {
			return ""
		}
		cmd = exec.CommandContext(ctx, "node", "--check", path)
	default:
		return ""
	}
	if _, err := exec.LookPath(cmd.Args[0]); err != nil {
		return ""
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if filepath.Ext(path) == ".go" && err == nil && text != "" {
		return fmt.Sprintf("gofmt 认为文件格式不规范（%s），请运行 gofmt -w 修正", text)
	}
	if err != nil || text != "" {
		return fmt.Sprintf("检查未通过: %s", text)
	}
	return "通过"
}

// ── edit_file ────────────────────────────────────────────────────

// EditFileTool 局部修改文件（指引 §2.3.3：old_string→new_string 方案，
// 存在且唯一则成功，否则失败；不唯一时返回带候选位置的结构化错误）。
type EditFileTool struct{}

func NewEditFileTool() *EditFileTool { return &EditFileTool{} }

func (t *EditFileTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "edit_file",
			Description: `局部修改文件：把 old_string 替换为 new_string。old_string 必须在文件中真实存在且唯一（除非 replace_all=true），否则失败。
何时使用：修改已有文件的一个或多个局部位置——这是修改代码的首选方式。
何时不要用：创建新文件用 write_file；大段重写整个文件用 write_file。
要点：old_string 必须逐字复制自 read_file 的输出内容（不含行号前缀），缩进与空白必须一致；
多处不同位置的修改请分多次调用，或并行发起相互独立的调用。
失败形态：未找到匹配时返回 IsError=true；匹配不唯一时返回全部候选位置的行号，请扩大 old_string 的上下文后重试。`,
			Parameters: schema(map[string]any{
				"path":        strProp("文件路径"),
				"old_string":  strProp("要被替换的原文（逐字，含缩进），例如 \"func main() {\""),
				"new_string":  strProp("替换后的新文本"),
				"replace_all": boolProp("可选，true 时替换全部出现位置，默认 false（要求唯一匹配）"),
			}, "path", "old_string", "new_string"),
		},
	}
}

func (t *EditFileTool) ConcurrencySafe() bool { return false }

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t *EditFileTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args editFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 path/old_string/new_string）：%v", err)}
	}
	if args.Path == "" || args.OldString == "" {
		return entity.ToolResult{IsError: true, Content: "参数 path 与 old_string 不能为空"}
	}
	p := absPath(tctx.Cwd(), args.Path)
	raw, err := os.ReadFile(p)
	if err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("读取文件失败: %v", err)}
	}
	content := string(raw)
	count := strings.Count(content, args.OldString)
	if count == 0 {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf(
			"未在 %s 中找到 old_string。请用 read_file 重新读取该文件，逐字复制要替换的原文（注意缩进与空白，不要含行号前缀）", p)}
	}
	if count > 1 && !args.ReplaceAll {
		// 结构化错误：给出全部候选行号（指引 §2.3.3）
		var atLines []string
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if strings.Contains(line, args.OldString) {
				atLines = append(atLines, fmt.Sprintf("%d", i+1))
			} else {
				// old_string 可能跨行：粗定位其首行
				firstLine := strings.SplitN(args.OldString, "\n", 2)[0]
				if strings.Contains(line, firstLine) {
					atLines = append(atLines, fmt.Sprintf("%d", i+1))
				}
			}
		}
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf(
			"old_string 在 %s 中出现 %d 次（候选起始行：%s）。请扩大 old_string 的上下文（多带几行）使其唯一，或确认后设 replace_all=true",
			p, count, strings.Join(atLines, ", "))}
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldString, args.NewString)
	} else {
		updated = strings.Replace(content, args.OldString, args.NewString, 1)
	}
	if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("写回文件失败: %v", err)}
	}
	result := fmt.Sprintf("已修改 %s（替换 %d 处）", p, count)
	if check := syntaxCheck(p); check != "" {
		result += "\n[语法检查] " + check
	}
	return entity.ToolResult{Content: result}
}
