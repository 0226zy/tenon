package secret

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMasterLocalCreateReloadAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "master.key")
	if _, err := LoadMaster(path, false); err == nil {
		t.Fatal("production unexpectedly created a missing master key")
	}
	first, err := LoadMaster(path, true)
	if err != nil || len(first) != 32 {
		t.Fatalf("created key length=%d err=%v", len(first), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode=%v err=%v", info.Mode().Perm(), err)
	}
	second, err := LoadMaster(path, false)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("reloaded key differs: err=%v", err)
	}
}

func TestLoadMasterAcceptsBase64AndRejectsInvalidMaterial(t *testing.T) {
	dir := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	path := filepath.Join(dir, "base64.key")
	encoded := base64.RawURLEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMaster(path, false)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("base64 key=%q err=%v", got, err)
	}
	invalid := filepath.Join(dir, "invalid.key")
	if err := os.WriteFile(invalid, []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMaster(invalid, true); err == nil {
		t.Fatal("invalid master key was accepted")
	}
}
