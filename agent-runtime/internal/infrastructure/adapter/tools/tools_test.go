package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/port"
)

// testCtx 构造指向临时目录的 ToolContext。
func testCtx(dir string) *port.ToolContext {
	return &port.ToolContext{
		SessionID: "t",
		Cwd:       func() string { return dir },
		Approve: func(ctx context.Context, toolName, summary, detail string) entity.ApprovalDecision {
			return entity.ApprovalDecision{Approved: true, By: "test"}
		},
		Spill: func(toolName, content string) string { return filepath.Join(dir, "spill.log") },
	}
}

// read_file：行号前缀 + 范围读取。
func TestReadFileLineNumbers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadFileTool(30000)
	res := tool.Execute(context.Background(), `{"path":"a.txt","offset":2,"limit":2}`, testCtx(dir))
	if res.IsError {
		t.Fatalf("read_file 报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "     2→line2") || !strings.Contains(res.Content, "     3→line3") {
		t.Fatalf("行号前缀缺失: %q", res.Content)
	}
	if strings.Contains(res.Content, "line1") {
		t.Fatalf("offset 未生效: %q", res.Content)
	}
	// 超出范围
	res = tool.Execute(context.Background(), `{"path":"a.txt","offset":99}`, testCtx(dir))
	if !res.IsError || !strings.Contains(res.Content, "超出文件总行数") {
		t.Fatalf("超范围应返回结构化错误: %+v", res)
	}
}

// write_file：创建 + 语法反馈；edit_file：唯一替换 / 不唯一 / 未找到 / replace_all。
func TestWriteAndEdit(t *testing.T) {
	dir := t.TempDir()
	ctx := testCtx(dir)
	w := NewWriteFileTool()

	res := w.Execute(ctx2(), `{"path":"hello.py","content":"print('hi')\n"}`, ctx)
	if res.IsError || !strings.Contains(res.Content, "已写入") {
		t.Fatalf("write_file: %+v", res)
	}
	if !strings.Contains(res.Content, "语法检查") {
		t.Fatalf("write_file 未附即时语法反馈: %q", res.Content)
	}

	// 语法错误仍写入成功，但反馈未通过
	res = w.Execute(ctx2(), `{"path":"bad.py","content":"def broken(:\n"}`, ctx)
	if res.IsError {
		t.Fatalf("语法错误不应阻止写入: %+v", res)
	}
	if !strings.Contains(res.Content, "检查未通过") && !strings.Contains(res.Content, "语法检查") {
		t.Fatalf("应包含语法检查输出: %q", res.Content)
	}

	e := NewEditFileTool()
	// 唯一替换
	res = e.Execute(ctx2(), `{"path":"hello.py","old_string":"print('hi')","new_string":"print('bye')"}`, ctx)
	if res.IsError {
		t.Fatalf("edit_file: %+v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "hello.py"))
	if !strings.Contains(string(raw), "bye") {
		t.Fatalf("替换未生效: %q", raw)
	}
	// 不唯一 → 结构化错误带候选行
	_ = os.WriteFile(filepath.Join(dir, "dup.txt"), []byte("foo\nbar\nfoo\n"), 0o644)
	res = e.Execute(ctx2(), `{"path":"dup.txt","old_string":"foo","new_string":"baz"}`, ctx)
	if !res.IsError || !strings.Contains(res.Content, "出现 2 次") || !strings.Contains(res.Content, "1") {
		t.Fatalf("不唯一时应报候选行号: %+v", res)
	}
	// replace_all
	res = e.Execute(ctx2(), `{"path":"dup.txt","old_string":"foo","new_string":"baz","replace_all":true}`, ctx)
	if res.IsError {
		t.Fatalf("replace_all: %+v", res)
	}
	// 未找到
	res = e.Execute(ctx2(), `{"path":"dup.txt","old_string":"不存在的内容","new_string":"x"}`, ctx)
	if !res.IsError || !strings.Contains(res.Content, "未在") {
		t.Fatalf("未找到应报错: %+v", res)
	}
}

func ctx2() context.Context { return context.Background() }

// glob：** 跨层匹配与截断。
func TestGlob(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "a/b/c"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "a/x.go"), []byte("package a"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a/b/y.go"), []byte("package b"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "a/b/c/z.md"), []byte("# z"), 0o644)

	g := NewGlobTool(100)
	res := g.Execute(context.Background(), `{"pattern":"**/*.go"}`, testCtx(dir))
	if res.IsError || !strings.Contains(res.Content, "x.go") || !strings.Contains(res.Content, "y.go") {
		t.Fatalf("glob **/*.go: %+v", res)
	}
	if strings.Contains(res.Content, "z.md") {
		t.Fatalf("glob 误匹配: %q", res.Content)
	}
	res = g.Execute(context.Background(), `{"pattern":"nothing/**"}`, testCtx(dir))
	if res.IsError || !strings.Contains(res.Content, "没有文件匹配") {
		t.Fatalf("空结果: %+v", res)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true},
		{"a/**", "a/b/c/d.txt", true},
		{"*.go", "a/b.go", false},
		{"internal/*/README.md", "internal/x/README.md", true},
		{"internal/*/README.md", "internal/x/y/README.md", false},
	}
	for _, c := range cases {
		got := globMatch(strings.Split(c.pat, "/"), strings.Split(c.path, "/"))
		if got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, 期望 %v", c.pat, c.path, got, c.want)
		}
	}
}

