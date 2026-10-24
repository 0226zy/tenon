// Package gate serializes access to a live workspace.
package gate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/pkg/contract"
)

var (
	ErrBusy            = errors.New("workspace gate is busy")
	ErrStaleGeneration = errors.New("stale generation")
	ErrNotHolder       = errors.New("run does not hold the workspace gate")
)

// Gate is an in-memory, generation-fenced workspace mutex. A zero Gate is ready
// for use. Waiting never holds the mutex and is cancelled by the caller context.
type Gate struct {
	mu      sync.Mutex
	changed chan struct{}
	held    bool
	holder  string
	runID   string
	gen     int64
	maxGen  int64
	since   time.Time
}

func (g *Gate) initLocked() {
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
}

func (g *Gate) signalLocked() {
	g.initLocked()
	close(g.changed)
	g.changed = make(chan struct{})
}

// AcquireRun grants the run gate. Re-entry by the same run is idempotent.
func (g *Gate) AcquireRun(ctx context.Context, runID string, gen int64) (contract.GateGrant, error) {
	if runID == "" {
		return contract.GateGrant{}, ErrNotHolder
	}
	return g.acquire(ctx, "run", runID, gen)
}

// AcquirePreview grants the live-preview gate. The returned release function is
// safe to call more than once.
func (g *Gate) AcquirePreview(ctx context.Context, gen int64) (func(), error) {
	if _, err := g.acquire(ctx, "preview", "", gen); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.held && g.holder == "preview" && g.gen == gen {
				g.releaseLocked()
			}
			g.mu.Unlock()
		})
	}, nil
}

func (g *Gate) acquire(ctx context.Context, holder, runID string, gen int64) (contract.GateGrant, error) {
	for {
		g.mu.Lock()
		g.initLocked()
		if gen < g.maxGen {
			g.mu.Unlock()
			return contract.GateGrant{}, ErrStaleGeneration
		}
		if g.held && holder == "run" && g.holder == "run" && g.runID == runID {
			// Re-entry may advance a run's generation, but never moves it back.
			if gen > g.gen {
				g.gen = gen
			}
			if gen > g.maxGen {
				g.maxGen = gen
			}
			grant := g.grantLocked()
			g.mu.Unlock()
			return grant, nil
		}
		if !g.held {
			g.held = true
			g.holder = holder
			g.runID = runID
			g.gen = gen
			g.maxGen = gen
			g.since = time.Now().UTC()
			grant := g.grantLocked()
			g.mu.Unlock()
			return grant, nil
		}
		changed := g.changed
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			return contract.GateGrant{}, ErrBusy
		case <-changed:
		}
	}
}

func (g *Gate) grantLocked() contract.GateGrant {
	return contract.GateGrant{Holder: g.holder, RunID: g.runID, Generation: g.gen, Since: g.since}
}

// ReleaseRun releases only the matching run. Releasing an already idle gate is
// deliberately idempotent; releasing somebody else's gate is not.
func (g *Gate) ReleaseRun(runID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return nil
	}
	if g.holder != "run" || runID == "" || g.runID != runID {
		return ErrNotHolder
	}
	g.releaseLocked()
	return nil
}

func (g *Gate) releaseLocked() {
	g.held = false
	g.holder = ""
	g.runID = ""
	g.gen = 0
	g.since = time.Time{}
	g.signalLocked()
}

func (g *Gate) HeldBy(runID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held && g.holder == "run" && runID != "" && g.runID == runID
}

// HeldByOrIdle is the precondition for orphan reaping.
func (g *Gate) HeldByOrIdle(runID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.held || (g.holder == "run" && runID != "" && g.runID == runID)
}

func (g *Gate) State() contract.GateState {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := contract.GateState{
		Held: g.held, Holder: g.holder, RunID: g.runID,
		Generation: g.gen, MaxGeneration: g.maxGen,
	}
	if g.held {
		since := g.since
		state.Since = &since
	}
	return state
}
