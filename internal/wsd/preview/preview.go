// Package preview supervises the live workspace preview process.
package preview

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/wsd/bus"
	"github.com/0226zy/protoforge/internal/wsd/cpclient"
	"github.com/0226zy/protoforge/internal/wsd/disk"
	"github.com/0226zy/protoforge/internal/wsd/gate"
	procregistry "github.com/0226zy/protoforge/internal/wsd/proc"
	"github.com/0226zy/protoforge/pkg/contract"
)

var ErrNoPreview = errors.New("no preview has been requested")

type EnsureReq struct {
	Generation int64
	Commit     string
	Wait       time.Duration
	force      bool
}

type Config struct {
	AppRoot       string
	DataDir       string
	Mode          string
	BuildTimeout  time.Duration
	MaxRestarts   int
	LogLines      int
	Local         bool
	Port          int
	Env           map[string]string
	DiskUsage     func() (disk.Usage, error)
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	StopGrace     time.Duration
}

type child struct {
	cmd  *exec.Cmd
	pgid int
	done chan error
}

type build struct {
	generation int64
	done       chan struct{}
}

type Supervisor struct {
	cfg      Config
	gate     *gate.Gate
	registry *procregistry.Registry
	cp       *cpclient.Client
	bus      *bus.Bus
	logs     *logRing
	probe    *http.Client

	mu       sync.Mutex
	state    contract.PreviewState
	current  *build
	process  *child
	stopping bool

	lifeCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func New(cfg Config, g *gate.Gate, p *procregistry.Registry, cp *cpclient.Client, b *bus.Bus) *Supervisor {
	if cfg.BuildTimeout <= 0 {
		cfg.BuildTimeout = 90 * time.Second
	}
	if cfg.MaxRestarts < 0 {
		cfg.MaxRestarts = 0
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = 3
	}
	if cfg.Mode == "" {
		cfg.Mode = "live"
	}
	if !cfg.Local && cfg.Port == 0 {
		cfg.Port = 3000
	}
	if cfg.ProbeInterval <= 0 {
		cfg.ProbeInterval = time.Second
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 2 * time.Second
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = 5 * time.Second
	}
	lifeCtx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		cfg: cfg, gate: g, registry: p, cp: cp, bus: b, logs: newLogRing(cfg.LogLines),
		lifeCtx: lifeCtx, cancel: cancel,
		probe: &http.Client{
			Timeout:       cfg.ProbeTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	s.state = contract.PreviewState{Status: "none", URLPath: "/"}
	return s
}

func (s *Supervisor) Ensure(ctx context.Context, req EnsureReq, runID string) (contract.PreviewState, error) {
	if req.Generation < 0 {
		return s.Status(), gate.ErrStaleGeneration
	}
	// Runtime preview calls omit generation because the control plane allocates it
	// when the Run starts. Inherit only from the gate held by this same Run; an
	// explicit older generation must remain stale, and a preview outside a Run
	// must never borrow another Run's generation.
	if req.Generation == 0 && runID != "" && s.gate != nil {
		state := s.gate.State()
		if state.Held && state.Holder == "run" && state.RunID == runID {
			req.Generation = state.Generation
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	stopLifeWatch := context.AfterFunc(s.lifeCtx, cancel)
	defer func() {
		stopLifeWatch()
		cancel()
	}()
	for {
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return s.Status(), ErrNoPreview
		}
		if req.Generation < s.state.Generation || (s.current != nil && req.Generation < s.current.generation) {
			s.mu.Unlock()
			return s.Status(), gate.ErrStaleGeneration
		}
		if !req.force && req.Generation == s.state.Generation && s.state.Status == "ready" {
			// A Runtime preview call runs before checkpoint and therefore has no
			// commit. The post-Run ensure backfills that commit without rebuilding
			// the already-correct workspace or relabelling a non-empty commit.
			backfilled := s.state.Commit == "" && req.Commit != ""
			if backfilled {
				s.state.Commit = req.Commit
			}
			state := cloneState(s.state)
			s.mu.Unlock()
			if backfilled {
				s.publish(state)
			}
			return state, nil
		}
		if s.current != nil {
			current := s.current
			if req.Generation == current.generation && !req.force {
				s.mu.Unlock()
				select {
				case <-ctx.Done():
					return s.Status(), ctx.Err()
				case <-current.done:
				}
				// Failed builds return directly. Ready builds loop through the
				// idempotent path above so a waiting post-Run call can backfill commit.
				state := s.Status()
				if state.Generation == req.Generation && state.Status == "failed" {
					return state, nil
				}
				continue
			}
			// A larger generation, or an explicit restart, queues behind the
			// current build rather than interrupting it.
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return s.Status(), ctx.Err()
			case <-current.done:
				continue
			}
		}
		current := &build{generation: req.Generation, done: make(chan struct{})}
		s.current = current
		s.mu.Unlock()

		state, err := s.runBuild(ctx, req, runID)
		s.mu.Lock()
		s.current = nil
		close(current.done)
		s.mu.Unlock()
		return state, err
	}
}

// Restart schedules a forced rebuild at the current generation and returns
// immediately. The goroutine is bound to the supervisor lifecycle.
func (s *Supervisor) Restart(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	if s.stopping || s.state.Generation == 0 && s.state.Status == "none" {
		s.mu.Unlock()
		return ErrNoPreview
	}
	req := EnsureReq{Generation: s.state.Generation, Commit: s.state.Commit, Wait: s.cfg.BuildTimeout, force: true}
	// Add while holding mu so Stop cannot begin Wait concurrently with Add.
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		_, _ = s.Ensure(s.lifeCtx, req, "")
	}()
	return nil
}

func (s *Supervisor) Status() contract.PreviewState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state)
}

