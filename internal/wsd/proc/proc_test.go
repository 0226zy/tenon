//go:build linux || darwin

package proc

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestReapDoesNotKillRegisteredPreview(t *testing.T) {
	registry := New()
	preview := exec.Command("sleep", "30")
	previewPGID, err := StartProcess(preview)
	if err != nil {
		t.Fatal(err)
	}
	registry.Own("preview", previewPGID)
	previewDone := make(chan error, 1)
	go func() { previewDone <- preview.Wait() }()
	defer func() {
		registry.Disown("preview")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = StopGroup(ctx, previewPGID, 50*time.Millisecond)
		select {
		case <-previewDone:
		case <-time.After(time.Second):
		}
	}()

	orphan := exec.Command("sleep", "30")
	if _, err := StartProcess(orphan); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before, _ := registry.orphanProcesses()
	t.Logf("orphans before reap: %+v", before)
	killed, remaining, err := registry.Reap(ctx, 50*time.Millisecond)
	left, _ := registry.orphanProcesses()
	t.Logf("orphans after reap: %+v", left)
	if err != nil || remaining != 0 || killed == 0 {
		t.Fatalf("reap = killed %d remaining %d, %v", killed, remaining, err)
	}
	if err := preview.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("registered preview was killed: %v", err)
	}
}
