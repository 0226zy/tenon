// Package auth implements Argon2id passwords, server-side sessions and principals.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/internal/kit/config"
	"github.com/0226zy/protoforge/internal/kit/httpx"
	"github.com/0226zy/protoforge/internal/kit/idgen"
	"github.com/0226zy/protoforge/internal/kit/secret"
	"golang.org/x/crypto/argon2"
)

type Principal struct{ UserID, Name string }
type Provider interface {
	Authenticate(*http.Request) (Principal, error)
}
type Service struct {
	st       *store.Store
	ids      idgen.IDGen
	cfg      config.Auth
	dummy    string
	mu       sync.Mutex
	failures map[string]*failure
}
type failure struct {
	Count              int
	First, LockedUntil time.Time
}

func New(st *store.Store, ids idgen.IDGen, cfg config.Auth) *Service {
	s := &Service{st: st, ids: ids, cfg: cfg, failures: map[string]*failure{}}
	s.dummy, _ = s.HashPassword("not-the-user-password")
	return s
}
func (s *Service) HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", httpx.E(400, "WEAK_PASSWORD", "password must contain at least 8 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, s.cfg.Argon2.Time, s.cfg.Argon2.Memory, s.cfg.Argon2.Threads, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", s.cfg.Argon2.Memory, s.cfg.Argon2.Time, s.cfg.Argon2.Threads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}
func verify(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
func (s *Service) Login(ctx context.Context, userID, password, ip string) (string, time.Time, error) {
	now := time.Now().UTC()
	key := userID + "|" + ip
	s.mu.Lock()
	f := s.failures[key]
	if f != nil && now.Before(f.LockedUntil) {
		s.mu.Unlock()
		err := httpx.E(429, "RATE_LIMITED", "too many login failures")
		err.Header = make(http.Header)
		err.Header.Set("Retry-After", fmt.Sprint(int(time.Until(f.LockedUntil).Seconds())+1))
		return "", time.Time{}, err
	}
	s.mu.Unlock()
	u, err := s.st.User(ctx, s.st.DB(), userID)
	hash := s.dummy
	if err == nil {
		hash = u.PwdHash
	}
	ok := verify(hash, password)
	if err != nil || u.Status != 1 || !ok {
		s.mu.Lock()
		f = s.failures[key]
		if f == nil || now.Sub(f.First) > s.cfg.LoginLock.Duration {
			f = &failure{First: now}
			s.failures[key] = f
		}
		f.Count++
		if f.Count >= s.cfg.LoginFailLimit {
			f.LockedUntil = now.Add(s.cfg.LoginLock.Duration)
		}
		s.mu.Unlock()
		return "", time.Time{}, httpx.E(http.StatusUnauthorized, "UNAUTHENTICATED", "invalid username or password")
	}
	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()
	raw := s.ids.Token()
	exp := now.Add(s.cfg.SessionTTL.Duration)
	if err := s.st.PutSession(ctx, s.st.DB(), &store.AuthSession{Hash: secret.Hash(raw), UserID: userID, ExpiresAt: exp}); err != nil {
		return "", time.Time{}, err
	}
	return raw, exp, nil
}
func (s *Service) Logout(ctx context.Context, raw string) error {
	if raw == "" {
		return nil
	}
	return s.st.DeleteSession(ctx, secret.Hash(raw))
}
func (s *Service) Authenticate(r *http.Request) (Principal, error) {
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil || c.Value == "" {
		return Principal{}, httpx.E(401, "UNAUTHENTICATED", "authentication required")
	}
	u, err := s.st.UserBySession(r.Context(), secret.Hash(c.Value), time.Now().UTC())
	if err != nil {
		return Principal{}, httpx.E(401, "UNAUTHENTICATED", "authentication required")
	}
	return Principal{UserID: u.UserID, Name: u.Name}, nil
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func (s *Service) CreateUser(ctx context.Context, userID, name, password string, maxConv int) error {
	if !usernamePattern.MatchString(userID) {
		return httpx.E(400, "BAD_USER", "invalid username")
	}
	if maxConv <= 0 {
		maxConv = 20
	}
	hash, err := s.HashPassword(password)
	if err != nil {
		return err
	}
	err = s.st.CreateUser(ctx, s.st.DB(), &store.User{UserID: userID, Name: name, PwdHash: hash, Status: 1, MaxConv: maxConv})
	if errors.Is(err, store.ErrDuplicate) {
		return httpx.E(409, "USER_EXISTS", "user already exists")
	}
	return err
}
func (s *Service) SetPassword(ctx context.Context, userID, password string) error {
	hash, err := s.HashPassword(password)
	if err != nil {
		return err
	}
	return s.st.SetUserPassword(ctx, userID, hash)
}
func (s *Service) Disable(ctx context.Context, userID string) error {
	return s.st.DisableUser(ctx, userID)
}
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
