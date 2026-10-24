package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

var errShellDied = errors.New("bash 会话已终止")
var errShellExited = errors.New("bash 会话意外退出")

var defaultEnvAllowlist = []string{"PATH", "HOME", "LANG", "TERM", "GOPROXY", "GOFLAGS", "GOMODCACHE", "GOWORK", "npm_config_registry", "npm_config_cache"}
var deniedEnv = map[string]bool{"LLM_PROXY_TOKEN": true, "MAGENTS_AUTH_TOKEN": true, "WSD_TOKEN": true}

// FilterEnv 按白名单过滤环境；三个 Runtime 敏感令牌始终拒绝，即使被误加入白名单。
func FilterEnv(environ []string, allow []string) []string {
	if len(allow) == 0 {
		allow = defaultEnvAllowlist
	}
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[name] = true
	}
	out := make([]string, 0, len(allow))
	seen := map[string]bool{}
	for _, item := range environ {
		name := item
		if i := strings.IndexByte(item, '='); i >= 0 {
			name = item[:i]
		}
		if deniedEnv[name] || !allowed[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, item)
	}
	return out
}

func setEnv(environ []string, name, value string) []string {
	out := make([]string, 0, len(environ)+1)
	for _, item := range environ {
		if !strings.HasPrefix(item, name+"=") {
			out = append(out, item)
		}
	}
	return append(out, name+"="+value)
}

type shellKey struct{ sessionID, runID string }

// BashTool 持有按 Run 隔离的持久 shell，并实现 port.ExecTracker。
type BashTool struct {
	timeout    time.Duration
	maxOutput  int
	headBytes  int
	tailBytes  int
	env        []string
	defaultCwd func(sessionID string) string

	mu       sync.Mutex
	sessions map[shellKey]*shell
	latest   map[string]string
	runs     map[string]map[int]*shell
	execMu   sync.Mutex
	listener func(sessionID, runID string, pgid int) error
}

// NewBashTool 构造工具。env 为空时使用安全默认白名单。
func NewBashTool(timeout time.Duration, maxOutput int, defaultCwd func(sessionID string) string, env []string) *BashTool {
	if maxOutput <= 0 {
		maxOutput = 65_536
	}
	head := maxOutput / 2
	return &BashTool{timeout: timeout, maxOutput: maxOutput, headBytes: head, tailBytes: maxOutput - head,
		env: append([]string(nil), env...), defaultCwd: defaultCwd,
		sessions: map[shellKey]*shell{}, latest: map[string]string{}, runs: map[string]map[int]*shell{}}
}

// SetOutputBounds 设置 Bash 有界头尾缓冲，不改变其它工具的 max_output_bytes。
func (b *BashTool) SetOutputBounds(head, tail int) {
	if head > 0 {
		b.headBytes = head
	}
	if tail > 0 {
		b.tailBytes = tail
	}
}

// SetProcessListener 在 shell 创建后持久登记 pgid；登记失败会终止新 shell并返回错误。
func (b *BashTool) SetProcessListener(listener func(sessionID, runID string, pgid int) error) {
	b.listener = listener
}

func (b *BashTool) CwdFor(sessionID string) string {
	b.mu.Lock()
	runID := b.latest[sessionID]
	sh := b.sessions[shellKey{sessionID: sessionID, runID: runID}]
	b.mu.Unlock()
	if sh != nil && !sh.isDead() {
		return sh.cwd()
	}
	if b.defaultCwd != nil {
		return b.defaultCwd(sessionID)
	}
	return "."
}

func (b *BashTool) CloseSession(sessionID string) {
	b.mu.Lock()
	var shells []*shell
	for key, sh := range b.sessions {
		if key.sessionID == sessionID {
			shells = append(shells, sh)
			delete(b.sessions, key)
		}
	}
	delete(b.latest, sessionID)
	b.mu.Unlock()
	for _, sh := range shells {
		sh.kill()
	}
}

func (b *BashTool) Definition() entity.ToolDefinition {
	return entity.ToolDefinition{Type: "function", Function: entity.FunctionSpec{
		Name: "bash",
		Description: `在按 Run 隔离的持久 bash 会话中执行 shell 命令。同一 Run 内工作目录、环境变量、后台作业跨调用保持。
何时使用：构建、测试、git 操作、包管理、运行脚本、查看进程等需要 shell 的场景。
何时不要用：读文件用 read_file、按内容搜索用 grep、按文件名定位用 glob。
参数：command 为完整 shell 命令，可含管道与重定向；timeout_seconds 可选。
失败形态：非零退出码、超时、取消均以结构化错误和已捕获输出返回。交互式命令应改用非交互形式。`,
		Parameters: schema(map[string]any{
			"command":         strProp("要执行的 shell 命令，例如 \"git status --short\" 或 \"npm test\""),
			"timeout_seconds": intProp("可选，本条命令的超时秒数，默认取服务端配置"),
		}, "command"),
	}}
}

