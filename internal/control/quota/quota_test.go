package quota

import (
	"context"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/internal/kit/config"
)

func TestFairRoundRobinAcrossUsers(t *testing.T) {
	for _, item := range []struct {
		name string
		kind Kind
	}{{"run", KindRun}, {"sandbox", KindSandbox}, {"build", KindBuild}} {
		t.Run(item.name, func(t *testing.T) {
			limits := config.Limit{Global: 2, PerUser: 2}
			q := New(config.Quota{Run: limits, Sandbox: limits, Build: limits, Queue: config.Queue{PerUser: 10, Timeout: config.Duration{Duration: time.Second}}})
			ctx := context.Background()
			a1, ok := q.TryAcquire(item.kind, "alice", "a1")
			if !ok {
				t.Fatal("first reservation was not granted")
			}
			a2, ok := q.TryAcquire(item.kind, "alice", "a2")
			if !ok {
				t.Fatal("second reservation was not granted")
			}
			type result struct {
				user string
				r    Reservation
				err  error
			}
			results := make(chan result, 4)
			acquire := func(user, conv string) {
				r, err := q.Acquire(ctx, item.kind, user, conv)
				results <- result{user: user, r: r, err: err}
			}
			go acquire("alice", "a3")
			waitQueue(t, q, "alice", 1)
			go acquire("bob", "b1")
			waitQueue(t, q, "bob", 1)
			go acquire("alice", "a4")
			waitQueue(t, q, "alice", 2)
			go acquire("bob", "b2")
			waitQueue(t, q, "bob", 2)

			q.Release(a1.ID)
			first := waitResult(t, results)
			if first.err != nil || first.user != "alice" || first.r.ConvID != "a3" {
				t.Fatalf("first queued reservation = %+v, want alice/a3", first)
			}
			q.Release(a2.ID)
			second := waitResult(t, results)
			if second.err != nil || second.user != "bob" || second.r.ConvID != "b1" {
				t.Fatalf("second queued reservation = %+v, want bob/b1 after rotation", second)
			}
			q.Release(first.r.ID)
			third := waitResult(t, results)
			if third.err != nil || third.user != "alice" || third.r.ConvID != "a4" {
				t.Fatalf("third queued reservation = %+v, want alice/a4", third)
			}
			q.Release(second.r.ID)
			fourth := waitResult(t, results)
			if fourth.err != nil || fourth.user != "bob" || fourth.r.ConvID != "b2" {
				t.Fatalf("fourth queued reservation = %+v, want bob/b2", fourth)
			}
			q.Release(third.r.ID)
			q.Release(fourth.r.ID)
			stats := q.Snapshot()[item.name]
			if stats.Active != 0 || stats.Queued != 0 {
				t.Fatalf("quota did not drain: %+v", stats)
			}
		})
	}
}

func TestQuotaKindsEnforcePerUserAndGlobalLimits(t *testing.T) {
	for _, item := range []struct {
		name string
		kind Kind
	}{{"run", KindRun}, {"sandbox", KindSandbox}, {"build", KindBuild}} {
		t.Run(item.name, func(t *testing.T) {
			limits := config.Limit{Global: 2, PerUser: 1}
			q := New(config.Quota{Run: limits, Sandbox: limits, Build: limits})
			alice, ok := q.TryAcquire(item.kind, "alice", "a1")
			if !ok {
				t.Fatal("first Alice reservation was rejected")
			}
			if _, ok := q.TryAcquire(item.kind, "alice", "a2"); ok {
				t.Fatal("per-user limit was not enforced with global capacity available")
			}
			bob, ok := q.TryAcquire(item.kind, "bob", "b1")
			if !ok {
				t.Fatal("another user did not receive remaining global capacity")
			}
			if _, ok := q.TryAcquire(item.kind, "carol", "c1"); ok {
				t.Fatal("global limit was not enforced")
			}
			q.Release(alice.ID)
			carol, ok := q.TryAcquire(item.kind, "carol", "c2")
			if !ok {
				t.Fatal("released global capacity was not reusable")
			}
			q.Release(bob.ID)
			q.Release(carol.ID)
		})
	}
}

func waitQueue(t *testing.T, q *Manager, user string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.QueueLen(user) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue for %s did not reach %d", user, want)
}

func waitResult[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(time.Second):
		t.Fatal("reservation did not complete")
		var zero T
		return zero
	}
}
