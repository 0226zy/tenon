package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

// DeployTarget 是一个部署目标：命令模板固化在服务端配置中，
// 模型只能选用目标并填参数，不能拼任意命令——风险面远小于 bash 直跑部署命令。
type DeployTarget struct {
	Name            string // 目标名（模型用 target 参数选择）
	Description     string // 给模型看的说明
	CommandTemplate string // 命令模板，参数占位符形如 {{image}}
}

// DeployTool 是部署与运维层的执行工具。
// 安全设计（对齐分析文档 §4.4）：
//   - 命令模板由配置作者固化；参数值一律经 shellQuote 转义后才替换进模板，
//     模型无法借参数注入 shell 片段；
//   - 本工具在 policy 钩子中注册为「强制审批」——任何审批模式（含 full-auto）
//     都必须经人工裁决，审批事件落盘可审计；
//   - 执行复用 bash 持久会话（超时/截断/spill 继承），失败输出错误回注。
type DeployTool struct {
	bash    *BashTool
	targets map[string]DeployTarget
	order   []string
	timeout time.Duration
}

// NewDeployTool 构造工具。targets 为空时不应注册本工具（配置决定启用）。
func NewDeployTool(bash *BashTool, targets []DeployTarget, timeout time.Duration) *DeployTool {
	d := &DeployTool{bash: bash, targets: map[string]DeployTarget{}, timeout: timeout}
	for _, t := range targets {
		d.targets[t.Name] = t
		d.order = append(d.order, t.Name)
	}
	return d
}

// Definition 实现 port.Tool。描述中列出可用目标（模型选择 target 的依据）。
func (d *DeployTool) Definition() entity.ToolDefinition {
	var sb strings.Builder
	sb.WriteString(`把构建产物部署到服务端预配置的目标环境。命令模板固化在配置中，你只能选择 target 并填写参数，不能执行任意命令。
何时使用：代码已构建且测试通过、用户要求上线/发布时。
何时不要用：本地构建与测试用 bash / run_tests；未在列表中的部署方式不存在，不要尝试用 bash 绕过。
失败形态：未知 target 或缺少参数时返回错误并列出可用目标；部署命令非零退出时回注输出（读取日志，修复后重试）。
注意：本工具任何审批模式下都需人工批准后才执行。
可用目标：`)
	for _, name := range d.order {
		t := d.targets[name]
		fmt.Fprintf(&sb, "\n- %s：%s", t.Name, t.Description)
	}
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name:        "deploy",
			Description: sb.String(),
			Parameters: schema(map[string]any{
				"target": strProp("部署目标名（见上方可用目标列表）"),
				"params": map[string]any{
					"type":                 "object",
					"description":          "模板参数键值对，如 {\"image\": \"myapp\", \"tag\": \"v1.2.0\"}；值会被安全转义后替换进命令模板的 {{key}} 占位符",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"timeout_seconds": intProp("可选，本次部署的超时秒数，默认取服务端 bash 超时"),
			}, "target"),
		},
	}
}

// ConcurrencySafe 实现 port.Tool：复用持久 bash 会话，不可并行。
func (d *DeployTool) ConcurrencySafe() bool { return false }

type deployArgs struct {
	Target         string            `json:"target"`
	Params         map[string]string `json:"params"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

// Execute 实现 port.Tool。
func (d *DeployTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args deployArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 target: string）：%v", err)}
	}
	target, ok := d.targets[args.Target]
	if !ok {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("未知部署目标 %q。可用目标：%s", args.Target, strings.Join(d.order, ", "))}
	}
	command, err := renderDeployCommand(target, args.Params)
	if err != nil {
		return entity.ToolResult{IsError: true, Content: err.Error()}
	}

	payload, _ := json.Marshal(bashArgs{Command: command, TimeoutSeconds: args.TimeoutSeconds})
	res := d.bash.Execute(ctx, string(payload), tctx)
	if res.IsError {
		res.Content = fmt.Sprintf("部署失败（目标: %s）。请阅读输出、修复后重试。\n%s", args.Target, res.Content)
	} else {
		res.Content = fmt.Sprintf("✓ 部署完成（目标: %s）\n%s", args.Target, res.Content)
	}
	return res
}

// renderDeployCommand 用转义后的参数值替换模板占位符。
// 渲染后仍残留 {{ 说明模板缺参数——拒绝执行（不拼半截命令）。
func renderDeployCommand(target DeployTarget, params map[string]string) (string, error) {
	cmd := target.CommandTemplate
	// 按 key 排序渲染，保证同一输入得到同一命令（审批详情可复现）。
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd = strings.ReplaceAll(cmd, "{{"+k+"}}", shellQuote(params[k]))
	}
	if strings.Contains(cmd, "{{") {
		return "", fmt.Errorf("目标 %q 的命令模板参数不完整（仍有未替换的 {{...}} 占位符）；模板：%s", target.Name, target.CommandTemplate)
	}
	return cmd, nil
}

// shellQuote 把任意参数值安全地包进 shell 单引号（' → '\”）。
// 模型提供的值经过此函数后不可能注入额外的 shell 片段。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