func (b *BashTool) ConcurrencySafe() bool { return false }

type bashArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (b *BashTool) Execute(ctx context.Context, argsJSON string, tctx *port.ToolContext) entity.ToolResult {
	var args bashArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("参数解析失败（应为 JSON 对象，含必填字段 command: string）：%v", err)}
	}
	if strings.TrimSpace(args.Command) == "" {
		return entity.ToolResult{IsError: true, Content: "参数 command 不能为空"}
	}
	timeout := b.timeout
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	sh, err := b.shellFor(tctx.SessionID, tctx.RunID, tctx.Cwd())
	if err != nil {
		return entity.ToolResult{IsError: true, Content: err.Error()}
	}
	output, exitCode, timedOut, err := sh.run(ctx, args.Command, timeout, b.headBytes, b.tailBytes)
	restarted := false
	// 仅重试明确尚未写入命令的已终止 shell；命令可能已经执行时绝不重放。
	if errors.Is(err, errShellDied) {
		if ctx.Err() == nil {
			restarted = true
			sh, err = b.shellFor(tctx.SessionID, tctx.RunID, tctx.Cwd())
			if err != nil {
				return entity.ToolResult{IsError: true, Content: err.Error()}
			}
			output, exitCode, timedOut, err = sh.run(ctx, args.Command, timeout, b.headBytes, b.tailBytes)
		}
	}
	if restarted {
		output = "[提示] 上一个 bash 会话已终止，本命令已在新会话中执行，之前的 shell 状态已丢失。\n" + output
	}
	if timedOut {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf(
			"执行超过 %s 被终止（进程组已清理）。输出如下：\n%s\n请改用更小的任务分片、增加 timeout_seconds，或改为后台运行后再轮询。", timeout, output)}
	}
	if err != nil {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("bash 执行失败: %v\n%s", err, output)}
	}
	if exitCode != 0 {
		return entity.ToolResult{IsError: true, Content: fmt.Sprintf("退出码 %d\n%s", exitCode, output)}
	}
	if strings.TrimSpace(output) == "" {
		output = "(命令成功，无输出)"
	}
	return entity.ToolResult{Content: output}
}

func (b *BashTool) shellFor(sessionID, runID, cwd string) (*shell, error) {
	key := shellKey{sessionID: sessionID, runID: runID}
	b.mu.Lock()
	if sh := b.sessions[key]; sh != nil && !sh.isDead() {
		b.mu.Unlock()
		return sh, nil
	}
	if (cwd == "" || cwd == ".") && b.defaultCwd != nil {
		cwd = b.defaultCwd(sessionID)
	}
	sh, err := newShell(cwd, FilterEnv(os.Environ(), b.env))
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	b.sessions[key] = sh
	b.latest[sessionID] = runID
	b.mu.Unlock()

	if runID != "" {
		pgid := sh.cmd.Process.Pid
		b.Register(runID, pgid)
		b.mu.Lock()
		b.runs[runID][pgid] = sh
		b.mu.Unlock()
		if b.listener != nil {
			if err := b.listener(sessionID, runID, pgid); err != nil {
				sh.kill()
				return nil, fmt.Errorf("登记 Run 进程组失败: %w", err)
			}
		}
	}
	return sh, nil
}

// Register 登记一个 Run 的进程组；nil shell 表示重启后仅知 pgid 的遗留进程。
func (b *BashTool) Register(runID string, pgid int) {
	if runID == "" || pgid <= 0 {
		return
	}
	b.mu.Lock()
	groups := b.runs[runID]
	if groups == nil {
		groups = map[int]*shell{}
		b.runs[runID] = groups
	}
	if _, ok := groups[pgid]; !ok {
		groups[pgid] = nil
	}
	b.mu.Unlock()
}

func processGroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (b *BashTool) Alive(runID string) int {
	b.mu.Lock()
	groups := make([]int, 0, len(b.runs[runID]))
	for pgid := range b.runs[runID] {
		groups = append(groups, pgid)
	}
	b.mu.Unlock()
	alive := 0
	for _, pgid := range groups {
		if processGroupAlive(pgid) {
			alive++
		}
	}
	return alive
}

