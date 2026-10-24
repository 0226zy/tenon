// Package app wires the control plane without hiding lifecycle or driver choices.
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/api"
	"github.com/0226zy/tenon/internal/control/auth"
	"github.com/0226zy/tenon/internal/control/consumer"
	"github.com/0226zy/tenon/internal/control/conversation"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/files"
	"github.com/0226zy/tenon/internal/control/internalapi"
	"github.com/0226zy/tenon/internal/control/llm"
	"github.com/0226zy/tenon/internal/control/preview"
	"github.com/0226zy/tenon/internal/control/quota"
	"github.com/0226zy/tenon/internal/control/run"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/control/store"
	"github.com/0226zy/tenon/internal/control/wsdwatch"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/internal/kit/httpx"
	"github.com/0226zy/tenon/internal/kit/idgen"
	"github.com/0226zy/tenon/internal/kit/metrics"
	"github.com/0226zy/tenon/internal/kit/secret"
)

type App struct {
	Config          *config.Config
	Store           *store.Store
	Auth            *auth.Service
	Catalog         *llm.Catalog
	Quota           *quota.Manager
	Sandbox         *sandbox.Manager
	Hub             *event.Hub
	Events          *event.Store
	Agent           *agentclient.Client
	Consumer        *consumer.Consumer
	Usage           *llm.UsageService
	Limiter         *llm.Limiter
	LLMProxy        *llm.Proxy
	Runs            *run.Service
	Conversations   *conversation.Service
	Files           *files.Service
	Preview         *preview.Service
	Tickets         *preview.Tickets
	Watcher         *wsdwatch.Watcher
	Registry        *metrics.Registry
	APIHandler      http.Handler
	PreviewHandler  http.Handler
	InternalHandler http.Handler
	MetricsHandler  http.Handler

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, cfg *config.Config) (_ *App, err error) {
	dialect, dsn := "mysql", cfg.MySQL.DSN
	if cfg.Mode == "local" {
		dialect, dsn = "sqlite", cfg.SQLite.Path
	}
	st, err := store.Open(ctx, dialect, dsn)
	if err != nil {
		return nil, fmt.Errorf("open control store: %w", err)
	}
	if dialect == "mysql" {
		st.DB().SetMaxOpenConns(cfg.MySQL.MaxOpen)
		st.DB().SetMaxIdleConns(cfg.MySQL.MaxIdle)
	}
	ok := false
	defer func() {
		if !ok {
			_ = st.Close()
		}
	}()
	master, err := secret.LoadMaster(cfg.Control.SecretFile, cfg.Mode == "local")
	if err != nil {
		return nil, err
	}
	catalog, err := llm.LoadCatalog(cfg.Catalog.File, cfg.Mode == "local")
	if err != nil {
		return nil, fmt.Errorf("load model catalog: %w", err)
	}
	ids := idgen.New()
	quotas := quota.New(cfg.Quota)
	var driver sandbox.Driver
	switch cfg.Sandbox.Driver {
	case "local":
		if cfg.Mode != "local" {
			return nil, fmt.Errorf("local sandbox driver requires local mode")
		}
		driver = sandbox.NewLocalDriver(cfg.Local.Root, cfg.Local.WSDBin, cfg.Local.TemplateDir, cfg.Local.AgentRuntimeBin, cfg.Local.GoProxy)
	case "docker":
		// The production driver is deliberately fail-closed until its isolation and
		// resource-limit implementation is available.
		driver = sandbox.DockerUnavailable{}
	default:
		return nil, fmt.Errorf("unknown sandbox driver %q", cfg.Sandbox.Driver)
	}
	defer func() {
		if !ok {
			_ = driver.Close()
		}
	}()
	sandboxes := sandbox.NewManager(st, driver, quotas, cfg, ids, master)
	hub := event.NewHub()
	mapper := event.Mapper{ToolArgsMax: cfg.Event.ToolArgsMax, ToolResultMax: cfg.Event.ToolResultMax}
	events := event.NewStore(st, hub)
	agent := agentclient.New(cfg.AgentClient)
	eventsConsumer := consumer.New(agent, st, events, hub, mapper, cfg.Run.ReconcileInterval.Duration)
	usage := &llm.UsageService{St: st}
	limiter := llm.NewLimiter(cfg.LLM.PerConvConcurrency, cfg.LLM.PerUserQPS)
	proxy := llm.NewProxy(catalog, sandboxes, usage, limiter, cfg.LLM)
	runs := run.NewService(st, events, hub, ids, catalog, sandboxes, agent, eventsConsumer, quotas, cfg)
	conversations := conversation.New(st, events, ids, catalog)
	conversations.SetHooks(runs, sandboxes)
	workspaceFiles := files.New(st, sandboxes, agent, cfg.Files.MaxBytes)
	tickets := preview.NewTickets(master, cfg.Preview.TicketTTL.Duration)
	previews := preview.NewService(st, sandboxes, agent, cfg, tickets)
	watcher := wsdwatch.New(agent, hub, mapper, sandboxes)
	// The observer must be installed before recovery can attach existing wsd streams.
	sandboxes.SetObserver(watcher)
	registry := metrics.New()
	appCtx, cancel := context.WithCancel(context.Background())
	a := &App{
		Config: cfg, Store: st, Auth: auth.New(st, ids, cfg.Auth), Catalog: catalog,
		Quota: quotas, Sandbox: sandboxes, Hub: hub, Events: events, Agent: agent,
		Consumer: eventsConsumer, Usage: usage, Limiter: limiter, LLMProxy: proxy,
		Runs: runs, Conversations: conversations, Files: workspaceFiles, Preview: previews,
		Tickets: tickets, Watcher: watcher, Registry: registry, ctx: appCtx, cancel: cancel,
	}
	a.APIHandler = api.NewRouter(api.Dependencies{Auth: a.Auth, Catalog: catalog, Conversations: conversations, Runs: runs, Events: events, Hub: hub, Mapper: mapper, Files: workspaceFiles, Preview: previews, Config: cfg})
	a.PreviewHandler = preview.NewHandler(st, sandboxes, agent, tickets, master, cfg)
	a.InternalHandler = internalapi.NewRouter(sandboxes, quotas, catalog, proxy, cfg)
	a.MetricsHandler = a.metricsMux()
	ok = true
	return a, nil
}

