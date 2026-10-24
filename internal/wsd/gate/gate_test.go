package gate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGateFencingReentryAndRelease(t *testing.T) {
	g := &Gate{}
	ctx := context.Background()
	grant, err := g.AcquireRun(ctx, "run-1", 2)
	if err != nil || grant.Generation != 2 {
		t.Fatalf("acquire = %+v, %v", grant, err)
	}
	if _, err := g.AcquireRun(ctx, "run-1", 2); err != nil {
		t.Fatalf("same run re-entry: %v", err)
	}
	immediate, cancel := context.WithTimeout(ctx, 0)
	defer cancel()
	if _, err := g.AcquireRun(immediate, "run-2", 2); !errors.Is(err, ErrBusy) {
		t.Fatalf("competing run = %v", err)
	}
	if _, err := g.AcquireRun(ctx, "run-1", 1); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale re-entry = %v", err)
	}
	if err := g.ReleaseRun("run-2"); !errors.Is(err, ErrNotHolder) {
		t.Fatalf("wrong release = %v", err)
	}
	if err := g.ReleaseRun("run-1"); err != nil {
		t.Fatal(err)
	}
	if err := g.ReleaseRun("run-1"); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	release, err := g.AcquirePreview(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if state := g.State(); !state.Held || state.Holder != "preview" || state.MaxGeneration != 3 {
		t.Fatalf("preview state = %+v", state)
	}
	release()
	release()
	if _, err := g.AcquireRun(ctx, "run-3", 2); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old generation after preview = %v", err)
	}
}

func TestGateWaiterWakesOnRelease(t *testing.T) {
	g := &Gate{}
	if _, err := g.AcquireRun(context.Background(), "run-1", 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := g.AcquirePreview(ctx, 1)
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := g.ReleaseRun("run-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("waiter did not wake")
	}
}
