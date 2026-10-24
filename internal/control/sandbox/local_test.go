package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalDriverEnvironmentAndProcessCleanup(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("process groups are Unix-specific")
	}
	root := t.TempDir()
	envFile := filepath.Join(root, "environment.txt")
	script := filepath.Join(root, "wsd-helper")
	body := fmt.Sprintf("#!/bin/sh\nenv > %q\ntrap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n", envFile)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", "")
	driver := NewLocalDriver(filepath.Join(root, "sandboxes"), script, filepath.Join(root, "template"), filepath.Join(root, "dshd"), "https://goproxy.cn,direct")
	t.Cleanup(func() { _ = driver.Close() })
	convID := "abcdefghij22"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	instance, err := driver.Create(ctx, Spec{ConvID: convID, InstID: "instance-1", ProxyToken: "proxy-token", WSDToken: "wsd-token", InternalURL: "http://127.0.0.1:8082", ImageVer: "local-test"})
	if err != nil {
		t.Fatal(err)
	}
	if instance.PID <= 0 || !strings.HasPrefix(instance.Addr, "127.0.0.1:") {
		t.Fatalf("instance=%+v", instance)
	}
	var environment string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(envFile)
		if err == nil && strings.Contains(string(raw), "WSD_TOKEN=") {
			environment = string(raw)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if environment == "" {
		t.Fatal("helper environment was not written")
	}
	workspace := filepath.Join(root, "sandboxes", convID, "workspace")
	for _, expected := range []string{
		"WSD_LOCAL=true", "WORKSPACE_ROOT=" + workspace,
		"WSD_TEMPLATE_DIR=" + filepath.Join(root, "template"), "WSD_DSHD_BIN=" + filepath.Join(root, "dshd"),
		"WSD_IMAGE_VER=local-test", "WSD_PREVIEW_PORT=0", "SANDBOX_ID=instance-1",
		"CP_INTERNAL_URL=http://127.0.0.1:8082", "LLM_PROXY_TOKEN=proxy-token", "WSD_TOKEN=wsd-token",
	} {
		if !strings.Contains(environment, expected+"\n") {
			t.Errorf("environment lacks %q", expected)
		}
	}
	if !strings.Contains(environment, "WSD_LISTEN="+instance.Addr+"\n") {
		t.Errorf("WSD_LISTEN does not match returned address %s", instance.Addr)
	}
	state, err := driver.Inspect(ctx, convID)
	if err != nil || !state.Running {
		t.Fatalf("running state=%+v err=%v", state, err)
	}
	if err := driver.Stop(ctx, convID); err != nil {
		t.Fatal(err)
	}
	assertProcessGroupGone(t, instance.PID)
	if err := driver.Remove(ctx, convID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sandboxes", convID)); !os.IsNotExist(err) {
		t.Fatalf("conversation directory remains: %v", err)
	}

	second, err := driver.Create(ctx, Spec{ConvID: convID, InstID: "instance-2", ProxyToken: "p", WSDToken: "w", InternalURL: "http://127.0.0.1:8082"})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}
	assertProcessGroupGone(t, second.PID)
}

func assertProcessGroupGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process group %d is still present", pid)
}

func TestDockerDriverFailsClosed(t *testing.T) {
	_, err := (DockerUnavailable{}).Create(context.Background(), Spec{})
	if !errors.Is(err, ErrDriverNotConfigured) {
		t.Fatalf("docker error=%v", err)
	}
}
