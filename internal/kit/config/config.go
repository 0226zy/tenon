// Package config loads the control-plane YAML configuration and its defaults.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar")
	}
	if n.Tag == "!!int" {
		var v int64
		if err := n.Decode(&v); err != nil {
			return err
		}
		d.Duration = time.Duration(v)
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
	return nil
}
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

type Config struct {
	Mode        string      `yaml:"mode"`
	Listen      Listen      `yaml:"listen"`
	Web         Web         `yaml:"web"`
	Control     Control     `yaml:"control"`
	MySQL       MySQL       `yaml:"mysql"`
	SQLite      SQLite      `yaml:"sqlite"`
	Auth        Auth        `yaml:"auth"`
	Run         Run         `yaml:"run"`
	Quota       Quota       `yaml:"quota"`
	Sandbox     Sandbox     `yaml:"sandbox"`
	Local       Local       `yaml:"local"`
	Preview     Preview     `yaml:"preview"`
	LLM         LLM         `yaml:"llm"`
	Catalog     Catalog     `yaml:"catalog"`
	Event       Event       `yaml:"event"`
	Files       Files       `yaml:"files"`
	AgentClient AgentClient `yaml:"agentclient"`
	User        User        `yaml:"user"`
}

type Listen struct {
	API      string `yaml:"api"`
	Preview  string `yaml:"preview"`
	Internal string `yaml:"internal"`
	Metrics  string `yaml:"metrics"`
}
type Web struct {
	Origin string `yaml:"origin"`
}
type Control struct {
	SecretFile string `yaml:"secret_file"`
}
type MySQL struct {
	DSN     string `yaml:"dsn"`
	MaxOpen int    `yaml:"max_open"`
	MaxIdle int    `yaml:"max_idle"`
}
type SQLite struct {
	Path string `yaml:"path"`
}
type Auth struct {
	SessionTTL     Duration `yaml:"session_ttl"`
	CookieName     string   `yaml:"cookie_name"`
	CookieSecure   bool     `yaml:"cookie_secure"`
	LoginFailLimit int      `yaml:"login_fail_limit"`
	LoginLock      Duration `yaml:"login_lock"`
	Argon2         Argon2   `yaml:"argon2"`
}
type Argon2 struct {
	Time    uint32 `yaml:"time"`
	Memory  uint32 `yaml:"memory"`
	Threads uint8  `yaml:"threads"`
}
type Run struct {
	MaxDuration        Duration `yaml:"max_duration"`
	StopConfirmTimeout Duration `yaml:"stop_confirm_timeout"`
	ReconcileInterval  Duration `yaml:"reconcile_interval"`
	ContentMaxChars    int      `yaml:"content_max_chars"`
	PrepareRetry       int      `yaml:"prepare_retry"`
}
type Quota struct {
	Run     Limit `yaml:"run"`
	Sandbox Limit `yaml:"sandbox"`
	Build   Limit `yaml:"build"`
	Queue   Queue `yaml:"queue"`
}
type Limit struct {
	Global  int `yaml:"global"`
	PerUser int `yaml:"per_user"`
}
type Queue struct {
	PerUser int      `yaml:"per_user"`
	Timeout Duration `yaml:"timeout"`
}
type Sandbox struct {
	Image                      string      `yaml:"image"`
	Network                    string      `yaml:"network"`
	Driver                     string      `yaml:"driver"`
	CPUs                       float64     `yaml:"cpus"`
	Memory                     string      `yaml:"memory"`
	Pids                       int         `yaml:"pids"`
	DiskLimit                  int64       `yaml:"disk_limit"`
	IdleStopAfter              Duration    `yaml:"idle_stop_after"`
	MaxLifetime                Duration    `yaml:"max_lifetime"`
	StartTimeout               Duration    `yaml:"start_timeout"`
	HealthInterval             Duration    `yaml:"health_interval"`
	HealthFailThreshold        int         `yaml:"health_fail_threshold"`
	VolumeRetentionAfterDelete Duration    `yaml:"volume_retention_after_delete"`
	InternalURL                string      `yaml:"internal_url"`
	Agent                      AgentLimits `yaml:"agent"`
	SystemPromptAppend         string      `yaml:"system_prompt_append"`
}
type AgentLimits struct {
	MaxIterations           int `yaml:"max_iterations"`
	TokenBudgetPerTurn      int `yaml:"token_budget_per_turn"`
	ContextCompressAtTokens int `yaml:"context_compress_at_tokens"`
}
type Local struct {
	Root        string `yaml:"root"`
	WSDBin      string `yaml:"wsd_bin"`
	TemplateDir string `yaml:"template_dir"`
	DSHDBin     string `yaml:"dshd_bin"`
	ImageVer    string `yaml:"image_ver"`
	GoProxy     string `yaml:"goproxy"`
}
type Preview struct {
	Domain            string   `yaml:"domain"`
	Scheme            string   `yaml:"scheme"`
	PublicAddress     string   `yaml:"public_address"`
	Mode              string   `yaml:"mode"`
	BuildTimeout      Duration `yaml:"build_timeout"`
	MaxRestarts       int      `yaml:"max_restarts"`
	LogLines          int      `yaml:"log_lines"`
	TicketTTL         Duration `yaml:"ticket_ttl"`
	CookieTTL         Duration `yaml:"cookie_ttl"`
	CookieSecure      bool     `yaml:"cookie_secure"`
	DiskHighWatermark float64  `yaml:"disk_high_watermark"`
}
type LLM struct {
	PerConvConcurrency int      `yaml:"per_conv_concurrency"`
	PerUserQPS         int      `yaml:"per_user_qps"`
	PerRunTokenLimit   int      `yaml:"per_run_token_limit"`
	MaxTokensCap       int      `yaml:"max_tokens_cap"`
	UpstreamTimeout    Duration `yaml:"upstream_timeout"`
}
type Catalog struct {
	File string `yaml:"file"`
}
type Event struct {
	SSEHeartbeat  Duration `yaml:"sse_heartbeat"`
	WriteTimeout  Duration `yaml:"write_timeout"`
	ToolArgsMax   int      `yaml:"tool_args_max"`
	ToolResultMax int      `yaml:"tool_result_max"`
}
type Files struct {
	MaxBytes int64 `yaml:"max_bytes"`
}
type AgentClient struct {
	Timeout             Duration `yaml:"timeout"`
	PreviewTimeout      Duration `yaml:"preview_timeout"`
	MaxIdleConnsPerHost int      `yaml:"max_idle_conns_per_host"`
}
type User struct {
	DefaultMaxConv int `yaml:"default_max_conv"`
}

