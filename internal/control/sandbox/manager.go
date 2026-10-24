package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/internal/control/quota"
	"github.com/0226zy/proto-platform/internal/control/store"
	"github.com/0226zy/proto-platform/internal/kit/config"
	"github.com/0226zy/proto-platform/internal/kit/httpx"
	"github.com/0226zy/proto-platform/internal/kit/idgen"
	"github.com/0226zy/proto-platform/internal/kit/secret"
	"github.com/0226zy/proto-platform/pkg/contract"
)

type Observer interface {
	Attach(Handle)
	Detach(string)
}
type Manager struct {
	st           *store.Store
	driver       Driver
	q            *quota.Manager
	cfg          *config.Config
	ids          idgen.IDGen
	master       []byte
	http         *http.Client
	observer     Observer
	mu           sync.Mutex
	locks        map[string]*sync.Mutex
	reservations map[string]string
}

func NewManager(st *store.Store, d Driver, q *quota.Manager, cfg *config.Config, ids idgen.IDGen, master []byte) *Manager {
	return &Manager{st: st, driver: d, q: q, cfg: cfg, ids: ids, master: master, http: &http.Client{Timeout: 2 * time.Second}, locks: map[string]*sync.Mutex{}, reservations: map[string]string{}}
}
func (m *Manager) SetObserver(observer Observer) { m.observer = observer }
func (m *Manager) lock(conv string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[conv] == nil {
		m.locks[conv] = &sync.Mutex{}
	}
	return m.locks[conv]
}
func (m *Manager) handle(b *store.Sandbox) Handle {
	return Handle{ConvID: b.ConvID, InstID: b.InstID, Addr: b.NetAddr, WSDToken: secret.DeriveWSDToken(m.master, b.InstID)}
}
func (m *Manager) Lookup(ctx context.Context, convID string) (Handle, int, error) {
	b, err := m.st.Sandbox(ctx, m.st.DB(), convID)
	if errors.Is(err, store.ErrNotFound) {
		return Handle{ConvID: convID}, 0, nil
	}
	if err != nil {
		return Handle{}, 0, err
	}
	h := m.handle(b)
	if b.Status == 2 && m.observer != nil {
		m.observer.Attach(h)
	}
	return h, b.Status, nil
}
func (m *Manager) EnsureAsync(convID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.cfg.Sandbox.StartTimeout.Duration+10*time.Second)
		defer cancel()
		c, err := m.st.Conversation(ctx, m.st.DB(), convID)
		if err == nil && c.State == 1 {
			_, _ = m.Ensure(ctx, convID, c.UserID)
		}
	}()
}
func (m *Manager) Ensure(ctx context.Context, convID, userID string) (Handle, error) {
	if _, err := m.st.ConversationOwned(ctx, m.st.DB(), userID, convID); err != nil {
		return Handle{}, httpx.E(404, "NOT_FOUND", "conversation not found")
	}
	l := m.lock(convID)
	l.Lock()
	defer l.Unlock()
	h, status, err := m.Lookup(ctx, convID)
	if err != nil {
		return Handle{}, err
	}
	if status == 2 {
		if err := m.health(ctx, h); err == nil {
			m.touchReservation(convID, userID)
			_ = m.st.TouchSandbox(ctx, convID)
			return h, nil
		}
	}
	var previous *store.Sandbox
	if status != 0 {
		previous, err = m.st.Sandbox(ctx, m.st.DB(), convID)
		if err != nil {
			return Handle{}, err
		}
	}
	// Stop the previous instance before replacing its token and address.
	if status != 0 && status != 5 {
		_ = m.driver.Stop(ctx, convID)
	}
	inst := m.ids.ULID()
	proxyToken := m.ids.Token()
	h = Handle{ConvID: convID, InstID: inst, WSDToken: secret.DeriveWSDToken(m.master, inst)}
	now := time.Now().UTC()
	exp := now.Add(m.cfg.Sandbox.MaxLifetime.Duration)
	b := &store.Sandbox{ConvID: convID, InstID: inst, Status: 1, VolumeName: "conv_" + convID, ImageVer: m.cfg.Local.ImageVer, TokenHash: secret.Hash(proxyToken), TokenExpiresAt: &exp, LastActiveAt: now}
	if previous != nil {
		// A process replacement does not invalidate the last confirmed build.
		// Preserve its fencing metadata so detail and Entry never regress to none/0.
		b.PreviewStatus = previous.PreviewStatus
		b.PreviewGeneration = previous.PreviewGeneration
		b.PreviewCommit = previous.PreviewCommit
	}
	if err := m.st.PutSandbox(ctx, m.st.DB(), b); err != nil {
		return Handle{}, err
	}
	if err := m.acquireSandbox(ctx, convID, userID); err != nil {
		_ = m.st.RevokeSandbox(ctx, m.st.DB(), convID, 4)
		return Handle{}, err
	}
	instInfo, err := m.driver.Create(ctx, Spec{ConvID: convID, InstID: inst, ProxyToken: proxyToken, WSDToken: h.WSDToken, InternalURL: m.cfg.Sandbox.InternalURL, ImageVer: m.cfg.Local.ImageVer})
	if err != nil {
		m.failStart(ctx, convID)
		return Handle{}, err
	}
	h.Addr = instInfo.Addr
	b.Status = 2
	b.ContainerID = instInfo.ID
	b.NetAddr = instInfo.Addr
	deadline := time.NewTimer(m.cfg.Sandbox.StartTimeout.Duration)
	defer deadline.Stop()
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := m.health(ctx, h); err == nil {
			if err := m.st.PutSandbox(ctx, m.st.DB(), b); err != nil {
				return Handle{}, err
			}
			return h, nil
		}
		select {
		case <-ctx.Done():
			m.failStart(ctx, convID)
			return Handle{}, ctx.Err()
		case <-deadline.C:
			m.failStart(ctx, convID)
			return Handle{}, fmt.Errorf("wsd health check timed out")
		case <-tick.C:
		}
	}
}
func (m *Manager) health(ctx context.Context, h Handle) error {
	req, err := http.NewRequestWithContext(ctx, "GET", h.BaseURL()+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("wsd health status %d", resp.StatusCode)
	}
	var v contract.Health
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return err
	}
	if !strings.HasPrefix(v.API, "wsd/") {
		return fmt.Errorf("unexpected wsd api %q", v.API)
	}
	return nil
}
func (m *Manager) acquireSandbox(ctx context.Context, conv, user string) error {
	m.mu.Lock()
	if _, ok := m.reservations[conv]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	r, err := m.q.Acquire(ctx, quota.KindSandbox, user, conv)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.reservations[conv] = r.ID
	m.mu.Unlock()
	return nil
}
func (m *Manager) touchReservation(conv, user string) {
	m.mu.Lock()
	_, ok := m.reservations[conv]
	m.mu.Unlock()
	if !ok {
		r := quota.Reservation{ID: "sandbox-" + conv, Kind: quota.KindSandbox, UserID: user, ConvID: conv, AcquiredAt: time.Now()}
		m.q.Restore([]quota.Reservation{r})
		m.mu.Lock()
		m.reservations[conv] = r.ID
		m.mu.Unlock()
	}
}
func (m *Manager) release(conv string) {
	if m.observer != nil {
		m.observer.Detach(conv)
	}
	m.mu.Lock()
	id := m.reservations[conv]
	delete(m.reservations, conv)
	m.mu.Unlock()
	if id != "" {
		m.q.Release(id)
	}
}
func (m *Manager) failStart(ctx context.Context, conv string) {
	c, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = m.driver.Stop(c, conv)
	_ = m.st.RevokeSandbox(ctx, m.st.DB(), conv, 4)
	m.release(conv)
}
func (m *Manager) Stop(ctx context.Context, convID, reason string) error {
	if err := m.driver.Stop(ctx, convID); err != nil {
		return err
	}
	if err := m.st.RevokeSandbox(ctx, m.st.DB(), convID, 3); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	m.release(convID)
	return nil
}
func (m *Manager) ForceStop(ctx context.Context, convID string) error {
	if err := m.driver.Stop(ctx, convID); err != nil {
		return err
	}
	st, err := m.driver.Inspect(ctx, convID)
	if err != nil {
		return err
	}
	if st.Running {
		return fmt.Errorf("sandbox process is still running")
	}
	if err := m.st.RevokeSandbox(ctx, m.st.DB(), convID, 3); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	m.release(convID)
	return nil
}
func (m *Manager) Remove(ctx context.Context, convID string) error {
	if err := m.driver.Remove(ctx, convID, true); err != nil {
		return err
	}
	if err := m.st.RevokeSandbox(ctx, m.st.DB(), convID, 5); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	m.release(convID)
	return nil
}
func (m *Manager) Touch(convID string) { _ = m.st.TouchSandbox(context.Background(), convID) }
func (m *Manager) UpdatePreview(ctx context.Context, convID string, st contract.PreviewState) error {
	return m.st.UpdateSandboxPreview(ctx, m.st.DB(), convID, st.Status, st.Commit, st.Generation)
}
func (m *Manager) Authenticate(ctx context.Context, bearer string) (Principal, error) {
	b, err := m.st.SandboxByToken(ctx, secret.Hash(bearer))
	if err != nil || b.TokenExpiresAt == nil || time.Now().After(*b.TokenExpiresAt) {
		return Principal{}, httpx.E(401, "UNAUTHENTICATED", "invalid sandbox token")
	}
	c, err := m.st.Conversation(ctx, m.st.DB(), b.ConvID)
	if err != nil || c.State != 1 {
		return Principal{}, httpx.E(401, "UNAUTHENTICATED", "invalid sandbox token")
	}
	return Principal{ConvID: b.ConvID, UserID: c.UserID, InstID: b.InstID}, nil
}
func (m *Manager) RequireActiveRun(ctx context.Context, p Principal) (string, error) {
	// Leaving running revokes authorization immediately. A time-based positive
	// cache would keep a finished, failed, cancelled or finalizing Run authorized.
	r, err := m.st.ActiveRunningRun(ctx, m.st.DB(), p.ConvID)
	if err != nil {
		return "", httpx.E(403, "RUN_NOT_ACTIVE", "no running Run for sandbox")
	}
	return r.RunID, nil
}
func (m *Manager) Reconcile(ctx context.Context) error {
	items, err := m.st.ListSandboxes(ctx)
	if err != nil {
		return err
	}
	for _, b := range items {
		if b.Status == 1 {
			_ = m.st.RevokeSandbox(ctx, m.st.DB(), b.ConvID, 3)
			continue
		}
		if b.Status == 2 {
			c, err := m.st.Conversation(ctx, m.st.DB(), b.ConvID)
			if err != nil {
				continue
			}
			h := m.handle(&b)
			if err := m.health(ctx, h); err == nil {
				m.touchReservation(b.ConvID, c.UserID)
				if m.observer != nil {
					m.observer.Attach(h)
				}
			} else {
				_ = m.st.RevokeSandbox(ctx, m.st.DB(), b.ConvID, 3)
			}
		}
	}
	return nil
}
func (m *Manager) Loops(ctx context.Context) {
	t := time.NewTicker(m.cfg.Sandbox.HealthInterval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			items, _ := m.st.ListSandboxes(ctx)
			for _, b := range items {
				if b.Status != 2 {
					continue
				}
				if r, err := m.st.ActiveRun(ctx, m.st.DB(), b.ConvID); err == nil && r.Active {
					_ = m.st.TouchSandbox(ctx, b.ConvID)
					continue
				}
				if time.Since(b.LastActiveAt) > m.cfg.Sandbox.IdleStopAfter.Duration {
					c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					_ = m.Stop(c, b.ConvID, "idle")
					cancel()
				}
			}
		}
	}
}
func (m *Manager) Close() error { return m.driver.Close() }
