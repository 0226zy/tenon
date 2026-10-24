package boot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/internal/wsd/agentproxy"
	"github.com/0226zy/proto-platform/internal/wsd/api"
	"github.com/0226zy/proto-platform/internal/wsd/bus"
	"github.com/0226zy/proto-platform/internal/wsd/cpclient"
	"github.com/0226zy/proto-platform/internal/wsd/disk"
	workspacefs "github.com/0226zy/proto-platform/internal/wsd/fs"
	"github.com/0226zy/proto-platform/internal/wsd/gate"
	gitservice "github.com/0226zy/proto-platform/internal/wsd/git"
	"github.com/0226zy/proto-platform/internal/wsd/preview"
	"github.com/0226zy/proto-platform/internal/wsd/proc"
	"github.com/0226zy/proto-platform/internal/wsd/watch"
	"github.com/0226zy/proto-platform/pkg/contract"
)

func Run(ctx context.Context, env Env) (returnErr error) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{})).With("sandbox_id", env.SandboxID)
	if err := proc.SetSubreaper(); err != nil {
		return fmt.Errorf("set child subreaper: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer func() {
		cancel()
		background.Wait()
	}()
	registry := proc.New()
	background.Add(1)
	go func() {
		defer background.Done()
		registry.StartReaper(runCtx)
	}()

	cp := cpclient.New(env.CPInternalURL, env.LLMProxyToken)
	configCtx, configCancel := context.WithTimeout(runCtx, 30*time.Second)
	cfg, err := fetchSandboxConfig(configCtx, cp, logger)
	configCancel()
	if err != nil {
		return fmt.Errorf("fetch sandbox config: %w", err)
	}
	created, err := InitWorkspace(env.WorkspaceRoot, env.TemplateDir)
	if err != nil {
		return fmt.Errorf("initialize workspace: %w", err)
	}
	logger.Info("workspace initialized", "created", created)
	agentRuntimeDir := filepath.Join(env.WorkspaceRoot, ".agent-runtime")
	if err := WriteModelsConfig(agentRuntimeDir, cfg, env.CPInternalURL); err != nil {
		return fmt.Errorf("write models config: %w", err)
	}
	tokenRaw := make([]byte, 32)
	if _, err := rand.Read(tokenRaw); err != nil {
		return err
	}
	agentRuntimeToken := hex.EncodeToString(tokenRaw)
	assets, err := resolveAssets(env)
	if err != nil {
		return err
	}
	if err := writeSystemPrompt(assets.basePrompt, assets.appendPrompt, cfg.SystemPromptAppend, filepath.Join(agentRuntimeDir, "system_prompt.md")); err != nil {
		return err
	}
	agentRuntimeAddress, err := allocateAgentRuntimeAddress(env.Local)
	if err != nil {
		return err
	}
	if err := writeAgentRuntimeConfig(agentRuntimeDir, cfg, agentRuntimeToken, assets.skillsDir, agentRuntimeAddress); err != nil {
		return fmt.Errorf("write agent-runtime config: %w", err)
	}

	home := filepath.Join(env.WorkspaceRoot, ".home")
	processEnv := agentRuntimeEnvironment(env, home, agentRuntimeToken)
	cmd := exec.Command(env.AgentRuntimeBin, "-config", filepath.Join(agentRuntimeDir, "agent-runtime.yaml"))
	cmd.Dir = filepath.Join(env.WorkspaceRoot, "app")
	cmd.Env = processEnv
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	agentRuntime := newAgentRuntimeSupervisor(cmd, registry, agentRuntimeAddress)
	var stopAgentRuntimeOnce sync.Once
	stopAgentRuntime := func() {
		stopAgentRuntimeOnce.Do(func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCancel()
			if err := agentRuntime.Stop(stopCtx); err != nil {
				logger.Error("agent-runtime stop failed", "error", err)
			}
		})
	}
	defer stopAgentRuntime()
	startCtx, startCancel := context.WithTimeout(runCtx, 30*time.Second)
	err = agentRuntime.Start(startCtx)
	startCancel()
	if err != nil {
		return fmt.Errorf("start agent-runtime: %w", err)
	}

	fileService, err := workspacefs.New(filepath.Join(env.WorkspaceRoot, "app"), nil, 1<<20)
	if err != nil {
		return err
	}
	defer fileService.Close()
	eventBus := bus.New()
	workspaceGate := &gate.Gate{}
	diskLimit := cfg.Disk.LimitBytes
	if diskLimit <= 0 {
		diskLimit = 5 << 30
	}
	highWatermark := cfg.Disk.HighWatermark
	if highWatermark <= 0 {
		highWatermark = 0.9
	}
	measureDisk := func() (disk.Usage, error) {
		return disk.Measure(env.WorkspaceRoot, diskLimit, highWatermark)
	}
	var diskMu sync.RWMutex
	cachedDisk, err := measureDisk()
	if err != nil {
		return fmt.Errorf("measure workspace disk: %w", err)
	}
	currentDisk := func() (disk.Usage, error) {
		diskMu.RLock()
		defer diskMu.RUnlock()
		return cachedDisk, nil
	}
	refreshDisk := func() {
		usage, err := measureDisk()
		if err != nil {
			logger.Error("disk measurement failed", "error", err)
			return
		}
		diskMu.Lock()
		cachedDisk = usage
		diskMu.Unlock()
		eventBus.Publish("disk_usage", usage)
	}
	previewTimeout := time.Duration(cfg.Preview.BuildTimeoutS) * time.Second
	if previewTimeout <= 0 {
		previewTimeout = 90 * time.Second
	}
	previewLogLines := cfg.Preview.LogLines
	if previewLogLines <= 0 {
		previewLogLines = 2000
	}
	previewSupervisor := preview.New(preview.Config{
		AppRoot: filepath.Join(env.WorkspaceRoot, "app"), DataDir: filepath.Join(env.WorkspaceRoot, "appdata"),
		Mode: cfg.Preview.Mode, BuildTimeout: previewTimeout, MaxRestarts: cfg.Preview.MaxRestarts,
		LogLines: previewLogLines, Local: env.Local, Port: env.PreviewPort,
		Env: previewEnvironment(env, home), DiskUsage: measureDisk,
	}, workspaceGate, registry, cp, eventBus)
	var stopPreviewOnce sync.Once
	stopPreview := func() {
		stopPreviewOnce.Do(func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer stopCancel()
			if err := previewSupervisor.Stop(stopCtx); err != nil {
				logger.Error("preview stop failed", "error", err)
			}
		})
	}
	defer stopPreview()
	gitService := gitservice.New(filepath.Join(env.WorkspaceRoot, "app"), filepath.Join(agentRuntimeDir, "checkpoints"), workspaceGate, eventBus)
	agentURL, err := parseAgentURL("http://" + agentRuntimeAddress)
	if err != nil {
		return err
	}
	server := api.NewServer(api.Deps{
		Token: env.WSDToken, SandboxID: env.SandboxID,
		Agent: agentproxy.New(agentURL, agentRuntimeToken, nil), Gate: workspaceGate, Proc: registry,
		FS: fileService, Preview: previewSupervisor, Git: gitService, Bus: eventBus,
		Health: func() contract.Health {
			healthCtx, healthCancel := context.WithTimeout(runCtx, time.Second)
			defer healthCancel()
			agentOK := agentRuntime.Healthy(healthCtx)
			_, workspaceErr := os.Stat(filepath.Join(env.WorkspaceRoot, "app"))
			return contract.Health{
				API: contract.WSDAPI, Agent: map[bool]string{true: "ok", false: "unavailable"}[agentOK],
				Workspace: map[bool]string{true: "ok", false: "unavailable"}[workspaceErr == nil],
				Image:     env.ImageVer, OK: agentOK && workspaceErr == nil, Version: "p0",
			}
		},
		DiskUsage: currentDisk, Logger: logger,
	})
	server.Addr = env.Listen
	server.BaseContext = func(net.Listener) context.Context { return runCtx }
	listener, err := net.Listen("tcp", env.Listen)
	if err != nil {
		return fmt.Errorf("listen wsd: %w", err)
	}

	background.Add(2)
	go func() {
		defer background.Done()
		if err := watch.Start(runCtx, filepath.Join(env.WorkspaceRoot, "app"), nil, 500*time.Millisecond, eventBus); err != nil {
			logger.Error("workspace watch stopped", "error", err)
		}
	}()
	go func() {
		defer background.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				refreshDisk()
			}
		}
	}()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()
	logger.Info("wsd ready", "listen", env.Listen, "image", env.ImageVer)

	select {
	case <-ctx.Done():
		returnErr = nil
	case err := <-agentRuntime.Done():
		if err == nil {
			err = errors.New("agent-runtime exited")
		}
		returnErr = fmt.Errorf("agent-runtime supervision: %w", err)
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			returnErr = fmt.Errorf("wsd HTTP server: %w", err)
		}
	}

	cancel()
	_ = listener.Close() // Stop accepting new work before process teardown.
	eventBus.Close()
	stopPreview()
	stopAgentRuntime()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	return returnErr
}

