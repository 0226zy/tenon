// Package policy 实现执行边界与审批机制（指引 FR-7 / §5.3 / §5.4）：
//
//   - 命令语义解析：把 bash 命令切成段（；、&&、||、|），递归展开 $() 与反引号，
//     在语义层判断真实效果，而不是关键字黑名单（`$(echo rm) -rf /` 之类绕不过）；
//   - 三档审批模式（对齐 Codex CLI）：suggest 事事请示 / auto-edit 只拦高风险 /
//     full-auto 全部放行（仍记录审批事件供审计）；
//   - 过程性防护：rm -rf、强推、写系统目录、管道执行远程脚本等破坏性捷径必须审批。
//
// 实现为 PreToolUse 钩子，与工具本身解耦（工具不内嵌权限逻辑）。
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/0226zy/magents/internal/domain/entity"
	"github.com/0226zy/magents/internal/domain/port"
)

// 审批模式。
const (
	ModeSuggest  = "suggest"   // 一切变更类操作都需人类批准
	ModeAutoEdit = "auto-edit" // 低风险自动执行，高风险请示
	ModeFullAuto = "full-auto" // 全部自动（审批事件仍落盘供审计）
)

// defaultReadOnlyTools 是内置只读工具（永远不触发审批）。
var defaultReadOnlyTools = map[string]bool{"read_file": true, "glob": true, "grep": true, "preview": true}

// Hook 是 PreToolUse 审批钩子。
type Hook struct {
	// ModeOf 返回会话当前审批模式（会话运行中可经 API 切换）。
	ModeOf func(sessionID string) string

	mu           sync.RWMutex
	readOnly     map[string]bool
	forceApprove map[string]bool
}

// NewHook 构造审批钩子。
func NewHook(modeOf func(sessionID string) string) *Hook {
	h := &Hook{ModeOf: modeOf, readOnly: map[string]bool{}, forceApprove: map[string]bool{}}
	for name := range defaultReadOnlyTools {
		h.readOnly[name] = true
	}
	return h
}

// AddReadOnlyTools 把工具名追加进只读名单（组合根启动期调用；
// 仅用于确认不修改环境的工具，如声明 read_only 的 MCP server 工具）。
func (h *Hook) AddReadOnlyTools(names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range names {
		h.readOnly[n] = true
	}
}

func (h *Hook) isReadOnly(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.readOnly[name]
}

// AddForceApproveTools 把工具名加入强制审批名单（组合根启动期调用）：
// 名单内工具在任何审批模式（含 full-auto）下都必须经人工裁决。
// 用于触达外部系统的高风险工具，如 deploy。
func (h *Hook) AddForceApproveTools(names ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range names {
		h.forceApprove[n] = true
	}
}

func (h *Hook) isForceApprove(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.forceApprove[name]
}

// PreToolUse 实现 port.Hook：返回非空串表示阻止执行（文本作为工具错误回注给模型）。
func (h *Hook) PreToolUse(ctx context.Context, toolName, argsJSON string, tctx *port.ToolContext) string {
	if h.isReadOnly(toolName) {
		return ""
	}
	mode := ModeAutoEdit
	if h.ModeOf != nil {
		if m := h.ModeOf(tctx.SessionID); m != "" {
			mode = m
		}
	}

	level, summary, detail := assess(toolName, argsJSON, tctx.Cwd())

	needApproval := false
	switch mode {
	case ModeFullAuto:
		needApproval = false
	case ModeSuggest:
		needApproval = true
	default: // auto-edit
		needApproval = level == entity.RiskHigh
	}
	// 强制审批工具（如 deploy）：任何模式（含 full-auto）都需人工裁决，审批事件落盘可审计。
	if h.isForceApprove(toolName) {
		needApproval = true
	}
	if !needApproval {
		return ""
	}

	decision := tctx.Approve(ctx, toolName, fmt.Sprintf("[%s] %s", level, summary), detail)
	if !decision.Approved {
		return fmt.Sprintf("操作被拒绝（%s）。不要重试相同操作；请向用户说明情况，或改用经用户认可的其他方案。",
			decision.Reason)
	}
	return ""
}

// PostToolUse 实现 port.Hook（本钩子无事后逻辑）。
func (h *Hook) PostToolUse(ctx context.Context, toolName, argsJSON string, tctx *port.ToolContext, result *entity.ToolResult) {
}

