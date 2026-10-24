//go:build linux || darwin

package boot

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/pkg/contract"
	"gopkg.in/yaml.v3"
)

func TestRunBootHealthAndAuthenticatedAgentProxy(t *testing.T) {
	// Hold the production port when possible. An existing listener also verifies
	// that local startup neither depends on nor adopts whatever owns 3080.
	fixedListener, err := net.Listen("tcp", "127.0.0.1:3080")
	if err == nil {
		defer fixedListener.Close()
	}
	var cfg contract.SandboxConfig
	cfg.Models = []contract.SandboxModel{{ID: "model-a", Default: true}}
	cfg.Agent.MaxIterations = 80
	cfg.Agent.TokenBudgetPerTurn = 800000
	cfg.Agent.ContextCompressAtTokens = 96000
	cfg.SystemPromptAppend = "integration append"
	cfg.Preview.Mode = "live"
	cfg.Preview.BuildTimeoutS = 5
	cfg.Preview.MaxRestarts = 1
	cfg.Preview.LogLines = 100
	cfg.Disk.LimitBytes = 1 << 30
	cfg.Disk.HighWatermark = 0.9
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer llm-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/internal/sandbox/config" {
			_ = json.NewEncoder(w).Encode(cfg)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer control.Close()

	workspace := t.TempDir()
	template := t.TempDir()
	if err := os.WriteFile(filepath.Join(template, "README.md"), []byte("integration"), 0o600); err != nil {
		t.Fatal(err)
	}
	dshdBin := filepath.Join(t.TempDir(), "fake-dshd")
	script := `#!/usr/bin/env python3
import os
import re
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
class Handler(BaseHTTPRequestHandler):
    def reply(self, code, body=b"{}"):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def do_GET(self):
        self.reply(200 if self.path == "/healthz" else 404)
    def do_POST(self):
        expected = "Bearer " + os.environ.get("MAGENTS_AUTH_TOKEN", "")
        if self.path == "/v1/sessions" and self.headers.get("Authorization") == expected:
            self.reply(200, b'{"id":"session-1"}')
        else:
            self.reply(401)
    def log_message(self, *args):
        pass
config_path = sys.argv[sys.argv.index("-config") + 1]
with open(config_path, encoding="utf-8") as config_file:
    config = config_file.read()
match = re.search(r"^\s*listen:\s*(\S+)\s*$", config, re.MULTILINE)
if match is None:
    raise RuntimeError("dsh config has no listen address")
host, port = match.group(1).rsplit(":", 1)
HTTPServer((host, int(port)), Handler).serve_forever()
`
	if err := os.WriteFile(dshdBin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wsdPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	env := Env{
		SandboxID: "integration", CPInternalURL: control.URL, LLMProxyToken: "llm-token", WSDToken: "wsd-token",
		Local: true, Listen: "127.0.0.1:" + strconv.Itoa(wsdPort), WorkspaceRoot: workspace,
		TemplateDir: template, DshdBin: dshdBin, ImageVer: "integration-image", PreviewPort: 0,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, env)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("wsd did not stop")
		}
	}()

	base := "http://127.0.0.1:" + strconv.Itoa(wsdPort)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	var health contract.Health
	deadline := time.Now().Add(12 * time.Second)
	for {
		resp, err := client.Get(base + "/healthz")
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				err = json.NewDecoder(resp.Body).Decode(&health)
				resp.Body.Close()
				if err == nil {
					break
				}
			} else {
				resp.Body.Close()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("wsd did not become healthy: %+v, %v", health, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if health.API != contract.WSDAPI || health.Agent != "ok" || health.Workspace != "ok" || health.Image != "integration-image" {
		t.Fatalf("health = %+v", health)
	}
	rawDshConfig, err := os.ReadFile(filepath.Join(workspace, ".dsh", "dsh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var generated struct {
		Server struct {
			Listen string `yaml:"listen"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(rawDshConfig, &generated); err != nil {
		t.Fatal(err)
	}
	dshdAddress := generated.Server.Listen
	host, portText, err := net.SplitHostPort(dshdAddress)
	if err != nil {
		t.Fatalf("invalid dshd address %q: %v", dshdAddress, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || host != "127.0.0.1" || port <= 0 || dshdAddress == "127.0.0.1:3080" {
		t.Fatalf("local dshd address = %q", dshdAddress)
	}
	resp, err := client.Post(base+"/agent/sessions", "application/json", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("agent without bearer = %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/sessions", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer wsd-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || session["id"] != "session-1" {
		t.Fatalf("agent response = %d %+v", resp.StatusCode, session)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wsd did not stop after cancellation")
	}
	conn, err := net.DialTimeout("tcp", dshdAddress, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("fake dshd remained after wsd shutdown")
	}
}
