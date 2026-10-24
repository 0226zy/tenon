// Package files provides owner-checked, bounded read-only workspace access.
package files

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/0226zy/proto-platform/internal/control/agentclient"
	"github.com/0226zy/proto-platform/internal/control/sandbox"
	"github.com/0226zy/proto-platform/internal/control/store"
	"github.com/0226zy/proto-platform/internal/kit/httpx"
	"github.com/0226zy/proto-platform/pkg/contract"
)

type Service struct {
	st  *store.Store
	sb  *sandbox.Manager
	ac  *agentclient.Client
	max int64
}

func New(st *store.Store, sb *sandbox.Manager, ac *agentclient.Client, max int64) *Service {
	return &Service{st: st, sb: sb, ac: ac, max: max}
}
func validate(path string) error {
	if len(path) > 1024 || strings.ContainsRune(path, '\x00') || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return httpx.E(400, "BAD_PATH", "invalid workspace path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return httpx.E(400, "BAD_PATH", "invalid workspace path")
		}
	}
	return nil
}
func (s *Service) handle(ctx context.Context, userID, convID string) (sandbox.Handle, error) {
	if _, err := s.st.ConversationOwned(ctx, s.st.DB(), userID, convID); err != nil {
		return sandbox.Handle{}, store.ErrNotFound
	}
	h, status, err := s.sb.Lookup(ctx, convID)
	if err != nil {
		return h, err
	}
	if status != 2 {
		s.sb.EnsureAsync(convID)
		e := httpx.E(503, "SANDBOX_STARTING", "sandbox is starting")
		e.Header = make(http.Header)
		e.Header.Set("Retry-After", "3")
		return h, e
	}
	s.sb.Touch(convID)
	return h, nil
}
func (s *Service) Tree(ctx context.Context, userID, convID, path string) ([]contract.FileEntry, error) {
	if err := validate(path); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx, userID, convID)
	if err != nil {
		return nil, err
	}
	entries, err := s.ac.Tree(ctx, h, path, 1)
	return entries, mapError(err)
}
func (s *Service) Content(ctx context.Context, userID, convID, path, ifNoneMatch string) (contract.FileContent, bool, error) {
	if path == "" {
		return contract.FileContent{}, false, httpx.E(400, "BAD_PATH", "file path is required")
	}
	if err := validate(path); err != nil {
		return contract.FileContent{}, false, err
	}
	h, err := s.handle(ctx, userID, convID)
	if err != nil {
		return contract.FileContent{}, false, err
	}
	v, err := s.ac.File(ctx, h, path, s.max)
	if err != nil {
		return v, false, mapError(err)
	}
	if ifNoneMatch != "" && ifNoneMatch == v.ETag {
		return v, true, nil
	}
	return v, false, nil
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		code := ae.Code
		if code == "" {
			code = "SANDBOX_BUSY"
		}
		status := ae.Status
		if status == 0 {
			status = 503
		}
		return httpx.E(status, code, "workspace request failed")
	}
	return err
}
