// Package fs provides path-confined, read-only workspace file access.
package fs

import (
	"crypto/sha1" //nolint:gosec // The ETag is a change token, not a security hash.
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0226zy/tenon/pkg/contract"
)

var (
	ErrBadPath    = errors.New("bad workspace path")
	ErrNotFound   = errors.New("workspace path not found")
	ErrTooLarge   = errors.New("requested file limit is too large")
	ErrNotRegular = errors.New("workspace path is not a regular file")
)

var DefaultIgnore = []string{".git", "node_modules", "dist", "bin", "*.sqlite", "*.sqlite-*"}

type Service struct {
	root     *os.Root
	ignore   []string
	maxBytes int64
}

func New(root string, ignore []string, maxBytes int64) (*Service, error) {
	if root == "" {
		return nil, ErrBadPath
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	if ignore == nil {
		ignore = append([]string(nil), DefaultIgnore...)
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &Service{root: r, ignore: ignore, maxBytes: maxBytes}, nil
}

func (s *Service) Close() error { return s.root.Close() }

func cleanPath(rel string) (string, error) {
	if len(rel) > 1024 || strings.ContainsRune(rel, '\x00') || strings.Contains(rel, "\\") || path.IsAbs(rel) || filepath.IsAbs(rel) {
		return "", ErrBadPath
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", ErrBadPath
		}
	}
	clean := path.Clean(rel)
	if clean == "" {
		clean = "."
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "/" {
		return "", ErrBadPath
	}
	return clean, nil
}

func mapPathError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, iofs.ErrNotExist) {
		return ErrNotFound
	}
	return ErrBadPath
}

// secureLstat rejects explicitly ignored path components and never intentionally
// follows a symlink supplied by the caller. Tree also filters hidden directories;
// File may read a known path below one without weakening containment checks.
func (s *Service) secureLstat(rel string, filterHiddenDirs bool) (os.FileInfo, error) {
	parts := strings.Split(rel, "/")
	current := ""
	for i, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = path.Join(current, part)
		}
		info, err := s.root.Lstat(current)
		if err != nil {
			return nil, mapPathError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrBadPath
		}
		if matchesIgnore(current, s.ignore) || (filterHiddenDirs && isHiddenDir(current, info.IsDir())) {
			return nil, ErrNotFound
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, ErrBadPath
		}
	}
	info, err := s.root.Lstat(rel)
	if err != nil {
		return nil, mapPathError(err)
	}
	return info, nil
}

func isHiddenDir(rel string, isDir bool) bool {
	name := path.Base(rel)
	return isDir && strings.HasPrefix(name, ".") && name != "."
}

func matchesIgnore(rel string, ignore []string) bool {
	name := path.Base(rel)
	for _, pattern := range ignore {
		if pattern == name {
			return true
		}
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

// ShouldIgnore applies the shared tree and watcher filter. Hidden directories
// are excluded, while visible project files such as .gitignore remain listed.
func ShouldIgnore(rel string, isDir bool, ignore []string) bool {
	return isHiddenDir(rel, isDir) || matchesIgnore(rel, ignore)
}

func (s *Service) Tree(rel string, depth int) ([]contract.FileEntry, error) {
	clean, err := cleanPath(rel)
	if err != nil {
		return nil, err
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > 16 {
		return nil, ErrBadPath
	}
	info, err := s.secureLstat(clean, true)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrBadPath
	}
	entries := make([]contract.FileEntry, 0)
	const maxEntries = 10000
	var walk func(string, int) error
	walk = func(dir string, remaining int) error {
		children, err := iofs.ReadDir(s.root.FS(), dir)
		if err != nil {
			return mapPathError(err)
		}
		for _, child := range children {
			relChild := path.Join(dir, child.Name())
			if dir == "." {
				relChild = child.Name()
			}
			childInfo, err := s.root.Lstat(relChild)
			if err != nil {
				return mapPathError(err)
			}
			isDir := childInfo.IsDir() && childInfo.Mode()&os.ModeSymlink == 0
			if ShouldIgnore(relChild, isDir, s.ignore) {
				continue
			}
			typ := "file"
			switch {
			case childInfo.Mode()&os.ModeSymlink != 0:
				typ = "symlink"
			case isDir:
				typ = "dir"
			}
			entries = append(entries, contract.FileEntry{
				Name: child.Name(), Path: relChild, Type: typ,
				Size: childInfo.Size(), MTime: childInfo.ModTime().UTC(),
			})
			if len(entries) > maxEntries {
				return ErrTooLarge
			}
			if isDir && remaining > 1 {
				if err := walk(relChild, remaining-1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(clean, depth); err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

func (s *Service) File(rel string, max int64) (contract.FileContent, error) {
	clean, err := cleanPath(rel)
	if err != nil {
		return contract.FileContent{}, err
	}
	if max < 0 || max > s.maxBytes {
		return contract.FileContent{}, ErrTooLarge
	}
	if max == 0 {
		max = s.maxBytes
	}
	if _, err := s.secureLstat(clean, false); err != nil {
		return contract.FileContent{}, err
	}
	f, err := s.root.Open(clean)
	if err != nil {
		return contract.FileContent{}, mapPathError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return contract.FileContent{}, mapPathError(err)
	}
	if !info.Mode().IsRegular() {
		return contract.FileContent{}, ErrNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return contract.FileContent{}, err
	}
	truncated := int64(len(data)) > max || info.Size() > max
	if int64(len(data)) > max {
		data = data[:max]
	}
	etag := etagFor(info.Size(), info.ModTime())
	result := contract.FileContent{
		Path: clean, Size: info.Size(), Truncated: truncated, ETag: etag,
	}
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	for _, b := range probe {
		if b == 0 {
			result.Binary = true
			return result, nil
		}
	}
	result.Content = string(data)
	return result, nil
}

func etagFor(size int64, mtime time.Time) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%d-%d", size, mtime.UnixNano()))) //nolint:gosec
	return fmt.Sprintf("%x", sum)[:16]
}