func cloneState(in contract.PreviewState) contract.PreviewState { return in }

func (s *Supervisor) Logs(tail int) []byte { return s.logs.Tail(tail) }

func (s *Supervisor) runBuild(ctx context.Context, req EnsureReq, runID string) (contract.PreviewState, error) {
	if s.cfg.Mode != "live" {
		return s.fail(req, "BUILD_FAILED", "only live preview mode is supported"), nil
	}
	if req.Wait > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Wait)
		defer cancel()
	}
	restarts := 0
	s.mu.Lock()
	if req.Generation == s.state.Generation {
		restarts = s.state.Restarts
	}
	s.state = contract.PreviewState{Status: "starting", Generation: req.Generation, Commit: req.Commit, URLPath: "/", Restarts: restarts}
	state := cloneState(s.state)
	s.mu.Unlock()
	s.publish(state)

	if s.cfg.DiskUsage != nil {
		usage, err := s.cfg.DiskUsage()
		if err != nil {
			return s.fail(req, "BUILD_FAILED", "could not measure workspace disk usage"), nil
		}
		if usage.AboveHighWatermark() {
			return s.fail(req, "DISK_FULL", "workspace disk high watermark reached"), nil
		}
	}

	releaseGate := func() {}
	if s.gate == nil {
		return s.fail(req, "BUILD_FAILED", "workspace gate is unavailable"), nil
	}
	if runID != "" && s.gate.HeldBy(runID) {
		if _, err := s.gate.AcquireRun(ctx, runID, req.Generation); err != nil {
			return s.Status(), err
		}
	} else {
		release, err := s.gate.AcquirePreview(ctx, req.Generation)
		if err != nil {
			return s.Status(), err
		}
		releaseGate = release
	}
	defer releaseGate()

	slotID, err := s.acquireSlot(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return s.fail(req, "BUILD_SLOT_TIMEOUT", "timed out waiting for a build slot"), nil
		}
		return s.fail(req, "BUILD_FAILED", "could not acquire a build slot"), nil
	}
	defer s.releaseSlot(slotID)

	if err := s.stopCurrent(); err != nil {
		return s.fail(req, "BUILD_FAILED", "could not stop the previous preview"), nil
	}
	port, err := s.reservePort()
	if err != nil {
		return s.fail(req, "PORT_CONFLICT", "preview port is already in use"), nil
	}

	child, err := s.startChild(port)
	if err != nil {
		return s.fail(req, "BUILD_FAILED", "could not start start.sh"), nil
	}
	state, ready := s.probeUntilReady(ctx, child, req, port)
	if !ready {
		s.stopChild(child)
		if state.ErrorCode == "" {
			state.ErrorCode = "PREVIEW_TIMEOUT"
		}
		state.LogTail = string(s.Logs(20))
		s.setState(state)
		return state, nil
	}
	return state, nil
}