// grep：文件:行号:内容 + include 过滤。
func TestGrep(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n// TODO fix\nfunc A() {}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.md"), []byte("# doc\nTODO here\n"), 0o644)

	g := NewGrepTool(100)
	res := g.Execute(context.Background(), `{"pattern":"TODO"}`, testCtx(dir))
	if res.IsError || !strings.Contains(res.Content, "a.go:2:") || !strings.Contains(res.Content, "b.md:2:") {
		t.Fatalf("grep: %+v", res)
	}
	res = g.Execute(context.Background(), `{"pattern":"TODO","include":"*.go"}`, testCtx(dir))
	if strings.Contains(res.Content, "b.md") {
		t.Fatalf("include 过滤失效: %q", res.Content)
	}
	// 非法正则
	res = g.Execute(context.Background(), `{"pattern":"["}`, testCtx(dir))
	if !res.IsError || !strings.Contains(res.Content, "正则表达式非法") {
		t.Fatalf("非法正则应报错: %+v", res)
	}
}

// bash：持久会话——cwd 跨调用保持、退出码、超时结构化错误。
func TestBashPersistentSession(t *testing.T) {
	dir := t.TempDir()
	b := NewBashTool(10*time.Second, 30000, func(string) string { return dir }, nil)
	ctx := testCtx(dir)

	res := b.Execute(context.Background(), `{"command":"echo hello"}`, ctx)
	if res.IsError || !strings.Contains(res.Content, "hello") {
		t.Fatalf("bash echo: %+v", res)
	}
	// cwd 持久：cd 之后 pwd 应反映新目录
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	if res := b.Execute(context.Background(), `{"command":"cd sub"}`, ctx); res.IsError {
		t.Fatalf("cd: %+v", res)
	}
	// macOS 上 /var 是 /private/var 的软链，pwd 报告的是解析后的路径
	wantCwd, _ := filepath.EvalSymlinks(filepath.Join(dir, "sub"))
	if got := b.CwdFor("t"); got != wantCwd {
		t.Fatalf("cwd 未持久: got %q, want %q", got, wantCwd)
	}
	// 环境变量持久
	_ = b.Execute(context.Background(), `{"command":"export FOO=bar"}`, ctx)
	res = b.Execute(context.Background(), `{"command":"echo $FOO"}`, ctx)
	if !strings.Contains(res.Content, "bar") {
		t.Fatalf("环境变量未持久: %q", res.Content)
	}
	// exit 会杀掉 bash 进程：应立刻失败（而非挂到超时），且不得重放已执行命令。
	start := time.Now()
	res = b.Execute(context.Background(), `{"command":"echo attempted >> exits; exit 3"}`, ctx)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("exit 应立刻失败，实际耗时 %s", time.Since(start))
	}
	if !res.IsError || !strings.Contains(res.Content, "bash 会话意外退出") {
		t.Fatalf("exit 语义: %+v", res)
	}
	attempts, err := os.ReadFile(filepath.Join(dir, "sub", "exits"))
	if err != nil || string(attempts) != "attempted\n" {
		t.Fatalf("可能已执行的命令被重放: content=%q, err=%v", attempts, err)
	}
	// 会话已自动重启，后续命令正常
	res = b.Execute(context.Background(), `{"command":"echo alive"}`, ctx)
	if res.IsError || !strings.Contains(res.Content, "alive") {
		t.Fatalf("重启后 bash 不可用: %+v", res)
	}
}

