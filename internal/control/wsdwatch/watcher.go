// Package wsdwatch forwards best-effort wsd notifications without persisting deltas.
package wsdwatch

import (
	"context"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/control/agentclient"
	"github.com/0226zy/protoforge/internal/control/event"
	"github.com/0226zy/protoforge/internal/control/sandbox"
	"github.com/0226zy/protoforge/pkg/contract"
)

type PreviewUpdater interface {
	UpdatePreview(context.Context, string, contract.PreviewState) error
}

const (
	defaultInitialBackoff = time.Second
	defaultMaxBackoff     = 30 * time.Second
)

type Watcher struct {
	ac             *agentclient.Client
	hub            *event.Hub
	mapper         event.Mapper
	updater        PreviewUpdater
	initialBackoff time.Duration
	maxBackoff     time.Duration
	mu             sync.Mutex
	cancel         map[string]context.CancelFunc
	building       map[string]bool
}

func New(ac *agentclient.Client, hub *event.Hub, mapper event.Mapper, updater PreviewUpdater) *Watcher {
	return &Watcher{
		ac:             ac,
		hub:            hub,
		mapper:         mapper,
		updater:        updater,
		initialBackoff: defaultInitialBackoff,
		maxBackoff:     defaultMaxBackoff,
		cancel:         map[string]context.CancelFunc{},
		building:       map[string]bool{},
	}
}
func (w *Watcher) Attach(h sandbox.Handle) {
	w.mu.Lock()
	if _, ok := w.cancel[h.ConvID]; ok {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel[h.ConvID] = cancel
	w.mu.Unlock()
	go w.loop(ctx, h)
}
func (w *Watcher) Detach(convID string) {
	w.mu.Lock()
	if cancel := w.cancel[convID]; cancel != nil {
		cancel()
	}
	delete(w.cancel, convID)
	delete(w.building, convID)
	w.mu.Unlock()
}
func (w *Watcher) Building(convID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.building[convID]
}
func (w *Watcher) Close() {
	w.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(w.cancel))
	for convID, cancel := range w.cancel {
		cancels = append(cancels, cancel)
		delete(w.cancel, convID)
		delete(w.building, convID)
	}
	w.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
func (w *Watcher) loop(ctx context.Context, h sandbox.Handle) {
	defer w.Detach(h.ConvID)
	backoff := w.initialBackoff
	for {
		stream, err := w.ac.WSDEvents(ctx, h)
		if err == nil {
			for {
				ev, err := stream.NextWSD()
				if err != nil {
					_ = stream.Close()
					break
				}
				backoff = w.initialBackoff
				switch ev.Type {
				case "file_changed":
					if f, ok := w.mapper.TempToUI("", "files_changed", ev.Paths); ok {
						w.hub.PublishTemp(h.ConvID, f)
					}
				case "preview_status":
					st := ev.Preview
					if st.Status == "" {
						st.Status = ev.Status
						st.Generation = ev.Generation
						st.Commit = ev.Commit
					}
					w.mu.Lock()
					w.building[h.ConvID] = st.Status == "starting"
					w.mu.Unlock()
					_ = w.updater.UpdatePreview(ctx, h.ConvID, st)
					if f, ok := w.mapper.TempToUI("", "preview_status", st); ok {
						w.hub.PublishTemp(h.ConvID, f)
					}
				}
			}
		}
		if !waitForBackoff(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, w.maxBackoff)
	}
}

func waitForBackoff(ctx context.Context, backoff time.Duration) bool {
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current, max time.Duration) time.Duration {
	if current >= max {
		return max
	}
	next := current * 2
	if next > max {
		return max
	}
	return next
}
