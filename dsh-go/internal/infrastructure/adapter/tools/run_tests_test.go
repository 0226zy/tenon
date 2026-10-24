package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 探测逻辑：按工作区文件特征推断测试命令（只查文件，不执行）。
func TestDetectTestCommand(t *testing.T) {
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("go.mod", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "go.mod", "module x\n")
		cmd, ok := DetectTestCommand(dir)
		if !ok || cmd != "go test ./..." {
			t.Fatalf("got %q, %v", cmd, ok)
		}
	})
	t.Run("Cargo.toml", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "Cargo.toml", "[package]\n")
		cmd, ok := DetectTestCommand(dir)
		if !ok || cmd != "cargo test" {
			t.Fatalf("got %q, %v", cmd, ok)
		}
	})
	t.Run("package.json 含 test 脚本", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
		cmd, ok := DetectTestCommand(dir)
		if !ok || cmd != "npm test" {
			t.Fatalf("got %q, %v", cmd, ok)
		}
	})
	t.Run("package.json 无 test 脚本则继续下探", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "package.json", `{"scripts":{"build":"tsc"}}`)
		write(dir, "pyproject.toml", "[tool.pytest]\n")
		cmd, ok := DetectTestCommand(dir)
		if !ok || cmd != "python3 -m pytest -q" {
			t.Fatalf("got %q, %v", cmd, ok)
		}
	})
	t.Run("Makefile 含 test 目标", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "Makefile", ".PHONY: test\ntest:\n\t@echo ok\n")
		cmd, ok := DetectTestCommand(dir)
		if !ok || cmd != "make test" {
			t.Fatalf("got %q, %v", cmd, ok)
		}
	})
	t.Run("空目录探测不到", func(t *testing.T) {
		if cmd, ok := DetectTestCommand(t.TempDir()); ok {
			t.Fatalf("空目录不应探测到命令，got %q", cmd)
		}
	})
	t.Run("go 优先于 make", func(t *testing.T) {
		dir := t.TempDir()
		write(dir, "go.mod", "module x\n")
		write(dir, "Makefile", "test:\n\t@echo ok\n")
		cmd, ok := DetectTestCommand(dir)
		if !ok || cmd != "go test ./..." {
			t.Fatalf("got %q, %v", cmd, ok)
		}
	})
}

// 执行路径：显式命令成功 / 失败回注（错误即输入）。
func TestRunTestsExplicitCommand(t *testing.T) {
	dir := t.TempDir()
	bash := NewBashTool(30*time.Second, 30000, func(string) string { return dir }, nil)
	t.Cleanup(func() { bash.CloseSession("t") })
	tool := NewRunTestsTool(bash, 300*time.Second)

	res := tool.Execute(context.Background(), `{"command":"echo all green"}`, testCtx(dir))
	if res.IsError {
		t.Fatalf("成功用例报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "✓ 测试通过") || !strings.Contains(res.Content, "all green") {
		t.Fatalf("成功结果缺少语义前缀或输出: %q", res.Content)
	}

	// 子 shell 中 exit：拿到非零退出码而不终止持久会话本身
	res = tool.Execute(context.Background(), `{"command":"(echo FAIL: TestX; exit 3)"}`, testCtx(dir))
	if !res.IsError {
		t.Fatal("非零退出码必须回注为错误结果")
	}
	if !strings.Contains(res.Content, "测试未通过") || !strings.Contains(res.Content, "退出码 3") || !strings.Contains(res.Content, "FAIL: TestX") {
		t.Fatalf("失败结果缺少前缀/退出码/输出: %q", res.Content)
	}
}

// 探测 + 执行集成：Makefile test 目标（无 make 环境时跳过）。
func TestRunTestsDetectAndRun(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("环境无 make，跳过集成用例")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test:\n\t@echo suite ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bash := NewBashTool(30*time.Second, 30000, func(string) string { return dir }, nil)
	t.Cleanup(func() { bash.CloseSession("t") })
	tool := NewRunTestsTool(bash, 300*time.Second)

	res := tool.Execute(context.Background(), `{}`, testCtx(dir))
	if res.IsError {
		t.Fatalf("探测执行报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "make test") || !strings.Contains(res.Content, "suite ok") {
		t.Fatalf("结果缺少探测命令或输出: %q", res.Content)
	}
}

// 未探测到命令：结构化错误并提示显式指定。
func TestRunTestsNotDetected(t *testing.T) {
	dir := t.TempDir()
	bash := NewBashTool(30*time.Second, 30000, func(string) string { return dir }, nil)
	t.Cleanup(func() { bash.CloseSession("t") })
	tool := NewRunTestsTool(bash, 300*time.Second)

	res := tool.Execute(context.Background(), `{}`, testCtx(dir))
	if !res.IsError || !strings.Contains(res.Content, "未能从工作区探测到测试命令") {
		t.Fatalf("应返回探测失败错误: %+v", res)
	}
}