func (s *Supervisor) acquireSlot(ctx context.Context) (string, error) {
	if s.cp == nil {
		return "", errors.New("control plane client is nil")
	}
	for {
		slotID, retryAfter, err := s.cp.AcquireBuild(ctx, "preview")
		if err == nil {
			if slotID == "" {
				return "", errors.New("control plane returned an empty build slot")
			}
			return slotID, nil
		}
		if !errors.Is(err, cpclient.ErrBuildBusy) {
			return "", err
		}
		if retryAfter <= 0 {
			retryAfter = 100 * time.Millisecond
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Supervisor) releaseSlot(slotID string) {
	if slotID == "" || s.cp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.cp.ReleaseBuild(ctx, slotID); err != nil {
		_, _ = s.logs.Write([]byte("build slot release failed\n"))
	}
}

func (s *Supervisor) reservePort() (int, error) {
	if s.cfg.Local && s.cfg.Port == 0 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			return 0, err
		}
		return port, nil
	}
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(s.cfg.Port))
	if err != nil {
		return 0, err
	}
	return s.cfg.Port, listener.Close()
}

var previewEnvAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "LANG": true, "TERM": true,
	"GOPROXY": true, "GOFLAGS": true, "GOMODCACHE": true, "GOWORK": true,
	"npm_config_registry": true, "npm_config_cache": true,
}

func (s *Supervisor) startChild(port int) (*child, error) {
	cmd := exec.Command("bash", filepath.Join(s.cfg.AppRoot, "start.sh"))
	cmd.Dir = s.cfg.AppRoot
	env := make([]string, 0, len(s.cfg.Env)+2)
	for key, value := range s.cfg.Env {
		if previewEnvAllowlist[key] && !strings.ContainsRune(key, '\x00') && !strings.ContainsRune(value, '\x00') {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "PORT="+strconv.Itoa(port), "DATA_DIR="+s.cfg.DataDir)
	cmd.Env = env
	cmd.Stdout = s.logs
	cmd.Stderr = s.logs
	pgid, err := procregistry.StartProcess(cmd)
	if err != nil {
		return nil, err
	}
	c := &child{cmd: cmd, pgid: pgid, done: make(chan error, 1)}
	go func() { c.done <- cmd.Wait(); close(c.done) }()
	s.mu.Lock()
	s.process = c
	s.mu.Unlock()
	s.registry.Own("preview", pgid)
	go s.monitor(c)
	return c, nil
}

func (s *Supervisor) probeUntilReady(ctx context.Context, c *child, req EnsureReq, port int) (contract.PreviewState, bool) {
	buildCtx, cancel := context.WithTimeout(ctx, s.cfg.BuildTimeout)
	defer cancel()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	ticker := time.NewTicker(s.cfg.ProbeInterval)
	defer ticker.Stop()
	state := s.Status()
	state.LocalPort = 0
	if s.cfg.Local {
		state.LocalPort = port
	}
	for {
		started := time.Now()
		code := s.probeOnce(buildCtx, url)
		state.Probe.Code = code
		state.Probe.MS = time.Since(started).Milliseconds()
		if code == http.StatusOK {
			// Serialize the ready transition with the process-exit monitor. If
			// Wait won the race, do not publish a false ready state.
			s.mu.Lock()
			select {
			case <-c.done:
				s.mu.Unlock()
				state.Status = "failed"
				state.ErrorCode = "BUILD_FAILED"
				return state, false
			default:
			}
			state.Status = "ready"
			state.ErrorCode = ""
			state.LogTail = string(s.Logs(20))
			s.state = cloneState(state)
			s.mu.Unlock()
			s.publish(state)
			return state, true
		}
		select {
		case <-buildCtx.Done():
			state.Status = "failed"
			state.ErrorCode = "PREVIEW_TIMEOUT"
			return state, false
		case <-c.done:
			state.Status = "failed"
			state.ErrorCode = "BUILD_FAILED"
			return state, false
		case <-ticker.C:
		}
	}
}

func (s *Supervisor) probeOnce(ctx context.Context, url string) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	resp, err := s.probe.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	return resp.StatusCode
}

