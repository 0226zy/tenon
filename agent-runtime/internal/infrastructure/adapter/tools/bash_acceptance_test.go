package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/0226zy/tenon/agent-runtime/internal/domain/port"
)

func acceptanceBashArgs(t *testing.T, command string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func newAcceptanceBash(t *testing.T, dir, runID string, maxOutput int) (*BashTool, *port.ToolContext) {
	t.Helper()
	const sessionID = "bash-acceptance-session"
	b := NewBashTool(30*time.Second, maxOutput, func(string) string { return dir }, nil)
	tctx := testCtx(dir)
	tctx.SessionID = sessionID
	tctx.RunID = runID
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = b.Kill(ctx, runID, 0)
		b.CloseSession(sessionID)
	})
	return b, tctx
}

func TestBashEnvironmentFiltersAllRuntimeTokens(t *testing.T) {
	secrets := map[string]string{
		"LLM_PROXY_TOKEN":    "llm-secret-7f38d5",
		"MAGENTS_AUTH_TOKEN": "magents-secret-19c62a",
		"WSD_TOKEN":          "wsd-secret-51ad07",
	}
	allow := []string{"PATH", "HOME"}
	for name, value := range secrets {
		t.Setenv(name, value)
		allow = append(allow, name) // Deny rules must win even over an explicitly bad allowlist.
	}
	dir := t.TempDir()
	b := NewBashTool(5*time.Second, 4096, func(string) string { return dir }, allow)
	t.Cleanup(func() { b.CloseSession("bash-env-session") })
	tctx := testCtx(dir)
	tctx.SessionID = "bash-env-session"
	res := b.Execute(context.Background(), acceptanceBashArgs(t, "env"), tctx)
	if res.IsError {
		t.Fatalf("env command failed: %+v", res)
	}
	for name, value := range secrets {
		if strings.Contains(res.Content, name) || strings.Contains(res.Content, value) {
			t.Errorf("bash environment leaked %s: %q", name, res.Content)
		}
	}
	if !strings.Contains(res.Content, "PATH=") {
		t.Fatalf("allowlisted PATH was not preserved: %q", res.Content)
	}
}

func acceptanceSpillPath(t *testing.T, output string) string {
	t.Helper()
	const prefix = "完整输出已保存到 "
	start := strings.Index(output, prefix)
	if start < 0 {
		t.Fatalf("truncated output has no spill path: %q", output)
	}
	rest := output[start+len(prefix):]
	end := strings.Index(rest, "，可用 read_file")
	if end <= 0 {
		t.Fatalf("malformed spill marker: %q", output)
	}
	path := rest[:end]
	if path == "" || !filepath.IsAbs(path) {
		t.Fatalf("invalid spill path %q", path)
	}
	return path
}

func assertFileBytesAt(t *testing.T, path string, offset int64, want string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, int64(len(got))), got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("spill bytes at %d = %q, want %q", offset, got, want)
	}
}

