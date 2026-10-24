// Package watch provides recursive workspace change notifications with 500ms
// debounce semantics. It uses portable polling so wsd needs no extra module.
package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/0226zy/tenon/internal/wsd/bus"
	workspacefs "github.com/0226zy/tenon/internal/wsd/fs"
)

const maxWatches = 8192

type signature struct {
	size  int64
	mtime int64
	mode  os.FileMode
}

// Start blocks until ctx is cancelled or the watch bound is exceeded.
func Start(ctx context.Context, root string, ignore []string, debounce time.Duration, b *bus.Bus) error {
	if ignore == nil {
		ignore = workspacefs.DefaultIgnore
	}
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	previous, err := scan(root, ignore)
	if err != nil {
		return err
	}
	interval := debounce / 2
	if interval <= 0 || interval > 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	pending := make(map[string]struct{})
	lastChange := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			current, scanErr := scan(root, ignore)
			if scanErr != nil {
				if errors.Is(scanErr, errTooManyWatches) {
					b.Publish("notice", map[string]string{"message": "workspace watch limit exceeded"})
					return scanErr
				}
				continue
			}
			for _, changed := range diff(previous, current) {
				pending[changed] = struct{}{}
				lastChange = now
			}
			previous = current
			if len(pending) > 0 && !lastChange.IsZero() && now.Sub(lastChange) >= debounce {
				paths := make([]string, 0, len(pending))
				for p := range pending {
					paths = append(paths, p)
				}
				sort.Strings(paths)
				b.Publish("file_changed", map[string]any{"paths": paths})
				pending = make(map[string]struct{})
				lastChange = time.Time{}
			}
		}
	}
}

var errTooManyWatches = errors.New("workspace watch limit exceeded")

func scan(root string, ignore []string) (map[string]signature, error) {
	out := make(map[string]signature)
	err := filepath.WalkDir(root, func(p string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		isDir := entry.IsDir() && entry.Type()&os.ModeSymlink == 0
		if workspacefs.ShouldIgnore(rel, isDir, ignore) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		out[rel] = signature{size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
		if len(out) > maxWatches {
			return errTooManyWatches
		}
		return nil
	})
	return out, err
}

func diff(old, new map[string]signature) []string {
	changed := make([]string, 0)
	for p, oldSig := range old {
		newSig, ok := new[p]
		if !ok || oldSig != newSig {
			changed = append(changed, p)
		}
	}
	for p := range new {
		if _, ok := old[p]; !ok {
			changed = append(changed, p)
		}
	}
	return changed
}
