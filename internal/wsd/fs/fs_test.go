package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeFileContainmentAndLimits(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel string, data []byte) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("src/main.txt", []byte("hello"))
	mustWrite("big.txt", []byte("abcdef"))
	mustWrite("binary.bin", []byte{0, 1, 2})
	mustWrite(".git/config", []byte("secret"))
	mustWrite(".hidden/file", []byte("secret"))
	mustWrite("node_modules/pkg/index.js", []byte("x"))
	mustWrite("data.sqlite", []byte("x"))
	mustWrite(".gitignore", []byte("bin"))
	outside := t.TempDir()
	mustWriteOutside := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(mustWriteOutside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mustWriteOutside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mustWriteOutside, filepath.Join(root, ".hidden", "escape")); err != nil {
		t.Fatal(err)
	}

	service, err := New(root, nil, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	entries, err := service.Tree("", 3)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	foundLink := false
	for _, entry := range entries {
		names = append(names, entry.Path)
		if entry.Path == "escape" && entry.Type == "symlink" {
			foundLink = true
		}
		for _, forbidden := range []string{".git", ".hidden", "node_modules", "data.sqlite"} {
			if entry.Path == forbidden || strings.HasPrefix(entry.Path, forbidden+"/") {
				t.Fatalf("ignored entry leaked: %+v", entry)
			}
		}
	}
	if !foundLink || !strings.Contains(strings.Join(names, ","), ".gitignore") {
		t.Fatalf("unexpected tree: %v", names)
	}

	for _, bad := range []string{"../secret", "/etc/passwd", "escape", ".hidden/escape", `..\secret`} {
		if _, err := service.File(bad, 0); !errors.Is(err, ErrBadPath) {
			t.Fatalf("File(%q) = %v", bad, err)
		}
	}
	if _, err := service.Tree("escape", 1); !errors.Is(err, ErrBadPath) {
		t.Fatalf("Tree(symlink) = %v", err)
	}
	hidden, err := service.File(".hidden/file", 0)
	if err != nil || hidden.Content != "secret" {
		t.Fatalf("known file below hidden directory = %+v, %v", hidden, err)
	}
	if _, err := service.Tree(".hidden", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Tree(hidden directory) = %v, want ErrNotFound", err)
	}
	for _, ignored := range []string{".git/config", "node_modules/pkg/index.js", "data.sqlite"} {
		if _, err := service.File(ignored, 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("File(explicitly ignored %q) = %v, want ErrNotFound", ignored, err)
		}
	}
	content, err := service.File("big.txt", 3)
	if err != nil {
		t.Fatal(err)
	}
	if content.Content != "abc" || !content.Truncated || content.Size != 6 || len(content.ETag) != 16 {
		t.Fatalf("bounded content = %+v", content)
	}
	binary, err := service.File("binary.bin", 0)
	if err != nil || !binary.Binary || binary.Content != "" {
		t.Fatalf("binary = %+v, %v", binary, err)
	}
	if _, err := service.File("big.txt", 1025); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("service max = %v", err)
	}
}

func TestDefaultLimitExactBoundaryAndNULPaths(t *testing.T) {
	root := t.TempDir()
	exact := strings.Repeat("x", 1<<20)
	if err := os.WriteFile(filepath.Join(root, "exact.txt"), []byte(exact), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "over.txt"), []byte(exact+"y"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	for _, bad := range []string{"exact.txt\x00", "dir/\x00/child"} {
		if _, err := service.File(bad, 0); !errors.Is(err, ErrBadPath) {
			t.Errorf("File(%q) error=%v, want ErrBadPath", bad, err)
		}
		if _, err := service.Tree(bad, 1); !errors.Is(err, ErrBadPath) {
			t.Errorf("Tree(%q) error=%v, want ErrBadPath", bad, err)
		}
	}
	content, err := service.File("exact.txt", 0)
	if err != nil || content.Size != 1<<20 || content.Truncated || content.Content != exact {
		t.Fatalf("exact default-limit file: size=%d truncated=%v content=%d err=%v", content.Size, content.Truncated, len(content.Content), err)
	}
	content, err = service.File("over.txt", 0)
	if err != nil || content.Size != 1<<20+1 || !content.Truncated || content.Content != exact {
		t.Fatalf("over-default-limit file: size=%d truncated=%v content=%d err=%v", content.Size, content.Truncated, len(content.Content), err)
	}
	if _, err := service.File("exact.txt", 1<<20); err != nil {
		t.Fatalf("explicit exact limit was rejected: %v", err)
	}
	if _, err := service.File("exact.txt", 1<<20+1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("explicit over-limit error=%v, want ErrTooLarge", err)
	}
}
