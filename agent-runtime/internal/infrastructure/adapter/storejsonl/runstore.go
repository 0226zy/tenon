package storejsonl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/derr"
	"github.com/0226zy/proto-platform/agent-runtime/internal/domain/entity"
)

// FileRunStore 与 Session Store 分类型，避免两者不同的 List 契约相互污染。
type FileRunStore struct {
	dir   string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewRunStore(dataDir string) (*FileRunStore, error) {
	dir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileRunStore{dir: dir, locks: map[string]*sync.Mutex{}}, nil
}

func (s *FileRunStore) sessionDir(sid string) string { return filepath.Join(s.dir, sid) }
func (s *FileRunStore) lockFor(sid string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.locks[sid]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[sid] = lock
	}
	return lock
}

func (s *FileRunStore) runsDir(sid string) string { return filepath.Join(s.sessionDir(sid), "runs") }
func (s *FileRunStore) runPath(sid, runID string) string {
	return filepath.Join(s.runsDir(sid), runID+".json")
}

func validStoreID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// Put 原子持久化完整 RunRecord。
func (s *FileRunStore) Put(sid string, rec entity.RunRecord) error {
	if !validStoreID(sid) || !validStoreID(rec.RunID) {
		return fmt.Errorf("%w: 会话或 Run 标识非法", derr.ErrInvalidRun)
	}
	if rec.SessionID == "" {
		rec.SessionID = sid
	}
	if rec.SessionID != sid {
		return fmt.Errorf("%w: RunRecord.SessionID 与路径不一致", derr.ErrInvalidRun)
	}
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()
	return s.putRunLocked(sid, rec)
}

func (s *FileRunStore) putRunLocked(sid string, rec entity.RunRecord) error {
	if err := os.MkdirAll(s.runsDir(sid), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.runPath(sid, rec.RunID), raw, 0o600)
}

func (s *FileRunStore) Get(sid, runID string) (entity.RunRecord, error) {
	var rec entity.RunRecord
	if !validStoreID(sid) || !validStoreID(runID) {
		return rec, derr.ErrRunNotFound
	}
	raw, err := os.ReadFile(s.runPath(sid, runID))
	if err != nil {
		if os.IsNotExist(err) {
			return rec, fmt.Errorf("%w: %s/%s", derr.ErrRunNotFound, sid, runID)
		}
		return rec, err
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		return rec, fmt.Errorf("解析 Run 记录失败: %w", err)
	}
	return rec, nil
}

func (s *FileRunStore) List(sid string) ([]entity.RunRecord, error) {
	if !validStoreID(sid) {
		return nil, derr.ErrSessionNotFound
	}
	entries, err := os.ReadDir(s.runsDir(sid))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]entity.RunRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.runsDir(sid), entry.Name()))
		if err != nil {
			return nil, err
		}
		var rec entity.RunRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("解析 Run 记录 %s 失败: %w", entry.Name(), err)
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// ListActive 只返回需要重启收敛的 running/cancelling；prepared 刻意不纳入。
func (s *FileRunStore) ListActive() ([]entity.RunRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []entity.RunRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		records, err := s.List(entry.Name())
		if err != nil {
			return nil, err
		}
		for _, rec := range records {
			if rec.Status == entity.RunRunning || rec.Status == entity.RunCancelling {
				out = append(out, rec)
			}
		}
	}
	return out, nil
}

func (s *FileRunStore) MaxGeneration(sid string) (int64, error) {
	records, err := s.List(sid)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, rec := range records {
		if rec.Generation > max {
			max = rec.Generation
		}
	}
	return max, nil
}

// AddRunPGID 将 Bash 新建的进程组登记到最新 Run 记录，供崩溃后的 Recover 清理。
func (s *FileRunStore) AddRunPGID(sid, runID string, pgid int) error {
	if pgid <= 0 {
		return nil
	}
	lock := s.lockFor(sid)
	lock.Lock()
	defer lock.Unlock()
	rec, err := s.Get(sid, runID)
	if err != nil {
		return err
	}
	for _, existing := range rec.Pgids {
		if existing == pgid {
			return nil
		}
	}
	rec.Pgids = append(rec.Pgids, pgid)
	return s.putRunLocked(sid, rec)
}

// atomicWriteFile 在同目录写临时文件，fsync 后 rename，并同步目录项。
func atomicWriteFile(path string, data []byte, perm os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
