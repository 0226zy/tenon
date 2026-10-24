// Package statusbar 实现环境状态栏注入（指引 §2.2 状态栏条款）：
// 动态信息（时间、cwd、git 状态）以状态栏形式追加在上下文末尾，
// 绝不改写静态前缀（系统提示词 + 工具定义必须逐字节稳定）。
package statusbar

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Bar 实现 port.StatusBarPort。
type Bar struct{}

// New 构造状态栏。
func New() *Bar { return &Bar{} }

// Snapshot 采集当前环境状态。git 探测带 2 秒超时：非 git 目录或 git 卡住
// 都不能阻塞推理（后台辅助调用失败直接降级，指引 §2.4）。
func (b *Bar) Snapshot(ctx context.Context, cwd string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[环境状态栏]\n当前时间: %s\n工作目录: %s", time.Now().Format("2006-01-02 15:04:05 -0700"), cwd)

	if branch := git(ctx, cwd, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		fmt.Fprintf(&sb, "\nGit 分支: %s", branch)
		if commit := git(ctx, cwd, "log", "-1", "--oneline"); commit != "" {
			fmt.Fprintf(&sb, "\n最近提交: %s", commit)
		}
		if status := git(ctx, cwd, "status", "--porcelain"); status != "" {
			lines := strings.Split(status, "\n")
			const capLines = 10
			fmt.Fprintf(&sb, "\n未提交变更: %d 个文件", len(lines))
			for i, l := range lines {
				if i >= capLines {
					fmt.Fprintf(&sb, "\n  … 等 %d 个", len(lines)-capLines)
					break
				}
				fmt.Fprintf(&sb, "\n  %s", l)
			}
		} else {
			sb.WriteString("\n未提交变更: 无（工作区干净）")
		}
	} else {
		sb.WriteString("\nGit: 非 git 仓库（如需版本回退能力，可先 git init）")
	}
	return sb.String()
}

func git(ctx context.Context, cwd string, args ...string) string {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