// assess 评估一次工具调用的风险，返回级别与一句话摘要。
func assess(toolName, argsJSON, cwd string) (entity.RiskLevel, string, string) {
	switch toolName {
	case "bash":
		var args struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		return assessShellCommand("bash", args.Command)
	case "run_tests":
		var args struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		if strings.TrimSpace(args.Command) == "" {
			// 缺省命令是固化的探测白名单（go test ./... 等），判低风险。
			return entity.RiskLow, "run_tests: 自动探测并运行测试", ""
		}
		// 自定义命令与 bash 同等对待：语义级风险解析。
		return assessShellCommand("run_tests", args.Command)
	case "write_file", "edit_file":
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		p := args.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		root := filepath.Clean(cwd)
		if p == root || (strings.HasPrefix(p, root+string(filepath.Separator))) {
			return entity.RiskLow, fmt.Sprintf("%s: %s（工作区内）", toolName, p), ""
		}
		return entity.RiskHigh, fmt.Sprintf("%s: 写入工作区之外的文件 %s", toolName, p), ""
	case "task":
		// 委派本身不改变环境：子代理的每次工具调用会独立过本钩子与审批通道，
		// 因此 task 本体判低风险。
		return entity.RiskLow, "task: 委派子代理（其内部操作将独立评估）", ""
	case "preview":
		return entity.RiskLow, "preview: 启动或刷新工作区预览", argsJSON
	case "deploy":
		// 部署触达外部系统：恒高风险（配合强制审批名单，任何模式都需人工裁决）。
		var args struct {
			Target string `json:"target"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		return entity.RiskHigh, fmt.Sprintf("deploy: 部署到目标 %q", args.Target), argsJSON
	default:
		return entity.RiskHigh, fmt.Sprintf("未知工具 %s（默认高风险，需审批）", toolName), argsJSON
	}
}

// assessShellCommand 对 shell 类工具的命令做语义级风险解析（bash / run_tests 共用）。
func assessShellCommand(toolName, command string) (entity.RiskLevel, string, string) {
	report := Analyze(command)
	if report.Level == entity.RiskHigh {
		return entity.RiskHigh, toolName + ": " + report.Reasons[0],
			fmt.Sprintf("命令：%s\n风险点：\n- %s", command, strings.Join(report.Reasons, "\n- "))
	}
	return entity.RiskLow, toolName + ": " + firstLine(command), command
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ── 命令语义解析（指引 §5.3：禁止关键字黑名单）──────────────────────

// Report 是一条 bash 命令的风险评估结果。
type Report struct {
	Level   entity.RiskLevel
	Reasons []string
}

// systemDirs 是绝对路径写入的高危目标。
var systemDirs = []string{"/etc", "/usr", "/bin", "/sbin", "/boot", "/System", "/Library", "/var", "/private"}

// AnalyzeCommand 在语义层解析命令的真实效果：
// 先按引用规则切段（；、&&、||、|），再递归展开命令替换（$() 与反引号），
// 逐段检查危险效果。任何一段命中即整条命令判高风险。
func AnalyzeCommand(command string) Report {
	var r Report
	analyzeSegment(command, &r, 0)
	if r.Level == "" {
		r.Level = entity.RiskLow
	}
	return r
}

// destructivePrimitives 是破坏性原语在原始文本中的痕迹。
// 命令替换（$() / 反引号）的展开结果静态不可知——`$(echo rm) -rf /` 之类
// 组合必须按高风险处理（指引 §5.3 点名的黑名单绕过样例）。
var destructivePrimitives = []string{"rm ", "rm\t", "-rf", "-fr", "git push", "git reset", "git clean", "dd ", "mkfs", "chmod", "chown"}

func analyzeSegment(cmd string, r *Report, depth int) {
	if depth > 4 {
		return
	}
	// 命令替换与破坏性原语同时出现：展开结果不可知，直接判高风险。
	if strings.ContainsAny(cmd, "$`") && (strings.Contains(cmd, "$(") || strings.Contains(cmd, "`")) {
		for _, p := range destructivePrimitives {
			if strings.Contains(cmd, p) {
				r.Level = entity.RiskHigh
				r.Reasons = append(r.Reasons, "命令替换（$() 或反引号）与破坏性原语组合，静态分析无法确定展开后的真实效果")
				break
			}
		}
	}
	for _, seg := range splitShell(cmd) {
		tokens := tokenize(seg)
		if len(tokens) == 0 {
			continue
		}
		checkTokens(tokens, r)
	}
	// 递归展开命令替换（对原始串提取，而非切词后——切词会把 $(echo rm) 拆断）
	for _, sub := range extractSubstitutions(cmd) {
		analyzeSegment(sub, r, depth+1)
	}
}

// splitShell 按 ;、&&、||、| 切分命令，尊重单双引号。
func splitShell(cmd string) []string {
	var segs []string
	var cur strings.Builder
	var quote rune
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if quote != 0 {
			cur.WriteRune(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			cur.WriteRune(c)
		case ';':
			segs = append(segs, cur.String())
			cur.Reset()
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
			}
			segs = append(segs, cur.String())
			cur.Reset()
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
			segs = append(segs, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	segs = append(segs, cur.String())
	return segs
}

// tokenize 把一段命令切成 token（尊重引号，剥离引号字符）。
func tokenize(seg string) []string {
	var tokens []string
	var cur strings.Builder
	var quote rune
	has := false
	for _, c := range seg {
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			has = true
		case ' ', '\t', '\n':
			if cur.Len() > 0 || has {
				tokens = append(tokens, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() > 0 || has {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// extractSubstitutions 提取 $(...) 与 `...` 中的子命令。
func extractSubstitutions(tok string) []string {
	var subs []string
	for i := 0; i < len(tok); i++ {
		if tok[i] == '$' && i+1 < len(tok) && tok[i+1] == '(' {
			depth := 1
			j := i + 2
			for ; j < len(tok) && depth > 0; j++ {
				switch tok[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
			}
			if depth == 0 {
				subs = append(subs, tok[i+2:j-1])
				i = j - 1
			}
		} else if tok[i] == '`' {
			j := strings.IndexByte(tok[i+1:], '`')
			if j >= 0 {
				subs = append(subs, tok[i+1:i+1+j])
				i += j + 1
			}
		}
	}
	return subs
}

// checkTokens 对一段已切词的命令做危险效果判定。
func checkTokens(tokens []string, r *Report) {
	name := filepath.Base(tokens[0])
	add := func(format string, args ...any) {
		r.Level = entity.RiskHigh
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
	}

	// mkfs.ext4 / mkfs.xfs 等文件系统族一律高危
	if name == "mkfs" || strings.HasPrefix(name, "mkfs.") {
		add("高危系统命令: %s（创建文件系统将抹除目标设备数据）", name)
		return
	}

	// sudo 一律升级
	if name == "sudo" {
		add("使用 sudo 提权执行: %s", strings.Join(tokens, " "))
		return
	}

	switch name {
	case "rm":
		recursive, force := false, false
		var targets []string
		for _, t := range tokens[1:] {
			if strings.HasPrefix(t, "-") {
				if strings.ContainsAny(t, "rR") {
					recursive = true
				}
				if strings.Contains(t, "f") {
					force = true
				}
				continue
			}
			targets = append(targets, t)
		}
		for _, tgt := range targets {
			clean := strings.TrimRight(tgt, "/")
			if clean == "" || clean == "/" {
				add("rm 以根目录 / 为目标")
			} else if recursive && (clean == "~" || clean == "$HOME" || clean == "." || clean == ".." || clean == "*" || clean == "/*") {
				add("rm -r 以高危路径为目标: %s", tgt)
			} else if recursive && force && isSystemPath(clean) {
				add("rm -rf 以系统目录为目标: %s", tgt)
			}
		}
	case "git":
		joined := strings.Join(tokens, " ")
		if len(tokens) >= 2 && tokens[1] == "push" && (strings.Contains(joined, "--force") || strings.Contains(joined, " -f")) {
			add("git push --force 会覆盖远端历史")
		}
		if strings.Contains(joined, "reset --hard") {
			add("git reset --hard 会丢弃未提交改动")
		}
		if len(tokens) >= 2 && tokens[1] == "clean" && strings.Contains(joined, "-f") {
			add("git clean -f 会删除未跟踪文件")
		}
	case "curl", "wget":
		// 远端脚本管道执行由 Analyze 在段间关系层面判定（IsPipeToShell）。
	case "dd":
		for _, t := range tokens[1:] {
			if strings.HasPrefix(t, "of=/dev/") {
				add("dd 直接写块设备: %s", t)
			}
		}
	case "shutdown", "reboot", "halt", "poweroff":
		add("高危系统命令: %s", name)
	case "chmod", "chown":
		joined := strings.Join(tokens, " ")
		if strings.Contains(joined, "-R") {
			for _, t := range tokens[1:] {
				if isSystemPath(strings.TrimRight(t, "/")) {
					add("%s -R 以系统目录为目标: %s", name, t)
				}
			}
		}
	}

	// 重定向写入系统目录 / 凭证目录
	for i, t := range tokens {
		if (t == ">" || t == ">>") && i+1 < len(tokens) {
			target := tokens[i+1]
			if isSystemPath(target) || strings.Contains(target, "/.ssh") || strings.Contains(target, "/.gnupg") || strings.Contains(target, "/.git/") {
				add("重定向写入敏感位置: %s", target)
			}
		}
		if strings.HasPrefix(t, ">") && len(t) > 1 && i == 0 {
			target := strings.TrimLeft(t, ">")
			if isSystemPath(target) {
				add("重定向写入敏感位置: %s", target)
			}
		}
	}
}

// IsPipeToShell 供调用方判定「下载 |  shell」模式：curl/wget 段紧接 sh/bash 段。
// splitShell 把管道切成独立段，故在 AnalyzeCommand 层面另行检测。
func IsPipeToShell(command string) bool {
	segs := splitShell(command)
	for i := 0; i+1 < len(segs); i++ {
		left := tokenize(segs[i])
		right := tokenize(segs[i+1])
		if len(left) == 0 || len(right) == 0 {
			continue
		}
		ln := filepath.Base(left[0])
		rn := filepath.Base(right[0])
		if (ln == "curl" || ln == "wget") && (rn == "sh" || rn == "bash" || rn == "zsh" || rn == "python3" || rn == "python") {
			return true
		}
	}
	return false
}

func isSystemPath(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	for _, d := range systemDirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// Analyze 是对外完整评估：段内检查 + 管道执行远程脚本模式。
func Analyze(command string) Report {
	r := AnalyzeCommand(command)
	if IsPipeToShell(command) {
		r.Level = entity.RiskHigh
		r.Reasons = append(r.Reasons, "下载内容直接管道给 shell 执行（curl|sh 模式）")
	}
	return r
}