func (a *App) Start(ctx context.Context) error {
	if err := a.Sandbox.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile sandboxes: %w", err)
	}
	if err := a.Runs.Recover(ctx); err != nil {
		return fmt.Errorf("recover Runs: %w", err)
	}
	a.wg.Add(2)
	go func() {
		defer a.wg.Done()
		a.Sandbox.Loops(a.ctx)
	}()
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case now := <-ticker.C:
				_ = a.Store.DeleteExpiredSessions(a.ctx, now.UTC())
			}
		}
	}()
	return nil
}

type Status struct {
	Time      string                 `json:"time"`
	Mode      string                 `json:"mode"`
	Driver    string                 `json:"sandbox_driver"`
	Runs      map[string]int         `json:"runs"`
	Sandboxes map[string]int         `json:"sandboxes"`
	Quota     map[string]quota.Stats `json:"quota"`
}

func (a *App) Status(ctx context.Context) (Status, error) {
	runs, err := a.Store.ActiveRunCounts(ctx)
	if err != nil {
		return Status{}, err
	}
	sandboxes, err := a.Store.SandboxCounts(ctx)
	if err != nil {
		return Status{}, err
	}
	quotas := a.Quota.Snapshot()
	for status, count := range runs {
		a.Registry.Set("control_active_runs", float64(count), "status", status)
	}
	for status, count := range sandboxes {
		a.Registry.Set("control_sandboxes", float64(count), "status", status)
	}
	for kind, stat := range quotas {
		a.Registry.Set("control_quota_active", float64(stat.Active), "kind", kind)
		a.Registry.Set("control_quota_queued", float64(stat.Queued), "kind", kind)
	}
	return Status{Time: time.Now().UTC().Format(time.RFC3339), Mode: a.Config.Mode, Driver: a.Config.Sandbox.Driver, Runs: runs, Sandboxes: sandboxes, Quota: quotas}, nil
}

func (a *App) metricsMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", a.Registry.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.HandleFunc("/debug/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		status, err := a.Status(r.Context())
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.JSON(w, http.StatusOK, status)
	})
	return mux
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.cancel()
		a.Runs.Close()
		a.Watcher.Close()
		a.closeErr = errors.Join(a.Sandbox.Close(), a.Store.Close())
		a.wg.Wait()
	})
	return a.closeErr
}