func (s *Supervisor) monitor(c *child) {
	<-c.done
	s.mu.Lock()
	if s.stopping || s.process != c || s.state.Status != "ready" {
		s.mu.Unlock()
		return
	}
	if s.state.Restarts >= s.cfg.MaxRestarts {
		s.state.Status = "failed"
		s.state.ErrorCode = "BUILD_FAILED"
		s.state.LogTail = string(s.Logs(20))
		s.process = nil
		state := cloneState(s.state)
		s.mu.Unlock()
		s.registry.Disown("preview")
		s.publish(state)
		return
	}
	s.state.Restarts++
	s.state.Status = "starting"
	req := EnsureReq{Generation: s.state.Generation, Commit: s.state.Commit, Wait: s.cfg.BuildTimeout, force: true}
	state := cloneState(s.state)
	// Serialize Add with Stop's transition to stopping.
	s.wg.Add(1)
	s.mu.Unlock()
	s.publish(state)
	go func() {
		defer s.wg.Done()
		_, _ = s.Ensure(s.lifeCtx, req, "")
	}()
}

func (s *Supervisor) fail(req EnsureReq, code, log string) contract.PreviewState {
	_, _ = s.logs.Write([]byte(log + "\n"))
	state := s.Status()
	state.Status = "failed"
	state.Generation = req.Generation
	state.Commit = req.Commit
	state.URLPath = "/"
	state.ErrorCode = code
	state.LogTail = string(s.Logs(20))
	s.setState(state)
	return state
}

func (s *Supervisor) setState(state contract.PreviewState) {
	s.mu.Lock()
	s.state = cloneState(state)
	s.mu.Unlock()
	s.publish(state)
}

func (s *Supervisor) publish(state contract.PreviewState) {
	if s.bus == nil {
		return
	}
	s.bus.Publish("preview_status", map[string]any{
		"status": state.Status, "generation": state.Generation, "commit": state.Commit,
		"restarts": state.Restarts, "error_code": state.ErrorCode,
	})
}

func (s *Supervisor) stopCurrent() error {
	s.mu.Lock()
	c := s.process
	s.mu.Unlock()
	if c == nil {
		return nil
	}
	return s.stopChild(c)
}

func (s *Supervisor) stopChild(c *child) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StopGrace+4*time.Second)
	defer cancel()
	err := procregistry.StopGroup(ctx, c.pgid, s.cfg.StopGrace)
	select {
	case <-c.done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	s.mu.Lock()
	if s.process == c {
		s.process = nil
	}
	s.mu.Unlock()
	s.registry.Disown("preview")
	return err
}

func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	current := s.current
	s.mu.Unlock()
	s.cancel()
	if current != nil {
		select {
		case <-current.done:
		case <-ctx.Done():
		}
	}
	err := s.stopCurrent()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	s.mu.Lock()
	s.state = contract.PreviewState{Status: "none", URLPath: "/"}
	s.mu.Unlock()
	return err
}