// Kill 只清理登记到 runID 的进程组：先 TERM，grace 后 KILL，并轮询到 ESRCH。
func (b *BashTool) Kill(ctx context.Context, runID string, grace time.Duration) (int, error) {
	b.execMu.Lock()
	defer b.execMu.Unlock()
	b.mu.Lock()
	groups := make([]int, 0, len(b.runs[runID]))
	for pgid := range b.runs[runID] {
		groups = append(groups, pgid)
	}
	b.mu.Unlock()
	if len(groups) == 0 {
		return 0, nil
	}
	remaining := func() int {
		n := 0
		for _, pgid := range groups {
			if processGroupAlive(pgid) {
				n++
			}
		}
		return n
	}
	if grace > 0 {
		for _, pgid := range groups {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		}
		timer := time.NewTimer(grace)
		ticker := time.NewTicker(50 * time.Millisecond)
		for remaining() > 0 {
			select {
			case <-timer.C:
				ticker.Stop()
				goto kill
			case <-ticker.C:
			case <-ctx.Done():
				timer.Stop()
				ticker.Stop()
				return remaining(), ctx.Err()
			}
		}
		timer.Stop()
		ticker.Stop()
	}

kill:
	if remaining() > 0 {
		for _, pgid := range groups {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		n := remaining()
		if n == 0 {
			b.mu.Lock()
			delete(b.runs, runID)
			for key, sh := range b.sessions {
				if key.runID == runID {
					delete(b.sessions, key)
					sh.markDead()
				}
			}
			b.mu.Unlock()
			return 0, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return n, ctx.Err()
		}
	}
}

// ── 持久 bash 进程 ────────────────────────────────────────────────

type shellEvent struct {
	data    []byte
	control bool
	rc      int
	cwd     string
}

type shell struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan shellEvent
	done   chan struct{}
	token  string
	curCwd string
	once   sync.Once
}

func newShell(cwd string, env []string) (*shell, error) {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	sh := &shell{events: make(chan shellEvent, 256), done: make(chan struct{}),
		token: "__AGENT_RUNTIME_DONE_" + hex.EncodeToString(buf) + "__", curCwd: cwd}
	cmd := exec.Command("bash")
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = setEnv(env, "TERM", "dumb")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sh.cmd = cmd
	sh.stdin = stdin
	go sh.readLoop(pr)
	go func() { _ = cmd.Wait(); _ = pr.Close(); sh.markDead() }()
	return sh, nil
}

func (sh *shell) emit(ev shellEvent) bool {
	select {
	case sh.events <- ev:
		return true
	case <-sh.done:
		return false
	}
}

