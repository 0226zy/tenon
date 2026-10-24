// Package git implements idempotent, run-bound workspace checkpoints.
package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/internal/wsd/bus"
	"github.com/0226zy/proto-platform/internal/wsd/gate"
	"github.com/0226zy/proto-platform/pkg/contract"
)

var validRunID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Service struct {
	mu          sync.Mutex
	root        string
	checkpoints string
	gate        *gate.Gate
	bus         *bus.Bus
}

func New(root, checkpoints string, g *gate.Gate, b *bus.Bus) *Service {
	return &Service{root: root, checkpoints: checkpoints, gate: g, bus: b}
}

func (s *Service) Commit(ctx context.Context, req contract.CommitRequest) (contract.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validRunID.MatchString(req.RunID) || s.gate == nil || !s.gate.HeldBy(req.RunID) {
		return contract.CommitResult{}, gate.ErrNotHolder
	}
	if result, ok, err := s.readCheckpoint(req.RunID); err != nil {
		return contract.CommitResult{}, err
	} else if ok {
		return result, nil
	}

	result, gitErr := s.commitOnce(ctx, req)
	if gitErr != nil {
		result.CheckpointStatus = "failed"
		result.Error = gitErr.Error()
		if result.Files == nil {
			result.Files = []contract.FileChange{}
		}
	}
	if err := s.writeCheckpoint(req.RunID, result); err != nil {
		return contract.CommitResult{}, fmt.Errorf("persist checkpoint: %w", err)
	}
	if gitErr == nil && s.bus != nil {
		s.bus.Publish("committed", map[string]any{
			"run_id": req.RunID, "commit": result.Commit, "changed": result.Changed,
		})
	}
	// A git failure is a completed checkpoint attempt, not an HTTP transport
	// failure. Its durable result is returned with checkpoint_status=failed.
	return result, nil
}

func (s *Service) commitOnce(ctx context.Context, req contract.CommitRequest) (contract.CommitResult, error) {
	result := contract.CommitResult{Files: []contract.FileChange{}, CheckpointStatus: "done"}
	if _, err := s.runGit(ctx, "add", "-A"); err != nil {
		return result, err
	}
	raw, err := s.runGit(ctx, "diff", "--cached", "--name-status", "-z", "--no-renames")
	if err != nil {
		return result, err
	}
	changes, err := parseNameStatus(raw)
	if err != nil {
		return result, err
	}
	result.FilesTotal = len(changes)
	if len(changes) > 50 {
		result.Files = append(result.Files, changes[:50]...)
	} else {
		result.Files = append(result.Files, changes...)
	}
	if len(changes) == 0 {
		head, err := s.runGit(ctx, "rev-parse", "HEAD")
		if err != nil {
			return result, err
		}
		result.Commit = strings.TrimSpace(head)
		return result, nil
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "Workspace checkpoint"
	}
	trailer := fmt.Sprintf("Run-Id: %s\nGeneration: %d", req.RunID, req.Generation)
	if _, err := s.runGit(ctx, "commit", "-m", message, "-m", trailer); err != nil {
		return result, err
	}
	head, err := s.runGit(ctx, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	result.Commit = strings.TrimSpace(head)
	result.Changed = true
	return result, nil
}

func (s *Service) Log(ctx context.Context, limit int) ([]contract.GitLogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	raw, err := s.runGit(ctx, "log", "--max-count="+strconv.Itoa(limit), "--pretty=format:%H%x00%s%x00%cI%x00%B%x1e")
	if err != nil {
		return nil, err
	}
	entries := make([]contract.GitLogEntry, 0)
	for _, record := range strings.Split(raw, "\x1e") {
		fields := strings.Split(strings.TrimSpace(record), "\x00")
		if len(fields) < 4 || fields[0] == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[2]))
		if err != nil {
			return nil, fmt.Errorf("parse git commit time: %w", err)
		}
		entry := contract.GitLogEntry{Commit: fields[0], Message: fields[1], Time: at}
		for _, line := range strings.Split(fields[3], "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "Run-Id:"); ok {
				entry.RunID = strings.TrimSpace(value)
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseNameStatus(raw string) ([]contract.FileChange, error) {
	if raw == "" {
		return []contract.FileChange{}, nil
	}
	parts := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	changes := make([]contract.FileChange, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		if i+1 >= len(parts) {
			return nil, errors.New("unexpected git name-status output")
		}
		status := parts[i]
		if status == "" || parts[i+1] == "" {
			continue
		}
		change := "M"
		switch status[0] {
		case 'A':
			change = "A"
		case 'D':
			change = "D"
		}
		changes = append(changes, contract.FileChange{Path: parts[i+1], Change: change})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func (s *Service) runGit(ctx context.Context, args ...string) (string, error) {
	output, err := s.runGitOnce(ctx, args...)
	if err == nil || !isIndexLockError(output) {
		return output, err
	}
	lock := filepath.Join(s.root, ".git", "index.lock")
	info, statErr := os.Stat(lock)
	if statErr != nil || time.Since(info.ModTime()) <= 60*time.Second {
		return output, err
	}
	// W9 permits one retry only after an index.lock is older than 60 seconds.
	if removeErr := os.Remove(lock); removeErr != nil {
		return output, err
	}
	return s.runGitOnce(ctx, args...)
}

func isIndexLockError(output string) bool {
	return strings.Contains(output, "index.lock")
}

func (s *Service) runGitOnce(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=C.UTF-8",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	output, err := cmd.CombinedOutput()
	text := string(output)
	if len(text) > 4096 {
		text = text[len(text)-4096:]
	}
	if err != nil {
		return text, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(text))
	}
	return text, nil
}

func (s *Service) checkpointPath(runID string) string {
	return filepath.Join(s.checkpoints, runID+".json")
}

func (s *Service) readCheckpoint(runID string) (contract.CommitResult, bool, error) {
	var result contract.CommitResult
	raw, err := os.ReadFile(s.checkpointPath(runID))
	if os.IsNotExist(err) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, false, fmt.Errorf("decode checkpoint: %w", err)
	}
	if result.Files == nil {
		result.Files = []contract.FileChange{}
	}
	return result, true, nil
}

func (s *Service) writeCheckpoint(runID string, result contract.CommitResult) error {
	if err := os.MkdirAll(s.checkpoints, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.checkpoints, ".checkpoint-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.checkpointPath(runID))
}
