package llm

import (
	"sync"
	"time"
)

type Limiter struct {
	mu                  sync.Mutex
	active              map[string]int
	requests            map[string][]time.Time
	perConv, perUserQPS int
}

func NewLimiter(perConv, perUserQPS int) *Limiter {
	return &Limiter{active: map[string]int{}, requests: map[string][]time.Time{}, perConv: perConv, perUserQPS: perUserQPS}
}
func (l *Limiter) Allow(convID, userID string) (func(), time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	times := l.requests[userID]
	cut := now.Add(-time.Second)
	kept := times[:0]
	for _, t := range times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	times = kept
	if l.active[convID] >= l.perConv {
		l.requests[userID] = times
		return func() {}, time.Second, false
	}
	if len(times) >= l.perUserQPS {
		retry := time.Until(times[0].Add(time.Second))
		if retry < 0 {
			retry = time.Millisecond
		}
		l.requests[userID] = times
		return func() {}, retry, false
	}
	l.active[convID]++
	l.requests[userID] = append(times, now)
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.active[convID]--
			if l.active[convID] <= 0 {
				delete(l.active, convID)
			}
			l.mu.Unlock()
		})
	}, 0, true
}
