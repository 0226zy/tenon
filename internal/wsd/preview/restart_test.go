//go:build linux || darwin

package preview

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/pkg/contract"
)

func TestRestartLogsAndAutomaticRestartLimit(t *testing.T) {
	supervisor, fake := newTestSupervisor(t, writePreviewApp(t), 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := supervisor.Ensure(ctx, EnsureReq{Generation: 1, Commit: "abc", Wait: 3 * time.Second}, "")
	if err != nil || state.Status != "ready" {
		t.Fatalf("initial = %+v, %v", state, err)
	}
	if err := supervisor.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	state = waitPreviewState(t, supervisor, func(state contract.PreviewState) bool {
		return state.Status == "ready" && fake.acquires.Load() == 2
	})
	if state.Restarts != 0 {
		t.Fatalf("manual restart counted as crash restart: %+v", state)
	}
	if !strings.Contains(string(supervisor.Logs(200)), "starting preview") {
		t.Fatalf("preview logs are missing output: %q", supervisor.Logs(200))
	}

	killCurrentPreview(t, supervisor)
	state = waitPreviewState(t, supervisor, func(state contract.PreviewState) bool {
		return state.Status == "ready" && state.Restarts == 1 && fake.acquires.Load() == 3
	})
	if state.ErrorCode != "" {
		t.Fatalf("automatic restart = %+v", state)
	}
	killCurrentPreview(t, supervisor)
	state = waitPreviewState(t, supervisor, func(state contract.PreviewState) bool {
		return state.Status == "failed" && state.Restarts == 1
	})
	if state.ErrorCode != "BUILD_FAILED" || fake.acquires.Load() != 3 {
		t.Fatalf("restart limit = %+v, acquires=%d", state, fake.acquires.Load())
	}
}

func waitPreviewState(t *testing.T, supervisor *Supervisor, ready func(contract.PreviewState) bool) contract.PreviewState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := supervisor.Status()
		if ready(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for preview state: %+v", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func killCurrentPreview(t *testing.T, supervisor *Supervisor) {
	t.Helper()
	supervisor.mu.Lock()
	process := supervisor.process
	supervisor.mu.Unlock()
	if process == nil {
		t.Fatal("preview process is nil")
	}
	if err := process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
}
