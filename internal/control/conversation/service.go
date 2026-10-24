// Package conversation implements owner-scoped conversation and message resources.
package conversation

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0226zy/proto-platform/internal/control/event"
	"github.com/0226zy/proto-platform/internal/control/store"
	"github.com/0226zy/proto-platform/internal/kit/httpx"
	"github.com/0226zy/proto-platform/internal/kit/idgen"
	"github.com/0226zy/proto-platform/pkg/contract"
)

type Catalog interface {
	Default() string
	Validate(string) error
}
type ActiveRunCanceller interface {
	CancelActive(context.Context, string, string) error
}
type SandboxRemover interface {
	Remove(context.Context, string) error
}
type Service struct {
	st      *store.Store
	ev      *event.Store
	ids     idgen.IDGen
	catalog Catalog
	cancel  ActiveRunCanceller
	remove  SandboxRemover
}

func New(st *store.Store, ev *event.Store, ids idgen.IDGen, cat Catalog) *Service {
	return &Service{st: st, ev: ev, ids: ids, catalog: cat}
}
func (s *Service) SetHooks(cancel ActiveRunCanceller, remove SandboxRemover) {
	s.cancel = cancel
	s.remove = remove
}
func (s *Service) Create(ctx context.Context, userID string, req contract.CreateConversationRequest) (contract.ConversationView, error) {
	title := strings.TrimSpace(req.Title)
	if utf8.RuneCountInString(title) > 128 {
		return contract.ConversationView{}, httpx.E(400, "BAD_TITLE", "title is too long")
	}
	model := req.Model
	if model == "" {
		model = s.catalog.Default()
	}
	if err := s.catalog.Validate(model); err != nil {
		return contract.ConversationView{}, httpx.E(400, "BAD_MODEL", "unknown model")
	}
	template := req.Template
	if template == "" {
		template = "react-go-sqlite"
	}
	if template != "react-go-sqlite" {
		return contract.ConversationView{}, httpx.E(400, "BAD_TEMPLATE", "unknown template")
	}
	for i := 0; i < 4; i++ {
		c := &store.Conversation{ConvID: s.ids.ConvID(), UserID: userID, Title: title, Model: model, Template: template, PreviewKey: s.ids.PreviewKey()}
		err := s.st.WithTx(ctx, func(tx *store.Tx) error {
			u, err := s.st.LockUser(ctx, tx, userID)
			if err != nil {
				return err
			}
			n, err := s.st.CountConversations(ctx, tx, userID)
			if err != nil {
				return err
			}
			if n >= u.MaxConv {
				return httpx.E(429, "CONV_LIMIT", "conversation limit reached")
			}
			return s.st.CreateConversation(ctx, tx, c)
		})
		if err == nil {
			return s.view(ctx, c), nil
		}
		if !errors.Is(err, store.ErrDuplicate) {
			return contract.ConversationView{}, err
		}
	}
	return contract.ConversationView{}, store.ErrDuplicate
}
func (s *Service) List(ctx context.Context, userID, cursor string, limit int) (contract.Page[contract.ConversationView], error) {
	items, next, err := s.st.ListConversations(ctx, userID, cursor, limit)
	if err != nil {
		return contract.Page[contract.ConversationView]{}, httpx.E(400, "BAD_CURSOR", "invalid cursor")
	}
	out := contract.Page[contract.ConversationView]{Items: []contract.ConversationView{}, NextCursor: next}
	for i := range items {
		out.Items = append(out.Items, s.view(ctx, &items[i]))
	}
	return out, nil
}
func (s *Service) GetOwned(ctx context.Context, userID, convID string) (*store.Conversation, error) {
	return s.st.ConversationOwned(ctx, s.st.DB(), userID, convID)
}
func (s *Service) Get(ctx context.Context, userID, convID string) (contract.ConversationDetail, error) {
	c, err := s.GetOwned(ctx, userID, convID)
	if err != nil {
		return contract.ConversationDetail{}, err
	}
	base := s.view(ctx, c)
	d := contract.ConversationDetail{ConversationView: base, LastSeq: c.LastSeq}
	d.Sandbox.Status = "absent"
	b, err := s.st.Sandbox(ctx, s.st.DB(), convID)
	if err == nil {
		d.Sandbox.Status = store.SandboxStatusName(b.Status)
		d.Preview = contract.PreviewBrief{Status: store.PreviewStatusName(b.PreviewStatus), Commit: b.PreviewCommit, Generation: b.PreviewGeneration}
	} else if !errors.Is(err, store.ErrNotFound) {
		return d, err
	}
	if d.Preview.Status == "" {
		d.Preview.Status = "none"
	}
	if r, err := s.st.ActiveRun(ctx, s.st.DB(), convID); err == nil {
		d.ActiveRun = &contract.RunBrief{RunID: r.RunID, Status: r.Status}
	}
	return d, nil
}
func (s *Service) Update(ctx context.Context, userID, convID string, req contract.UpdateConversationRequest) (contract.ConversationView, error) {
	var updated *store.Conversation
	err := s.st.WithTx(ctx, func(tx *store.Tx) error {
		c, err := s.st.LockConversation(ctx, tx, userID, convID)
		if err != nil {
			return err
		}
		if req.Title != nil {
			title := strings.TrimSpace(*req.Title)
			if title == "" || utf8.RuneCountInString(title) > 128 {
				return httpx.E(400, "BAD_TITLE", "invalid title")
			}
			req.Title = &title
		}
		if req.Model != nil && *req.Model != c.Model {
			if err := s.catalog.Validate(*req.Model); err != nil {
				return httpx.E(400, "BAD_MODEL", "unknown model")
			}
			if _, err := s.st.ActiveRun(ctx, tx, convID); err == nil {
				return httpx.E(409, "RUN_IN_PROGRESS", "cannot change model during an active Run")
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if _, err := s.ev.AppendTx(ctx, tx, event.Control(convID, "", "model_changed", map[string]any{"from": c.Model, "to": *req.Model})); err != nil {
				return err
			}
		}
		if err := s.st.UpdateConversation(ctx, tx, convID, req.Title, req.Model, nil); err != nil {
			return err
		}
		updated, err = s.st.ConversationOwned(ctx, tx, userID, convID)
		return err
	})
	if err != nil {
		return contract.ConversationView{}, err
	}
	return s.view(ctx, updated), nil
}
func (s *Service) Delete(ctx context.Context, userID, convID string) error {
	if _, err := s.GetOwned(ctx, userID, convID); err != nil {
		return err
	}
	if s.cancel != nil {
		if err := s.cancel.CancelActive(ctx, convID, "conversation deleted"); err != nil {
			return err
		}
	}
	if err := s.st.WithTx(ctx, func(tx *store.Tx) error { return s.st.DeleteConversation(ctx, tx, convID) }); err != nil {
		return err
	}
	if s.remove != nil {
		return s.remove.Remove(ctx, convID)
	}
	return nil
}
func (s *Service) Messages(ctx context.Context, userID, convID, before string, limit int) (contract.Page[contract.MessageView], error) {
	if _, err := s.GetOwned(ctx, userID, convID); err != nil {
		return contract.Page[contract.MessageView]{}, err
	}
	items, next, err := s.st.ListMessages(ctx, convID, before, limit)
	if err != nil {
		return contract.Page[contract.MessageView]{}, httpx.E(400, "BAD_CURSOR", "invalid cursor")
	}
	out := contract.Page[contract.MessageView]{Items: []contract.MessageView{}, NextCursor: next}
	for _, m := range items {
		role := "user"
		if m.Role == 2 {
			role = "assistant"
		}
		out.Items = append(out.Items, contract.MessageView{ID: m.MsgID, Role: role, Content: m.Content, RunID: m.RunID, CreatedAt: m.CreatedAt.Format(time.RFC3339Nano)})
	}
	return out, nil
}
func (s *Service) view(ctx context.Context, c *store.Conversation) contract.ConversationView {
	v := contract.ConversationView{ID: c.ConvID, Title: c.Title, Model: c.Model, Status: "idle", LastActive: c.LastActiveAt.Format(time.RFC3339Nano), CreatedAt: c.CreatedAt.Format(time.RFC3339Nano)}
	if r, err := s.st.ActiveRun(ctx, s.st.DB(), c.ConvID); err == nil {
		v.Run = &contract.RunBrief{RunID: r.RunID, Status: r.Status}
		if r.Status == contract.RunQueued {
			v.Status = "queued"
		} else {
			v.Status = "running"
		}
	}
	if b, err := s.st.Sandbox(ctx, s.st.DB(), c.ConvID); err == nil {
		v.Preview = &contract.PreviewBrief{Status: store.PreviewStatusName(b.PreviewStatus), Commit: b.PreviewCommit, Generation: b.PreviewGeneration}
		if v.Run == nil && b.Status == 4 {
			v.Status = "error"
		}
	}
	return v
}
