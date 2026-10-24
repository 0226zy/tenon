package conversation

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/0226zy/protoforge/internal/control/event"
	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/internal/kit/httpx"
	"github.com/0226zy/protoforge/internal/kit/idgen"
	"github.com/0226zy/protoforge/pkg/contract"
)

type conversationCatalog struct{}

func (conversationCatalog) Default() string { return "local-demo" }
func (conversationCatalog) Validate(model string) error {
	if model != "local-demo" {
		return errors.New("unknown model")
	}
	return nil
}

func TestConcurrentCreateHonorsPerUserLimit(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, "sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(ctx, st.DB(), &store.User{UserID: "alice", Name: "Alice", PwdHash: "hash", Status: 1, MaxConv: 2}); err != nil {
		t.Fatal(err)
	}
	hub := event.NewHub()
	service := New(st, event.NewStore(st, hub), idgen.New(), conversationCatalog{})
	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.Create(context.Background(), "alice", contract.CreateConversationRequest{})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, limited := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
			continue
		}
		var httpErr *httpx.Error
		if errors.As(err, &httpErr) && httpErr.Status == 429 && httpErr.Code == "CONV_LIMIT" {
			limited++
		} else {
			t.Errorf("unexpected create error: %v", err)
		}
	}
	if succeeded != 2 || limited != attempts-2 {
		t.Fatalf("succeeded=%d limited=%d", succeeded, limited)
	}
	count, err := st.CountConversations(ctx, st.DB(), "alice")
	if err != nil || count != 2 {
		t.Fatalf("stored conversations=%d err=%v", count, err)
	}
}
