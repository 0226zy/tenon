// Package quota implements bounded, per-user fair in-process reservations.
package quota

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/kit/config"
)

type Kind int

const (
	KindRun Kind = iota
	KindSandbox
	KindBuild
)

var ErrQueueFull = errors.New("quota queue full")

type Reservation struct {
	ID             string
	Kind           Kind
	UserID, ConvID string
	AcquiredAt     time.Time
}
type Stats struct {
	Active, Queued int
	Users          map[string]int
}
type Manager struct {
	mu    sync.Mutex
	cfg   config.Quota
	pools map[Kind]*pool
}
type pool struct {
	active  map[string]Reservation
	perUser map[string]int
	queues  map[string][]*waiter
	order   []string
	rr      int
}
type waiter struct {
	id               string
	kind             Kind
	user, conv       string
	ch               chan Reservation
	queued, acquired bool
}

func New(cfg config.Quota) *Manager {
	m := &Manager{cfg: cfg, pools: map[Kind]*pool{}}
	for _, k := range []Kind{KindRun, KindSandbox, KindBuild} {
		m.pools[k] = &pool{active: map[string]Reservation{}, perUser: map[string]int{}, queues: map[string][]*waiter{}}
	}
	return m
}
func (m *Manager) limits(k Kind) (int, int) {
	switch k {
	case KindRun:
		return m.cfg.Run.Global, m.cfg.Run.PerUser
	case KindSandbox:
		return m.cfg.Sandbox.Global, m.cfg.Sandbox.PerUser
	default:
		return m.cfg.Build.Global, m.cfg.Build.PerUser
	}
}
func newID() string { b := make([]byte, 16); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func (m *Manager) Acquire(ctx context.Context, k Kind, userID, convID string) (Reservation, error) {
	m.mu.Lock()
	p := m.pools[k]
	if m.queueLenLocked(k, userID) >= m.cfg.Queue.PerUser {
		m.mu.Unlock()
		return Reservation{}, ErrQueueFull
	}
	w := m.addWaiterLocked(k, userID, convID)
	m.scheduleLocked(k)
	m.mu.Unlock()
	select {
	case r := <-w.ch:
		return r, nil
	case <-ctx.Done():
		m.mu.Lock()
		if w.queued {
			m.removeWaiterLocked(p, w)
		}
		acquired := w.acquired
		m.scheduleLocked(k)
		m.mu.Unlock()
		if acquired {
			select {
			case r := <-w.ch:
				m.Release(r.ID)
			default:
			}
		}
		return Reservation{}, ctx.Err()
	}
}
func (m *Manager) TryAcquire(k Kind, userID, convID string) (Reservation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pools[k]
	w := m.addWaiterLocked(k, userID, convID)
	m.scheduleLocked(k)
	if w.acquired {
		select {
		case r := <-w.ch:
			return r, true
		default:
		}
	}
	if w.queued {
		m.removeWaiterLocked(p, w)
	}
	return Reservation{}, false
}
func (m *Manager) addWaiterLocked(k Kind, user, conv string) *waiter {
	p := m.pools[k]
	if _, ok := p.queues[user]; !ok {
		p.order = append(p.order, user)
	}
	w := &waiter{id: newID(), kind: k, user: user, conv: conv, ch: make(chan Reservation, 1), queued: true}
	p.queues[user] = append(p.queues[user], w)
	return w
}
func (m *Manager) scheduleLocked(k Kind) {
	p := m.pools[k]
	global, per := m.limits(k)
	for len(p.active) < global && len(p.order) > 0 {
		granted := false
		for n := 0; n < len(p.order); n++ {
			idx := (p.rr + n) % len(p.order)
			u := p.order[idx]
			q := p.queues[u]
			if len(q) == 0 || p.perUser[u] >= per {
				continue
			}
			w := q[0]
			q = q[1:]
			if len(q) == 0 {
				delete(p.queues, u)
			} else {
				p.queues[u] = q
			}
			w.queued = false
			w.acquired = true
			r := Reservation{ID: w.id, Kind: k, UserID: u, ConvID: w.conv, AcquiredAt: time.Now().UTC()}
			p.active[r.ID] = r
			p.perUser[u]++
			w.ch <- r
			p.rr = (idx + 1) % len(p.order)
			granted = true
			break
		}
		m.compactOrderLocked(p)
		if !granted {
			break
		}
	}
}
func (m *Manager) compactOrderLocked(p *pool) {
	out := p.order[:0]
	for _, u := range p.order {
		if len(p.queues[u]) > 0 {
			out = append(out, u)
		}
	}
	p.order = out
	if len(p.order) == 0 {
		p.rr = 0
	} else {
		p.rr %= len(p.order)
	}
}
func (m *Manager) removeWaiterLocked(p *pool, w *waiter) {
	q := p.queues[w.user]
	for i, x := range q {
		if x == w {
			q = append(q[:i], q[i+1:]...)
			w.queued = false
			break
		}
	}
	if len(q) == 0 {
		delete(p.queues, w.user)
	} else {
		p.queues[w.user] = q
	}
	m.compactOrderLocked(p)
}
func (m *Manager) Release(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, p := range m.pools {
		if r, ok := p.active[id]; ok {
			delete(p.active, id)
			p.perUser[r.UserID]--
			if p.perUser[r.UserID] <= 0 {
				delete(p.perUser, r.UserID)
			}
			m.scheduleLocked(k)
			return
		}
	}
}
func (m *Manager) QueueLen(userID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, k := range []Kind{KindRun, KindSandbox, KindBuild} {
		n += m.queueLenLocked(k, userID)
	}
	return n
}
func (m *Manager) queueLenLocked(k Kind, userID string) int { return len(m.pools[k].queues[userID]) }
func (m *Manager) Snapshot() map[string]Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Stats{}
	for k, p := range m.pools {
		st := Stats{Active: len(p.active), Users: map[string]int{}}
		for u, n := range p.perUser {
			st.Users[u] = n
		}
		for _, q := range p.queues {
			st.Queued += len(q)
		}
		name := map[Kind]string{KindRun: "run", KindSandbox: "sandbox", KindBuild: "build"}[k]
		out[name] = st
	}
	return out
}
func (m *Manager) Restore(rs []Reservation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		p := m.pools[r.Kind]
		p.active[r.ID] = r
		p.perUser[r.UserID]++
	}
}
func (m *Manager) ReapExpired(kind Kind, maxAge time.Duration) []Reservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.pools[kind]
	now := time.Now()
	var expired []Reservation
	for id, r := range p.active {
		if now.Sub(r.AcquiredAt) > maxAge {
			delete(p.active, id)
			p.perUser[r.UserID]--
			expired = append(expired, r)
		}
	}
	m.scheduleLocked(kind)
	return expired
}
func (m *Manager) ReleaseConv(kind Kind, convID string) {
	m.mu.Lock()
	ids := []string{}
	p := m.pools[kind]
	for id, r := range p.active {
		if r.ConvID == convID {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Release(id)
	}
}