func TestBashTimeout(t *testing.T) {
	dir := t.TempDir()
	b := NewBashTool(time.Second, 30000, func(string) string { return dir }, nil)
	res := b.Execute(context.Background(), `{"command":"sleep 30"}`, testCtx(dir))
	if !res.IsError || !strings.Contains(res.Content, "被终止") {
		t.Fatalf("超时应返回结构化错误: %+v", res)
	}
}

func TestTruncateOutput(t *testing.T) {
	long := strings.Repeat("a", 1000) + strings.Repeat("b", 1000) + strings.Repeat("c", 1000)
	out := TruncateOutput(long, 300, func() string { return "/tmp/spill.log" })
	if len(out) > 500 {
		t.Fatalf("截断后仍过长: %d", len(out))
	}
	if !strings.Contains(out, "完整输出已保存到 /tmp/spill.log") {
		t.Fatalf("缺截断提示: %q", out)
	}
	if !strings.HasPrefix(out, "aaa") || !strings.HasSuffix(out, "ccc") {
		t.Fatal("应保留头尾")
	}
}

func TestReadFileRejectsLongSingleLineOverReadMax(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(strings.Repeat("x", 129)), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(dir)
	spillCalls := 0
	ctx.Spill = func(string, string) string {
		spillCalls++
		return filepath.Join(dir, "spill.log")
	}
	tool := NewReadFileTool(1024, 128)
	res := tool.Execute(context.Background(), `{"path":"long.txt"}`, ctx)
	if !res.IsError || !strings.Contains(res.Content, "read_max_bytes") || !strings.Contains(res.Content, "128") {
		t.Fatalf("超过读取上限的超长单行应明确拒绝: %+v", res)
	}
	if spillCalls != 0 {
		t.Fatalf("读取上限拒绝不应生成 spill，调用 %d 次", spillCalls)
	}
}

func TestReadFileTruncatesLongSingleLineOutput(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 2048)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(dir)
	spillBytes := 0
	ctx.Spill = func(_ string, content string) string {
		spillBytes = len(content)
		return "/tmp/read-file-spill.log"
	}
	tool := NewReadFileTool(256, 4096)
	res := tool.Execute(context.Background(), `{"path":"long.txt"}`, ctx)
	if res.IsError || !strings.Contains(res.Content, "中间省略") {
		t.Fatalf("读取上限内的超长单行应截断输出: %+v", res)
	}
	if len(res.Content) > 512 {
		t.Fatalf("256 字节输出预算后结果仍过长: %d", len(res.Content))
	}
	if spillBytes <= len(line) {
		t.Fatalf("spill 应保留完整带行号内容，got %d", spillBytes)
	}
}

func TestGrepSearchMaxBytesIsCumulative(t *testing.T) {
	dir := t.TempDir()
	contentA := "needle\n" + strings.Repeat("a", 57)
	contentB := "needle\n" + strings.Repeat("b", 57)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(contentA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(contentB), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool(10, 80)
	res := tool.Execute(context.Background(), `{"pattern":"needle"}`, testCtx(dir))
	if res.IsError || !strings.Contains(res.Content, "a.txt:1:needle") {
		t.Fatalf("预算内首个文件应完成搜索: %+v", res)
	}
	if strings.Contains(res.Content, "b.txt") || !strings.Contains(res.Content, "search_max_bytes") {
		t.Fatalf("累计预算应阻止第二个 64 字节文件并明确提示: %q", res.Content)
	}
}

func TestGlobSearchMaxBytesIsCumulative(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(first, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool(10, len(first)+1)
	res := tool.Execute(context.Background(), `{"pattern":"*.txt"}`, testCtx(dir))
	if res.IsError || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("首个路径应在累计字节预算内: %+v", res)
	}
	if strings.Contains(res.Content, "b.txt") || !strings.Contains(res.Content, "search_max_bytes") {
		t.Fatalf("第二个同长度路径应超过累计预算并明确提示: %q", res.Content)
	}
}

func TestFilterEnvKeepsGoWorkIsolationAndDropsSecrets(t *testing.T) {
	filtered := FilterEnv([]string{"GOWORK=off", "PATH=/usr/bin", "WSD_TOKEN=secret"}, nil)
	joined := strings.Join(filtered, "\n")
	if !strings.Contains(joined, "GOWORK=off") || !strings.Contains(joined, "PATH=/usr/bin") {
		t.Fatalf("default environment lost required build values: %v", filtered)
	}
	if strings.Contains(joined, "WSD_TOKEN") {
		t.Fatalf("default environment leaked a Runtime token: %v", filtered)
	}
}
