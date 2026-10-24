package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/internal/wsd/bus"
)

func TestFileChangesAreDebouncedAndMerged(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "initial.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := bus.New()
	messages, cancelSubscription := b.Subscribe()
	defer cancelSubscription()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Start(ctx, root, nil, 500*time.Millisecond, b) }()
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(root, "initial.txt"), []byte{byte('b' + i)}, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	var event struct {
		Paths []string `json:"paths"`
	}
	select {
	case msg := <-messages:
		if msg.Type != "file_changed" {
			t.Fatalf("event type = %s", msg.Type)
		}
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			t.Fatal(err)
		}
		if len(event.Paths) != 1 || event.Paths[0] != "initial.txt" {
			t.Fatalf("paths = %v", event.Paths)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no file_changed event")
	}
	select {
	case msg := <-messages:
		t.Fatalf("duplicate event without a new change: %s", msg.Data)
	case <-time.After(700 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