func TestBash120MiBOutputCompletesWithBoundedResultAndCompleteSpill(t *testing.T) {
	dir := t.TempDir()
	b, tctx := newAcceptanceBash(t, dir, "run-bash-120mib", 4096)
	const bulkBytes = int64(120 * 1024 * 1024)
	const prefix = "AGENT_RUNTIME_120MIB_HEAD\n"
	const suffix = "\nAGENT_RUNTIME_120MIB_TAIL\n"
	command := `printf 'AGENT_RUNTIME_120MIB_HEAD\n'
dd if=/dev/zero bs=1048576 count=120 2>/dev/null | tr '\000' 'x'
printf '\nAGENT_RUNTIME_120MIB_TAIL\n'`
	res := b.Execute(context.Background(), acceptanceBashArgs(t, command), tctx)
	if res.IsError {
		t.Fatalf("120 MiB command did not complete: %s", res.Content)
	}
	if len(res.Content) > 8192 {
		t.Fatalf("bounded result is too large: %d bytes", len(res.Content))
	}
	if !strings.HasPrefix(res.Content, prefix) || !strings.HasSuffix(res.Content, "AGENT_RUNTIME_120MIB_TAIL\n") {
		t.Fatalf("head/tail were not preserved: prefix=%q suffix=%q", res.Content[:min(len(res.Content), 80)], res.Content[max(0, len(res.Content)-80):])
	}
	wantTotal := bulkBytes + int64(len(prefix)+len(suffix))
	wantOmitted := wantTotal - 4096
	if !strings.Contains(res.Content, fmt.Sprintf("中间省略 %d 字节", wantOmitted)) {
		t.Fatalf("wrong truncation accounting, want omitted=%d: %q", wantOmitted, res.Content)
	}
	spill := acceptanceSpillPath(t, res.Content)
	defer func() { _ = os.Remove(spill) }()
	info, err := os.Stat(spill)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != wantTotal {
		t.Fatalf("spill size = %d, want %d", info.Size(), wantTotal)
	}
	assertFileBytesAt(t, spill, 0, prefix)
	assertFileBytesAt(t, spill, int64(len(prefix)), strings.Repeat("x", 64))
	assertFileBytesAt(t, spill, info.Size()-int64(len(suffix)), suffix)
}

func TestBashFiveMiBSingleLineAndProcessGroupCleanup(t *testing.T) {
	dir := t.TempDir()
	const runID = "run-bash-long-line"
	b, tctx := newAcceptanceBash(t, dir, runID, 1024)
	command := `dd if=/dev/zero bs=1048576 count=5 2>/dev/null | tr '\000' 'x'; printf '\n'`
	res := b.Execute(context.Background(), acceptanceBashArgs(t, command), tctx)
	if res.IsError {
		t.Fatalf("5 MiB single-line command failed: %s", res.Content)
	}
	if len(res.Content) > 2048 || !strings.Contains(res.Content, "中间省略") {
		t.Fatalf("5 MiB output was not bounded and truncated: len=%d", len(res.Content))
	}
	spill := acceptanceSpillPath(t, res.Content)
	info, err := os.Stat(spill)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(5*1024*1024 + 1); info.Size() != want {
		t.Fatalf("5 MiB spill size = %d, want %d", info.Size(), want)
	}
	if err := os.Remove(spill); err != nil {
		t.Fatal(err)
	}

	res = b.Execute(context.Background(), acceptanceBashArgs(t, `printf 'AFTER_LONG_LINE\n'`), tctx)
	if res.IsError || !strings.Contains(res.Content, "AFTER_LONG_LINE") {
		t.Fatalf("shell did not recover after a long line: %+v", res)
	}
	pidPath := filepath.Join(dir, "child.pid")
	res = b.Execute(context.Background(), acceptanceBashArgs(t, `sleep 300 & printf '%s\n' "$!" > child.pid`), tctx)
	if res.IsError {
		t.Fatalf("starting tracked child failed: %+v", res)
	}
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || childPID <= 0 {
		t.Fatalf("invalid child pid %q: %v", rawPID, err)
	}
	b.mu.Lock()
	sh := b.sessions[shellKey{sessionID: tctx.SessionID, runID: runID}]
	b.mu.Unlock()
	if sh == nil || sh.cmd == nil || sh.cmd.Process == nil {
		t.Fatal("persistent shell was not registered")
	}
	pgid := sh.cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	}()
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child should be alive before cleanup: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	remaining, err := b.Kill(ctx, runID, 10*time.Millisecond)
	cancel()
	if err != nil || remaining != 0 {
		t.Fatalf("process-group cleanup = remaining %d, err %v", remaining, err)
	}
	if processGroupAlive(pgid) {
		t.Fatalf("process group %d remains alive after Kill", pgid)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d remains after Kill: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.Alive(runID); got != 0 {
		t.Fatalf("BashTool.Alive(%s) = %d, want 0", runID, got)
	}
}