func Default() *Config {
	c := &Config{}
	c.Mode = "mysql"
	c.Listen = Listen{API: "127.0.0.1:8080", Preview: "127.0.0.1:8081", Internal: "127.0.0.1:8082", Metrics: "127.0.0.1:9090"}
	c.Web.Origin = "http://127.0.0.1:8080"
	c.Control.SecretFile = "/etc/proto/secret.key"
	c.MySQL = MySQL{MaxOpen: 50, MaxIdle: 10}
	c.Auth = Auth{SessionTTL: Duration{7 * 24 * time.Hour}, CookieName: "proto_sid", CookieSecure: true, LoginFailLimit: 5, LoginLock: Duration{15 * time.Minute}, Argon2: Argon2{Time: 2, Memory: 64 * 1024, Threads: 2}}
	c.Run = Run{MaxDuration: Duration{30 * time.Minute}, StopConfirmTimeout: Duration{30 * time.Second}, ReconcileInterval: Duration{5 * time.Second}, ContentMaxChars: 20000, PrepareRetry: 3}
	c.Quota = Quota{Run: Limit{8, 2}, Sandbox: Limit{10, 3}, Build: Limit{2, 1}, Queue: Queue{5, Duration{10 * time.Minute}}}
	c.Sandbox = Sandbox{Image: "proto-sandbox:v3", Network: "proto-sbx", Driver: "docker", CPUs: 1, Memory: "2g", Pids: 512, DiskLimit: 10 << 30, IdleStopAfter: Duration{30 * time.Minute}, MaxLifetime: Duration{24 * time.Hour}, StartTimeout: Duration{60 * time.Second}, HealthInterval: Duration{10 * time.Second}, HealthFailThreshold: 3, VolumeRetentionAfterDelete: Duration{7 * 24 * time.Hour}, InternalURL: "http://127.0.0.1:8082", Agent: AgentLimits{80, 800000, 96000}}
	c.Preview = Preview{Domain: "example.com", Scheme: "https", Mode: "live", BuildTimeout: Duration{90 * time.Second}, MaxRestarts: 3, LogLines: 200, TicketTTL: Duration{time.Minute}, CookieTTL: Duration{12 * time.Hour}, DiskHighWatermark: .9}
	c.LLM = LLM{PerConvConcurrency: 4, PerUserQPS: 5, PerRunTokenLimit: 2000000, MaxTokensCap: 8192, UpstreamTimeout: Duration{180 * time.Second}}
	c.Event = Event{SSEHeartbeat: Duration{15 * time.Second}, WriteTimeout: Duration{30 * time.Second}, ToolArgsMax: 2048, ToolResultMax: 4096}
	c.Files.MaxBytes = 1 << 20
	c.AgentClient = AgentClient{Timeout: Duration{10 * time.Second}, PreviewTimeout: Duration{100 * time.Second}, MaxIdleConnsPerHost: 4}
	c.User.DefaultMaxConv = 20
	return c
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	base := filepath.Dir(absolutePath)
	resolve := func(p *string) {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Clean(filepath.Join(base, *p))
		}
	}
	resolve(&c.Control.SecretFile)
	resolve(&c.SQLite.Path)
	resolve(&c.Catalog.File)
	resolve(&c.Local.Root)
	resolve(&c.Local.WSDBin)
	resolve(&c.Local.TemplateDir)
	resolve(&c.Local.DSHDBin)
	if v := os.Getenv("CONTROL_MYSQL_DSN"); v != "" {
		c.MySQL.DSN = v
	}
	if v := os.Getenv("CONTROL_MYSQL_PASSWORD"); v != "" {
		if strings.Contains(c.MySQL.DSN, "${CONTROL_MYSQL_PASSWORD}") {
			c.MySQL.DSN = strings.ReplaceAll(c.MySQL.DSN, "${CONTROL_MYSQL_PASSWORD}", v)
		}
	}
	if c.Mode != "local" && c.Mode != "mysql" {
		return nil, fmt.Errorf("mode must be local or mysql")
	}
	if c.Mode == "local" {
		c.Sandbox.Driver = "local"
		if c.SQLite.Path == "" {
			return nil, fmt.Errorf("sqlite.path is required in local mode")
		}
		if c.Local.Root == "" || c.Local.WSDBin == "" {
			return nil, fmt.Errorf("local.root and local.wsd_bin are required")
		}
	} else if c.MySQL.DSN == "" {
		return nil, fmt.Errorf("mysql.dsn is required")
	}
	if c.Preview.Domain == "" || c.Control.SecretFile == "" || c.Catalog.File == "" {
		return nil, fmt.Errorf("preview.domain, control.secret_file and catalog.file are required")
	}
	return c, nil
}
