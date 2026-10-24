package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocalResolvesPathsAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir = canonicalDir
	path := filepath.Join(dir, "control.yaml")
	raw := `mode: local
sqlite:
  path: data/control.db
control:
  secret_file: secrets/master.key
catalog:
  file: models.yaml
preview:
  domain: preview.local
local:
  root: sandboxes
  wsd_bin: bin/wsd
  template_dir: template
  agent_runtime_bin: bin/agent-runtime
run:
  max_duration: 2m
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_MYSQL_DSN", "from-environment")
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(workingDir) })
	cfg, err := Load("control.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sandbox.Driver != "local" || cfg.MySQL.DSN != "from-environment" || cfg.Run.MaxDuration.Duration.Minutes() != 2 {
		t.Fatalf("loaded config=%+v", cfg)
	}
	for name, got := range map[string]string{
		"sqlite": cfg.SQLite.Path, "secret": cfg.Control.SecretFile, "catalog": cfg.Catalog.File,
		"root": cfg.Local.Root, "wsd": cfg.Local.WSDBin, "template": cfg.Local.TemplateDir, "agent-runtime": cfg.Local.AgentRuntimeBin,
	} {
		if !filepath.IsAbs(got) || !strings.HasPrefix(got, dir+string(os.PathSeparator)) {
			t.Errorf("%s path was not resolved relative to config: %q", name, got)
		}
	}
}

func TestLoadRejectsUnknownFieldsAndMissingProductionSecretDSN(t *testing.T) {
	dir := t.TempDir()
	unknown := filepath.Join(dir, "unknown.yaml")
	if err := os.WriteFile(unknown, []byte("mode: local\nunknown_field: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(unknown); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("unknown field error=%v", err)
	}
	mysql := filepath.Join(dir, "mysql.yaml")
	raw := "mode: mysql\ncontrol:\n  secret_file: secret.key\ncatalog:\n  file: models.yaml\npreview:\n  domain: preview.local\n"
	if err := os.WriteFile(mysql, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(mysql); err == nil || !strings.Contains(err.Error(), "mysql.dsn") {
		t.Fatalf("missing DSN error=%v", err)
	}
}
