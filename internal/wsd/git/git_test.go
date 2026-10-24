package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/0226zy/protoforge/internal/wsd/bus"
	"github.com/0226zy/protoforge/internal/wsd/gate"
	"github.com/0226zy/protoforge/pkg/contract"
)

func TestCommitIdempotencyAndLog(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Test Bot"}, {"config", "user.email", "test@localhost"}} {
		runGitTest(t, root, args...)
	}
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "add", "-A")
	runGitTest(t, root, "commit", "-m", "initial")

	workspaceGate := &gate.Gate{}
	service := New(root, filepath.Join(t.TempDir(), "checkpoints"), workspaceGate, bus.New())
	ctx := context.Background()
	if _, err := workspaceGate.AcquireRun(ctx, "run-1", 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := contract.CommitRequest{RunID: "run-1", Generation: 1, Message: "first checkpoint"}
	first, err := service.Commit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.Commit == "" || first.FilesTotal != 2 || first.CheckpointStatus != "done" {
		t.Fatalf("first = %+v", first)
	}
	if err := os.WriteFile(filepath.Join(root, "app.txt"), []byte("retry must not commit"), 0o600); err != nil {
		t.Fatal(err)
	}
	retry, err := service.Commit(ctx, contract.CommitRequest{RunID: "run-1", Generation: 1, Message: "different message"})
	if err != nil || retry.Commit != first.Commit || retry.FilesTotal != first.FilesTotal {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	if got := runGitTest(t, root, "rev-parse", "HEAD"); got != first.Commit {
		t.Fatalf("HEAD changed on retry: %s != %s", got, first.Commit)
	}
	if err := workspaceGate.ReleaseRun("run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Commit(ctx, req); err == nil {
		t.Fatal("checkpoint without gate succeeded")
	}

	if _, err := workspaceGate.AcquireRun(ctx, "run-2", 2); err != nil {
		t.Fatal(err)
	}
	second, err := service.Commit(ctx, contract.CommitRequest{RunID: "run-2", Generation: 2, Message: "second checkpoint"})
	if err != nil || !second.Changed || second.Commit == first.Commit {
		t.Fatalf("second = %+v, %v", second, err)
	}
	entries, err := service.Log(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].RunID != "run-2" || entries[1].RunID != "run-1" || entries[0].Message != "second checkpoint" {
		t.Fatalf("log = %+v", entries)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(bytesTrimSpace(output))
}

func bytesTrimSpace(raw []byte) []byte {
	for len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\n' || raw[0] == '\r' || raw[0] == '\t') {
		raw = raw[1:]
	}
	for len(raw) > 0 && (raw[len(raw)-1] == ' ' || raw[len(raw)-1] == '\n' || raw[len(raw)-1] == '\r' || raw[len(raw)-1] == '\t') {
		raw = raw[:len(raw)-1]
	}
	return raw
}
