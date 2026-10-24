package preview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/0226zy/protoforge/internal/control/agentclient"
	"github.com/0226zy/protoforge/internal/control/sandbox"
	"github.com/0226zy/protoforge/internal/control/store"
	"github.com/0226zy/protoforge/internal/kit/config"
	"github.com/0226zy/protoforge/internal/kit/httpx"
	"github.com/0226zy/protoforge/pkg/contract"
)

type Service struct {
	st      *store.Store
	sb      *sandbox.Manager
	ac      *agentclient.Client
	cfg     *config.Config
	tickets *Tickets
}

func NewService(st *store.Store, sb *sandbox.Manager, ac *agentclient.Client, cfg *config.Config, tickets *Tickets) *Service {
	return &Service{st: st, sb: sb, ac: ac, cfg: cfg, tickets: tickets}
}
func (s *Service) Entry(ctx context.Context, userID, convID string) (contract.PreviewView, error) {
	c, err := s.st.ConversationOwned(ctx, s.st.DB(), userID, convID)
	if err != nil {
		return contract.PreviewView{}, store.ErrNotFound
	}
	h, status, err := s.sb.Lookup(ctx, convID)
	if err != nil {
		return contract.PreviewView{}, err
	}
	view := contract.PreviewView{Status: "starting"}
	if b, err := s.st.Sandbox(ctx, s.st.DB(), convID); err == nil {
		view.Status = store.PreviewStatusName(b.PreviewStatus)
		view.Generation = b.PreviewGeneration
		view.Commit = b.PreviewCommit
	} else if !errors.Is(err, store.ErrNotFound) {
		return view, err
	}

	// The last successful checkpoint, not the asynchronously updated sandbox
	// cache, is the desired preview version. Never ask wsd to build an older
	// generation while a newer post-Run build is still in progress.
	var expectedGeneration int64
	expectedCommit := ""
	latest, latestErr := s.st.LatestSuccessfulRun(ctx, s.st.DB(), convID)
	if latestErr == nil {
		expectedGeneration = latest.Generation
		expectedCommit = latest.Commit
	} else if !errors.Is(latestErr, store.ErrNotFound) {
		return view, latestErr
	}

	if status != 2 {
		s.sb.EnsureAsync(convID)
		view.Status = "starting"
	} else {
		st, err := s.ac.PreviewStatus(ctx, h)
		if err != nil {
			return view, mapError(err)
		}
		if st.Generation < expectedGeneration || (st.Generation == expectedGeneration && st.Commit == "" && expectedCommit != "") {
			st, err = s.ac.PreviewEnsure(ctx, h, contract.PreviewEnsureRequest{Generation: expectedGeneration, Commit: expectedCommit})
			if err != nil {
				return view, mapError(err)
			}
		}
		if err := s.sb.UpdatePreview(ctx, convID, st); err != nil {
			return view, err
		}
		view.Status = st.Status
		view.Commit = st.Commit
		view.Generation = st.Generation
	}
	view.Stale = expectedGeneration > view.Generation
	if r, err := s.st.ActiveRun(ctx, s.st.DB(), convID); err == nil {
		view.Stale = view.Stale || r.Generation > view.Generation
	} else if !errors.Is(err, store.ErrNotFound) {
		return view, err
	}
	if view.Status != "ready" {
		view.URL = ""
		return view, nil
	}
	host := s.publicHost(c.PreviewKey)
	ticket, err := s.tickets.Issue(convID, host)
	if err != nil {
		return view, err
	}
	scheme := s.cfg.Preview.Scheme
	if scheme == "" {
		scheme = "https"
	}
	view.URL = fmt.Sprintf("%s://%s/__enter?t=%s", scheme, host, ticket)
	return view, nil
}
func (s *Service) publicHost(key string) string {
	if s.cfg.Preview.PublicAddress != "" {
		return strings.ReplaceAll(s.cfg.Preview.PublicAddress, "{key}", key)
	}
	return key + ".preview." + s.cfg.Preview.Domain
}
func (s *Service) Restart(ctx context.Context, userID, convID string) error {
	if _, err := s.st.ConversationOwned(ctx, s.st.DB(), userID, convID); err != nil {
		return store.ErrNotFound
	}
	if _, err := s.st.ActiveRun(ctx, s.st.DB(), convID); err == nil {
		return httpx.E(409, "RUN_IN_PROGRESS", "cannot restart preview during a Run")
	}
	h, status, err := s.sb.Lookup(ctx, convID)
	if err != nil {
		return err
	}
	if status != 2 {
		s.sb.EnsureAsync(convID)
		return httpx.E(503, "SANDBOX_STARTING", "sandbox is starting")
	}
	return mapError(s.ac.PreviewRestart(ctx, h))
}
func (s *Service) Logs(ctx context.Context, userID, convID string, tail int) ([]byte, error) {
	if _, err := s.st.ConversationOwned(ctx, s.st.DB(), userID, convID); err != nil {
		return nil, store.ErrNotFound
	}
	h, status, err := s.sb.Lookup(ctx, convID)
	if err != nil {
		return nil, err
	}
	if status != 2 {
		s.sb.EnsureAsync(convID)
		return nil, httpx.E(503, "SANDBOX_STARTING", "sandbox is starting")
	}
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	return s.ac.PreviewLogs(ctx, h, tail)
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var ae *agentclient.Error
	if errors.As(err, &ae) {
		status := ae.Status
		if status == 0 {
			status = 503
		}
		code := ae.Code
		if code == "" {
			code = "SANDBOX_BUSY"
		}
		return httpx.E(status, code, "preview request failed")
	}
	return err
}
