package boot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitWorkspaceMigratesLegacyAgentRuntimeState(t *testing.T) {
	root, template := existingAppWorkspace(t)
	legacyState := filepath.Join(root, ".dsh", "data", "state.json")
	writeWorkspaceFixture(t, legacyState, `{"session":"preserved"}`)
	before, err := os.Stat(legacyState)
	if err != nil {
		t.Fatal(err)
	}

	created, err := InitWorkspace(root, template)
	if err != nil || created {
		t.Fatalf("InitWorkspace() created = %v, err = %v", created, err)
	}
	currentState := filepath.Join(root, ".agent-runtime", "data", "state.json")
	after, err := os.Stat(currentState)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("legacy state was not moved as the same filesystem object")
	}
	if got := readWorkspaceFixture(t, currentState); got != `{"session":"preserved"}` {
		t.Fatalf("migrated state = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, ".dsh")); !os.IsNotExist(err) {
		t.Fatalf("legacy state directory still exists: %v", err)
	}
	for _, dir := range []string{"data", "checkpoints"} {
		info, err := os.Stat(filepath.Join(root, ".agent-runtime", dir))
		if err != nil || !info.IsDir() {
			t.Fatalf("migrated %s directory: info = %v, err = %v", dir, info, err)
		}
	}

	created, err = InitWorkspace(root, template)
	if err != nil || created {
		t.Fatalf("second InitWorkspace() created = %v, err = %v", created, err)
	}
	if got := readWorkspaceFixture(t, currentState); got != `{"session":"preserved"}` {
		t.Fatalf("state after repeated initialization = %q", got)
	}
}

func TestInitWorkspacePreservesExistingAgentRuntimeState(t *testing.T) {
	root, template := existingAppWorkspace(t)
	legacyState := filepath.Join(root, ".dsh", "legacy.txt")
	currentState := filepath.Join(root, ".agent-runtime", "current.txt")
	writeWorkspaceFixture(t, legacyState, "legacy must remain separate")
	writeWorkspaceFixture(t, currentState, "current must not be overwritten")

	created, err := InitWorkspace(root, template)
	if err != nil || created {
		t.Fatalf("InitWorkspace() created = %v, err = %v", created, err)
	}
	if got := readWorkspaceFixture(t, legacyState); got != "legacy must remain separate" {
		t.Fatalf("legacy state = %q", got)
	}
	if got := readWorkspaceFixture(t, currentState); got != "current must not be overwritten" {
		t.Fatalf("current state = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent-runtime", "legacy.txt")); !os.IsNotExist(err) {
		t.Fatalf("legacy file was merged into existing destination: %v", err)
	}
}

func TestMigrateAgentRuntimeStatePreservesEmptyDestination(t *testing.T) {
	root := t.TempDir()
	legacyState := filepath.Join(root, ".dsh", "legacy.txt")
	currentPath := filepath.Join(root, ".agent-runtime")
	writeWorkspaceFixture(t, legacyState, "legacy must remain")
	if err := os.Mkdir(currentPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := migrateAgentRuntimeState(root); err != nil {
		t.Fatal(err)
	}
	if got := readWorkspaceFixture(t, legacyState); got != "legacy must remain" {
		t.Fatalf("legacy state = %q", got)
	}
	entries, err := os.ReadDir(currentPath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty destination was replaced or populated: entries = %v, err = %v", entries, err)
	}
}

func TestMigrateAgentRuntimeStateRejectsNonDirectoryLegacy(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, ".dsh")
	writeWorkspaceFixture(t, legacyPath, "do not move a regular file")

	if err := migrateAgentRuntimeState(root); err == nil {
		t.Fatal("migration accepted a regular file as legacy state")
	}
	if got := readWorkspaceFixture(t, legacyPath); got != "do not move a regular file" {
		t.Fatalf("legacy file = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, ".agent-runtime")); !os.IsNotExist(err) {
		t.Fatalf("destination exists after rejected migration: %v", err)
	}
}

func existingAppWorkspace(t *testing.T) (root, template string) {
	t.Helper()
	root = t.TempDir()
	template = t.TempDir()
	writeWorkspaceFixture(t, filepath.Join(root, "app", "README.md"), "existing app")
	return root, template
}

func writeWorkspaceFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readWorkspaceFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
