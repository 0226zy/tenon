package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/port"
)

// RunTestsTool 是自验证层（Fix Loop）的执行工具：
// 在会话工作区运行测试命令并把结果回注给模型——测试失败（非零退出码）
// 以普通工具错误结果回注（错误即输入，指引 §4.3），模型的下一轮自然进入
// 修复路径，Fix Loop 由此涌现，无需专门的循环驱动器。
//
// 实现上复用 BashTool 的持久会话执行：cwd/环境变量与 bash 工具一致，
// 重启重试、结构化超时、超长输出截断 + spill 落盘全部继承。
type RunTestsTool struct {
	bash    *BashTool
	timeout time.Duration // 缺省测试超时（测试通常比单条命令慢）
}

// NewRunTestsTool 构造工具。bash 为会话共享的持久终端工具；
// timeout 是未显式指定 timeout_seconds 时的缺省值。
func NewRunTestsTool(bash *BashTool, timeout time.Duration) *RunTestsTool {
	return &RunTestsTool{bash: bash, timeout: timeout}
}

// Definition 实现 port.Tool。
func (r *RunTestsTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{
		Type: "function",
		Function: entity.FunctionSpec{
			Name: "run_tests",
			Description: `在工作区运行测试并把结果作为反馈。是「修改代码 → 验证」闭环的验证入口：改完代码应调用本工具确认没有破坏既有行为。
何时使用：完成一轮代码修改后验证；修复失败后重新验证；接手项目时先跑一遍确认基线。
何时不要用：需要精确控制测试范围或附加参数时用 bash 自行执行；构建（非测试）用 bash。
参数：command 可选——缺省时按工作区特征自动探测（go.mod→go test ./...；Cargo.toml→cargo test；package.json 含 test 脚本→npm test；pytest 工程→python3 -m pytest -q；Makefile 含 test 目标→make test）；探测不到会返回错误，此时请显式指定 command。
失败形态：测试失败返回退出码与输出（IsError=true）——读取失败日志，修复后再次调用；超过 timeout_seconds（缺省比 bash 更长）返回结构化超时错误与已捕获输出；超长输出截断并全文落盘，可用 read_file 查看。`,
			Parameters: schema(map[string]any{
				"command":         strProp("可选，显式测试命令，例如 \"go test ./pkg/... -run TestFoo -v\"；缺省时自动探测"),
				"timeout_seconds": intProp("可选，本次测试的超时秒数，默认取服务端测试超时配置（通常 300 秒）"),
			}),
		},
	}
}

// ConcurrencySafe 实现 port.Tool：复用持久 bash 会话，不可并行。
func (r *RunTestsTool) ConcurrencySafe() bool { return false }

type runTestsArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Execute 实现 port.Tool。
func (r *RunTestsTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args runTestsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，字段均为可选）：%v", err)}
	}

	command := strings.TrimSpace(args.Command)
	if command == "" {
		detected, ok := DetectTestCommand(tctx.Cwd())
		if !ok {
			return entity.ToolResult{IsError: true, Content: "未能从工作区探测到测试命令（未识别 go.mod / Cargo.toml / package.json 的 test 脚本 / pytest 工程 / Makefile 的 test 目标）。请用 command 参数显式指定测试命令，例如 {\"command\": \"go test ./...\"}。"}
		}
		command = detected
	}

	timeoutSeconds := int(r.timeout / time.Second)
	if args.TimeoutSeconds > 0 {
		timeoutSeconds = args.TimeoutSeconds
	}
	payload, _ := json.Marshal(bashArgs{Command: command, TimeoutSeconds: timeoutSeconds})
	res := r.bash.Execute(ctx, string(payload), tctx)
	// 冠以测试语义前缀；失败内容（退出码+输出）原样保留——错误即输入。
	if res.IsError {
		res.Content = fmt.Sprintf("测试未通过（命令: %s）。请阅读失败输出、修复后重新运行本工具。\n%s", command, res.Content)
	} else {
		res.Content = fmt.Sprintf("✓ 测试通过（命令: %s，exit 0）\n%s", command, res.Content)
	}
	return res
}

// DetectTestCommand 按工作区文件特征推断测试命令。
// 探测顺序即优先级：go → cargo → npm → pytest → make。
// 只检查文件特征，不执行任何命令。
func DetectTestCommand(cwd string) (string, bool) {
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(cwd, name))
		return err == nil && !info.IsDir()
	}
	switch {
	case exists("go.mod"):
		return "go test ./...", true
	case exists("Cargo.toml"):
		return "cargo test", true
	case exists("package.json") && hasNpmTestScript(cwd):
		return "npm test", true
	case exists("pytest.ini") || exists("pyproject.toml") || exists("setup.py") || exists("tox.ini"):
		return "python3 -m pytest -q", true
	case exists("Makefile") && hasMakeTestTarget(cwd):
		return "make test", true
	}
	return "", false
}

// hasNpmTestScript 判断 package.json 是否声明了 scripts.test。
func hasNpmTestScript(cwd string) bool {
	raw, err := os.ReadFile(filepath.Join(cwd, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return false
	}
	return strings.TrimSpace(pkg.Scripts["test"]) != ""
}

// hasMakeTestTarget 判断 Makefile 是否定义了 test 目标（行首 test:）。
func hasMakeTestTarget(cwd string) bool {
	raw, err := os.ReadFile(filepath.Join(cwd, "Makefile"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "test:") {
			return true
		}
	}
	return false
}
