// Package boot assembles the wsd process from its environment.
package boot

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

type ProxyEnv struct {
	GOPROXY           string
	GOFLAGS           string
	GOMODCACHE        string
	NPMConfigRegistry string
	NPMConfigCache    string
}

type Env struct {
	SandboxID     string
	CPInternalURL string
	LLMProxyToken string
	WSDToken      string
	Proxy         ProxyEnv

	Local           bool
	Listen          string
	WorkspaceRoot   string
	TemplateDir     string
	AgentRuntimeBin string
	ImageVer        string
	PreviewPort     int
}

func LoadEnv() (Env, error) {
	env := Env{
		SandboxID: os.Getenv("SANDBOX_ID"), CPInternalURL: os.Getenv("CP_INTERNAL_URL"),
		LLMProxyToken: os.Getenv("LLM_PROXY_TOKEN"), WSDToken: os.Getenv("WSD_TOKEN"),
		Listen:          defaultEnv("WSD_LISTEN", "0.0.0.0:7000"),
		WorkspaceRoot:   defaultEnv("WORKSPACE_ROOT", "/workspace"),
		TemplateDir:     defaultEnv("WSD_TEMPLATE_DIR", "/opt/templates/react-go-sqlite"),
		AgentRuntimeBin: defaultEnv("WSD_AGENT_RUNTIME_BIN", "/usr/local/bin/agent-runtime"),
		ImageVer:        defaultEnv("WSD_IMAGE_VER", "dev"),
		Proxy: ProxyEnv{
			GOPROXY: os.Getenv("GOPROXY"), GOFLAGS: os.Getenv("GOFLAGS"), GOMODCACHE: os.Getenv("GOMODCACHE"),
			NPMConfigRegistry: os.Getenv("npm_config_registry"), NPMConfigCache: os.Getenv("npm_config_cache"),
		},
	}
	switch raw := os.Getenv("WSD_LOCAL"); raw {
	case "", "false":
	case "true":
		env.Local = true
	default:
		return Env{}, errors.New("WSD_LOCAL must be exactly true or false")
	}
	if env.SandboxID == "" || env.CPInternalURL == "" || env.LLMProxyToken == "" || env.WSDToken == "" {
		return Env{}, errors.New("SANDBOX_ID, CP_INTERNAL_URL, LLM_PROXY_TOKEN and WSD_TOKEN are required")
	}
	parsed, err := url.Parse(env.CPInternalURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Env{}, errors.New("CP_INTERNAL_URL must be an absolute HTTP(S) URL without credentials or query")
	}
	for name, value := range map[string]string{"WORKSPACE_ROOT": env.WorkspaceRoot, "WSD_TEMPLATE_DIR": env.TemplateDir, "WSD_AGENT_RUNTIME_BIN": env.AgentRuntimeBin} {
		if !filepath.IsAbs(value) {
			return Env{}, fmt.Errorf("%s must be an absolute path", name)
		}
	}
	if err := validateListen(env.Listen); err != nil {
		return Env{}, err
	}
	env.PreviewPort = 3000
	if raw := os.Getenv("WSD_PREVIEW_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 0 || port > 65535 {
			return Env{}, errors.New("WSD_PREVIEW_PORT must be between 0 and 65535")
		}
		env.PreviewPort = port
	}
	if !env.Local {
		if env.WorkspaceRoot != "/workspace" || env.TemplateDir != "/opt/templates/react-go-sqlite" || env.AgentRuntimeBin != "/usr/local/bin/agent-runtime" || env.Listen != "0.0.0.0:7000" || env.PreviewPort != 3000 {
			return Env{}, errors.New("production paths and ports are fixed; set WSD_LOCAL=true only for trusted local acceptance")
		}
	}
	return env, nil
}

func defaultEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func validateListen(listen string) error {
	_, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("WSD_LISTEN is invalid: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return errors.New("WSD_LISTEN must use a fixed port between 1 and 65535")
	}
	return nil
}

func (e Env) AgentWSDURL() string {
	if !e.Local {
		return "http://127.0.0.1:7000"
	}
	_, port, _ := net.SplitHostPort(e.Listen)
	return "http://127.0.0.1:" + port
}