func fetchSandboxConfig(ctx context.Context, cp *cpclient.Client, logger *slog.Logger) (contract.SandboxConfig, error) {
	backoff := time.Second
	var lastErr error
	for {
		cfg, err := cp.SandboxConfig(ctx)
		if err == nil {
			return cfg, nil
		}
		lastErr = err
		logger.Error("sandbox config fetch failed", "error", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return contract.SandboxConfig{}, lastErr
		case <-timer.C:
		}
		if backoff < 8*time.Second {
			backoff *= 2
		}
	}
}

type runtimeAssets struct {
	skillsDir    string
	basePrompt   string
	appendPrompt string
}

func resolveAssets(env Env) (runtimeAssets, error) {
	if !env.Local {
		return runtimeAssets{skillsDir: "/opt/agent-runtime/skills", basePrompt: "/opt/agent-runtime/system_prompt.md", appendPrompt: "/opt/agent-runtime/system_prompt.append.md"}, nil
	}
	agentRuntimeRoot := filepath.Dir(env.AgentRuntimeBin)
	if filepath.Base(agentRuntimeRoot) == "bin" {
		agentRuntimeRoot = filepath.Dir(agentRuntimeRoot)
	}
	imageRoot := ""
	if filepath.Base(env.TemplateDir) == "react-go-sqlite" && filepath.Base(filepath.Dir(env.TemplateDir)) == "templates" {
		imageRoot = filepath.Dir(filepath.Dir(env.TemplateDir))
	}
	skillsDir := filepath.Join(env.WorkspaceRoot, ".agent-runtime", "skills")
	if err := os.RemoveAll(skillsDir); err != nil {
		return runtimeAssets{}, err
	}
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return runtimeAssets{}, err
	}
	for _, source := range []string{filepath.Join(agentRuntimeRoot, "skills"), filepath.Join(imageRoot, "skills")} {
		if info, err := os.Stat(source); err == nil && info.IsDir() {
			if err := copyDirectoryContents(source, skillsDir); err != nil {
				return runtimeAssets{}, err
			}
		}
	}
	basePrompt := filepath.Join(agentRuntimeRoot, "prompt", "system_prompt.md")
	if _, err := os.Stat(basePrompt); err != nil {
		basePrompt = "/opt/agent-runtime/system_prompt.md"
	}
	if _, err := os.Stat(basePrompt); err != nil {
		// A fake agent-runtime used in trusted local contract tests need not ship
		// the real prompt. The merged file still contains all platform append segments.
		basePrompt = filepath.Join(env.WorkspaceRoot, ".agent-runtime", "system_prompt.base.md")
		if err := writeAtomic(basePrompt, []byte("\n"), 0o600); err != nil {
			return runtimeAssets{}, err
		}
	}
	appendPrompt := "/opt/agent-runtime/system_prompt.append.md"
	if imageRoot != "" {
		candidate := filepath.Join(imageRoot, "system_prompt.append.md")
		if _, err := os.Stat(candidate); err == nil {
			appendPrompt = candidate
		}
	}
	return runtimeAssets{skillsDir: skillsDir, basePrompt: basePrompt, appendPrompt: appendPrompt}, nil
}

