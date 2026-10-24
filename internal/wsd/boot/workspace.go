package boot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// InitWorkspace copies the template only into a missing or empty app directory.
// Existing volumes are never reset or recommitted by wsd.
func InitWorkspace(root string, template string) (bool, error) {
	if root == "" || template == "" {
		return false, errors.New("workspace root and template are required")
	}
	for _, dir := range []string{
		root,
		filepath.Join(root, "appdata"),
		filepath.Join(root, ".home"),
		filepath.Join(root, ".dsh"),
		filepath.Join(root, ".dsh", "data"),
		filepath.Join(root, ".dsh", "checkpoints"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, err
		}
	}
	app := filepath.Join(root, "app")
	entries, err := os.ReadDir(app)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(app, 0o700); err != nil {
			return false, err
		}
		entries = nil
	} else if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, nil
	}
	info, err := os.Stat(template)
	if err != nil || !info.IsDir() {
		return false, fmt.Errorf("template directory is unavailable")
	}
	if output, err := exec.Command("cp", "-a", template+string(os.PathSeparator)+".", app+string(os.PathSeparator)).CombinedOutput(); err != nil {
		return false, fmt.Errorf("copy template: %s", limited(output))
	}
	ctx, cancel := context.WithTimeout(context.Background(), workspaceGitTimeout)
	defer cancel()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "platform-bot"},
		{"config", "user.email", "platform-bot@localhost"},
		{"add", "-A"},
		{"commit", "-m", "Initial react-go-sqlite template"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = app
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=C.UTF-8", "GIT_TERMINAL_PROMPT=0"}
		if output, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("initialize template git repository: %s", limited(output))
		}
	}
	return true, nil
}

const workspaceGitTimeout = 30_000_000_000 // 30s; kept untyped for old Go toolchains.

func limited(raw []byte) string {
	if len(raw) > 2048 {
		raw = raw[len(raw)-2048:]
	}
	return string(raw)
}
