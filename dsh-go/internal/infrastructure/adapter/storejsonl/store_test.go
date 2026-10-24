package storejsonl

import (
	"testing"

	"github.com/0226zy/magents/internal/domain/entity"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// append-only 日志：seq 连续、事件只增不改。
func TestCreateAppendLoad(t *testing.T) {
	s := newStore(t)
	header := entity.SessionHeader{ID: "s1", Cwd: "/tmp/p", Mode: "auto-edit"}
	if err := s.Create(header); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 3; i++ {
		ev, err := s.Append("s1", entity.Event{Kind: entity.EventMessage, Message: &entity.Message{Role: "user", Content: "hi"}})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if ev.Seq != i+1 { // 0 号是 created marker
			t.Fatalf("seq = %d, 期望 %d（相邻性契约）", ev.Seq, i+1)
		}
	}
	h, events, err := s.Load("s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h.ID != "s1" || h.Mode != "auto-edit" || h.Epoch == "" {
		t.Fatalf("header = %+v", h)
	}
	if len(events) != 4 || events[0].Kind != entity.EventMarker {
		t.Fatalf("events = %d 条, 首条 kind = %s", len(events), events[0].Kind)
	}
}

// fork：复制前缀、血缘 header、fork marker；子会话继续追加不改写继承部分。
func TestFork(t *testing.T) {
	s := newStore(t)
	if err := s.Create(entity.SessionHeader{ID: "parent", Cwd: "/tmp", Mode: "suggest", Model: "kimi"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Append("parent", entity.Event{Kind: entity.EventMessage, Message: &entity.Message{Role: "user", Content: string(rune('a' + i))}}); err != nil {
			t.Fatal(err)
		}
	}
	parentHeader, _, err := s.Load("parent")
	if err != nil {
		t.Fatal(err)
	}
	header, err := s.Fork("parent", "child", 3)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if header.ParentID != "parent" || header.InheritedEventCount != 3 || header.Model != "kimi" || header.Epoch == "" || header.Epoch == parentHeader.Epoch {
		t.Fatalf("fork header = %+v", header)
	}
	_, events, err := s.Load("child")
	if err != nil {
		t.Fatal(err)
	}
	// 3 条继承 + 1 条 forked marker
	if len(events) != 4 || events[3].Marker == nil || events[3].Marker.Type != "forked" {
		t.Fatalf("child events = %d 条", len(events))
	}
	// 子会话追加：seq 从 4 开始
	ev, err := s.Append("child", entity.Event{Kind: entity.EventMessage, Message: &entity.Message{Role: "user", Content: "new"}})
	if err != nil || ev.Seq != 4 {
		t.Fatalf("fork 后追加 seq = %d, err = %v, 期望 4", ev.Seq, err)
	}
	// 父会话不受影响
	_, parentEvents, _ := s.Load("parent")
	if len(parentEvents) != 6 {
		t.Fatalf("父会话被改写: %d 条事件", len(parentEvents))
	}
	// 非法切点
	if _, err := s.Fork("parent", "bad", 99); err == nil {
		t.Fatal("非法切点未报错")
	}
}

func TestListAndUpdateHeader(t *testing.T) {
	s := newStore(t)
	_ = s.Create(entity.SessionHeader{ID: "a", Cwd: "/tmp", Mode: "suggest"})
	_ = s.Create(entity.SessionHeader{ID: "b", Cwd: "/tmp", Mode: "auto-edit"})
	headers, err := s.List()
	if err != nil || len(headers) != 2 {
		t.Fatalf("List: %v, %d", err, len(headers))
	}
	if err := s.UpdateHeader(entity.SessionHeader{ID: "a", Cwd: "/tmp", Mode: "full-auto"}); err != nil {
		t.Fatalf("UpdateHeader: %v", err)
	}
	h, _, _ := s.Load("a")
	if h.Mode != "full-auto" {
		t.Fatalf("mode = %q, 期望 full-auto", h.Mode)
	}
}