// readLoop 用固定大小 ReadSlice 分片；单行再长也不会构造完整 line。
func (sh *shell) readLoop(pr *io.PipeReader) {
	reader := bufio.NewReaderSize(pr, 4096)
	var line []byte
	overflow := false
	awaitPwd := false
	pendingRC := 0
	handleLine := func(complete []byte) {
		if awaitPwd {
			sh.emit(shellEvent{control: true, rc: pendingRC, cwd: strings.TrimSpace(string(complete))})
			awaitPwd = false
			return
		}
		if bytes.HasPrefix(complete, []byte(sh.token)) {
			_, _ = fmt.Sscanf(strings.TrimSpace(string(complete[len(sh.token):])), "%d", &pendingRC)
			awaitPwd = true
			return
		}
		sh.emit(shellEvent{data: append([]byte(nil), complete...)})
	}
	for {
		fragment, err := reader.ReadSlice('\n')
		complete := len(fragment) > 0 && fragment[len(fragment)-1] == '\n'
		if len(fragment) > 0 {
			if overflow {
				sh.emit(shellEvent{data: append([]byte(nil), fragment...)})
			} else {
				line = append(line, fragment...)
				if len(line) > 4096 && !complete {
					sh.emit(shellEvent{data: append([]byte(nil), line...)})
					line = nil
					overflow = true
				} else if complete {
					handleLine(line)
					line = nil
				}
			}
			if complete {
				overflow = false
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if len(line) > 0 {
				sh.emit(shellEvent{data: append([]byte(nil), line...)})
			}
			close(sh.events)
			sh.markDead()
			return
		}
	}
}

func (sh *shell) isDead() bool {
	select {
	case <-sh.done:
		return true
	default:
		return false
	}
}
func (sh *shell) markDead() { sh.once.Do(func() { close(sh.done) }) }
func (sh *shell) kill() {
	if sh.cmd != nil && sh.cmd.Process != nil {
		_ = syscall.Kill(-sh.cmd.Process.Pid, syscall.SIGKILL)
		_ = sh.cmd.Process.Kill()
	}
	sh.markDead()
}
func (sh *shell) cwd() string {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.curCwd
}

func (sh *shell) run(ctx context.Context, command string, timeout time.Duration, head, tail int) (output string, exitCode int, timedOut bool, retErr error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	buffer := newBoundedBuffer(head, tail)
	defer func() { output = buffer.finish() }()
	if sh.isDead() || sh.stdin == nil {
		return "", -1, false, errShellDied
	}
	wrapped := fmt.Sprintf("%s\n__agent_runtime_rc=$?; echo %q$__agent_runtime_rc; pwd\n", command, sh.token)
	if _, err := io.WriteString(sh.stdin, wrapped); err != nil {
		sh.markDead()
		// 写入可能已部分完成，不能把这次调用当作“未执行”自动重试。
		return "", -1, false, fmt.Errorf("%w: %v", errShellExited, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	handle := func(ev shellEvent) (bool, int) {
		if ev.control {
			sh.curCwd = ev.cwd
			return true, ev.rc
		}
		_, _ = buffer.Write(ev.data)
		return false, 0
	}
	for {
		// 优先消费已排队事件，避免进程退出通知掩盖末尾 sentinel。
		select {
		case ev, ok := <-sh.events:
			if !ok {
				return "", -1, false, errShellExited
			}
			if done, rc := handle(ev); done {
				return "", rc, false, nil
			}
			continue
		default:
		}
		select {
		case ev, ok := <-sh.events:
			if !ok {
				return "", -1, false, errShellExited
			}
			if done, rc := handle(ev); done {
				return "", rc, false, nil
			}
		case <-timer.C:
			sh.kill()
			return "", -1, true, nil
		case <-ctx.Done():
			sh.kill()
			return "", -1, false, ctx.Err()
		case <-sh.done:
			for {
				select {
				case ev, ok := <-sh.events:
					if !ok {
						return "", -1, false, errShellExited
					}
					if done, rc := handle(ev); done {
						return "", rc, false, nil
					}
				default:
					return "", -1, false, errShellExited
				}
			}
		}
	}
}

// boundedBuffer 从头至尾流式写 spill；内存仅保留固定 head/tail。
type boundedBuffer struct {
	head, tail []byte
	headLimit  int
	tailLimit  int
	total      int64
	spill      *os.File
	spillErr   error
}

func newBoundedBuffer(head, tail int) *boundedBuffer {
	if head <= 0 && tail <= 0 {
		head, tail = 32_768, 32_768
	}
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	file, err := os.CreateTemp("", "agent-runtime-bash-*.log")
	b := &boundedBuffer{headLimit: head, tailLimit: tail, spill: file}
	if err != nil {
		b.spillErr = err
	}
	return b
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	b.total += int64(original)
	if b.spill != nil {
		if _, err := b.spill.Write(p); err != nil && b.spillErr == nil {
			b.spillErr = err
		}
	}
	if remain := b.headLimit - len(b.head); remain > 0 {
		n := len(p)
		if n > remain {
			n = remain
		}
		b.head = append(b.head, p[:n]...)
	}
	if b.tailLimit > 0 {
		if len(p) >= b.tailLimit {
			b.tail = append(b.tail[:0], p[len(p)-b.tailLimit:]...)
		} else {
			overflow := len(b.tail) + len(p) - b.tailLimit
			if overflow > 0 {
				copy(b.tail, b.tail[overflow:])
				b.tail = b.tail[:len(b.tail)-overflow]
			}
			b.tail = append(b.tail, p...)
		}
	}
	return original, nil
}

func (b *boundedBuffer) finish() string {
	if b.spill != nil {
		_ = b.spill.Close()
	}
	limit := int64(b.headLimit + b.tailLimit)
	truncated := b.total > limit
	spillPath := ""
	if truncated && b.spill != nil && b.spillErr == nil {
		spillPath = b.spill.Name()
	} else if b.spill != nil {
		_ = os.Remove(b.spill.Name())
	}
	tail := b.tail
	if !truncated {
		overlap := len(b.head) + len(tail) - int(b.total)
		if overlap > 0 {
			tail = tail[overlap:]
		}
	}
	if !truncated {
		return string(append(append([]byte(nil), b.head...), tail...))
	}
	omitted := b.total - int64(len(b.head)) - int64(len(tail))
	marker := fmt.Sprintf("\n…… [中间省略 %d 字节；完整输出已保存到 %s，可用 read_file 分段查看] ……\n", omitted, spillPath)
	if b.spillErr != nil {
		marker = fmt.Sprintf("\n…… [中间省略 %d 字节；spill 落盘失败: %v] ……\n", omitted, b.spillErr)
	}
	out := make([]byte, 0, len(b.head)+len(marker)+len(tail))
	out = append(out, b.head...)
	out = append(out, marker...)
	out = append(out, tail...)
	return string(out)
}