func copyDirectoryContents(source, destination string) error {
	cmd := exec.Command("cp", "-a", source+string(os.PathSeparator)+".", destination+string(os.PathSeparator))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy runtime assets: %s", limited(output))
	}
	return nil
}

func parseAgentURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func agentRuntimeEnvironment(env Env, home, token string) []string {
	values := baseProcessEnvironment(env, home)
	values["LLM_PROXY_TOKEN"] = env.LLMProxyToken
	values["MAGENTS_AUTH_TOKEN"] = token
	values["WSD_URL"] = env.AgentWSDURL()
	values["WSD_TOKEN"] = env.WSDToken
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func previewEnvironment(env Env, home string) map[string]string {
	values := baseProcessEnvironment(env, home)
	delete(values, "LLM_PROXY_TOKEN")
	delete(values, "MAGENTS_AUTH_TOKEN")
	delete(values, "WSD_TOKEN")
	delete(values, "WSD_URL")
	return values
}

func baseProcessEnvironment(env Env, home string) map[string]string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	lang := os.Getenv("LANG")
	if lang == "" {
		lang = "C.UTF-8"
	}
	gomodcache := env.Proxy.GOMODCACHE
	if gomodcache == "" {
		gomodcache = filepath.Join(home, "go", "pkg", "mod")
	}
	npmCache := env.Proxy.NPMConfigCache
	if npmCache == "" {
		npmCache = filepath.Join(home, ".npm")
	}
	_ = os.MkdirAll(gomodcache, 0o700)
	_ = os.MkdirAll(npmCache, 0o700)
	return map[string]string{
		"PATH": path, "HOME": home, "LANG": lang, "TERM": os.Getenv("TERM"),
		"GOPROXY": env.Proxy.GOPROXY, "GOFLAGS": env.Proxy.GOFLAGS, "GOMODCACHE": gomodcache, "GOWORK": "off",
		"npm_config_registry": env.Proxy.NPMConfigRegistry, "npm_config_cache": npmCache,
	}
}
