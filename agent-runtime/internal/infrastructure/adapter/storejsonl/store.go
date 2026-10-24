// Package storejsonl 是 append-only 会话日志的磁盘实现（dsh State 层的等价物）。
//
// 布局：
//
//	<data_dir>/sessions/<id>/header.json   会话元数据（存储关注点）
//	<data_dir>/sessions/<id>/events.jsonl  事件日志（一行一个 JSON，只追加）
//
// 不变量（对齐 dsh 的 seq = log.length 相邻性契约）：
//   - 事件 seq 连续，等于追加前的日志长度；
//   - 日志只追加，fork 通过「复制前缀 + 血缘 header + fork marker」实现，
//     绝不改写已有事件；撤销语义由上层以追加新事件表达。
package storejsonl

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/tenon/agent-runtime/internal/domain/derr"
	"github.com/0226zy/tenon/agent-runtime/internal/domain/entity"
)

const (
	fsyncBatchEvents = 64
	fsyncBatchDelay  = 200 * time.Millisecond
)

type sessionLog struct {
	file    *os.File
	pending int
	timer   *time.Timer
	syncErr error
	closed  bool
}

// Store 实现 port.SessionStorePort。
type Store struct {
	dir string

	mu     sync.Mutex // 保护 counters、logs 与关闭状态
	seqs   map[string]int
	locks  map[string]*sync.Mutex // 每会话一把写锁
	logs   map[string]*sessionLog
	closed bool
}

// New 构造存储，必要时创建根目录。
func New(dataDir string) (*Store, error) {
	dir := filepath.Join(dataDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建会话存储目录失败: %w", err)
	}
	return &Store{dir: dir, seqs: map[string]int{}, locks: map[string]*sync.Mutex{}, logs: map[string]*sessionLog{}}, nil
}

func (s *Store) sessionDir(id string) string { return filepath.Join(s.dir, id) }
func (s *Store) headerPath(id string) string { return filepath.Join(s.sessionDir(id), "header.json") }
func (s *Store) eventsPath(id string) string { return filepath.Join(s.sessionDir(id), "events.jsonl") }

func (s *Store) lockFor(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[id]
	if !ok {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

// Create 原子落盘 header，并写入 created marker 作为第 0 号事件。
func (s *Store) Create(header entity.SessionHeader) error {
	if header.ID == "" {
		return fmt.Errorf("会话 ID 不能为空")
	}
	if header.FormatVersion == 0 {
		header.FormatVersion = entity.SessionFormatVersion
	}
	if header.CreatedAt.IsZero() {
		header.CreatedAt = time.Now()
	}
	if header.Epoch == "" {
		epoch, err := newSessionEpoch()
		if err != nil {
			return err
		}
		header.Epoch = epoch
	}
	if err := os.MkdirAll(s.sessionDir(header.ID), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWriteFile(s.headerPath(header.ID), raw, 0o600); err != nil {
		return err
	}
	if header.InheritedEventCount == 0 {
		// 全新会话：写 created marker。fork 会话由 Fork 负责铺好前缀与 marker。
		_, err = s.Append(header.ID, entity.Event{
			Kind:   entity.EventMarker,
			Marker: &entity.Marker{Type: "created", Detail: "session created"},
		})
		return err
	}
	return nil
}

func newSessionEpoch() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成会话 Epoch 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Append 追加一条事件：分配连续 seq、打时间戳、写盘后再返回。
// 每 64 条或首批写入后 200ms fsync；Close 会同步并关闭全部日志。
func (s *Store) Append(sessionID string, ev entity.Event) (entity.Event, error) {
	l := s.lockFor(sessionID)
	l.Lock()
	defer l.Unlock()

	next, err := s.nextSeq(sessionID)
	if err != nil {
		return entity.Event{}, err
	}
	ev.Seq = next
	ev.TS = time.Now()

	raw, err := json.Marshal(ev)
	if err != nil {
		return entity.Event{}, fmt.Errorf("编码事件失败: %w", err)
	}
	log, err := s.logFor(sessionID)
	if err != nil {
		return entity.Event{}, err
	}
	if log.syncErr != nil {
		return entity.Event{}, log.syncErr
	}
	if _, err := log.file.Write(append(raw, '\n')); err != nil {
		return entity.Event{}, fmt.Errorf("追加事件失败: %w", err)
	}
	log.pending++
	s.mu.Lock()
	s.seqs[sessionID] = next + 1
	s.mu.Unlock()
	if log.pending >= fsyncBatchEvents {
		if log.timer != nil {
			log.timer.Stop()
			log.timer = nil
		}
		if err := s.syncLogLocked(log); err != nil {
			return ev, err
		}
	} else if log.timer == nil {
		log.timer = time.AfterFunc(fsyncBatchDelay, func() { s.syncSession(sessionID, log) })
	}
	return ev, nil
}

// logFor 由会话写锁保护调用，保证每个会话只有一个追加文件与定时器。
func (s *Store) logFor(sessionID string) (*sessionLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("会话存储已关闭: %w", os.ErrClosed)
	}
	if log := s.logs[sessionID]; log != nil {
		return log, nil
	}
	file, err := os.OpenFile(s.eventsPath(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	log := &sessionLog{file: file}
	s.logs[sessionID] = log
	return log, nil
}

func (s *Store) syncSession(sessionID string, log *sessionLog) {
	l := s.lockFor(sessionID)
	l.Lock()
	defer l.Unlock()
	if log.closed {
		return
	}
	log.timer = nil
	_ = s.syncLogLocked(log) // 异步错误由下一次 Append 或 Close 返回。
}

// syncLogLocked 由同一会话的写锁保护调用。
func (s *Store) syncLogLocked(log *sessionLog) error {
	if log.pending == 0 {
		return nil
	}
	if err := log.file.Sync(); err != nil {
		log.syncErr = fmt.Errorf("同步会话事件日志失败: %w", err)
		return log.syncErr
	}
	log.pending = 0
	log.syncErr = nil
	return nil
}

// Close 同步并关闭全部事件日志；关闭后拒绝新的 Append。
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	logs := make(map[string]*sessionLog, len(s.logs))
	for id, log := range s.logs {
		logs[id] = log
	}
	s.mu.Unlock()

	var firstErr error
	for id, log := range logs {
		l := s.lockFor(id)
		l.Lock()
		if log.timer != nil {
			log.timer.Stop()
			log.timer = nil
		}
		if err := s.syncLogLocked(log); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := log.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		log.closed = true
		l.Unlock()
	}
	return firstErr
}

func (s *Store) nextSeq(sessionID string) (int, error) {
	s.mu.Lock()
	n, ok := s.seqs[sessionID]
	s.mu.Unlock()
	if ok {
		return n, nil
	}
	_, events, err := s.Load(sessionID)
	if err != nil {
		return 0, err
	}
	for _, ev := range events {
		if ev.Seq >= n {
			n = ev.Seq + 1
		}
	}
	s.mu.Lock()
	s.seqs[sessionID] = n
	s.mu.Unlock()
	return n, nil
}

// Load 读取 header 与完整事件日志。逐行解析：单行损坏不拖垮整个会话，
// 而是返回已恢复的前缀与第一个错误（指引 §4.1 上下文层故障的专门修复路径基础）。
func (s *Store) Load(sessionID string) (entity.SessionHeader, []entity.Event, error) {
	var header entity.SessionHeader
	raw, err := os.ReadFile(s.headerPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return header, nil, derr.ErrSessionNotFound
		}
		return header, nil, err
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return header, nil, fmt.Errorf("解析会话 header 失败: %w", err)
	}

	f, err := os.Open(s.eventsPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return header, nil, nil
		}
		return header, nil, err
	}
	defer f.Close()

	var events []entity.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // 工具结果可能很大
	line := 0
	for sc.Scan() {
		line++
		var ev entity.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return header, events, fmt.Errorf("会话 %s 事件日志第 %d 行损坏: %w", sessionID, line, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return header, events, err
	}
	return header, events, nil
}

// UpdateHeader 原子覆写会话 header（存储元数据，不触碰事件日志）。
func (s *Store) UpdateHeader(header entity.SessionHeader) error {
	if _, err := os.Stat(s.headerPath(header.ID)); err != nil {
		if os.IsNotExist(err) {
			return derr.ErrSessionNotFound
		}
		return err
	}
	raw, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(s.headerPath(header.ID), raw, 0o600)
}

// List 返回全部会话 header，按创建时间倒序。
func (s *Store) List() ([]entity.SessionHeader, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []entity.SessionHeader
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(s.headerPath(e.Name()))
		if err != nil {
			continue // 残破目录跳过，不影响列表
		}
		var h entity.SessionHeader
		if json.Unmarshal(raw, &h) == nil {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Fork 从 parentID 的前 atSeq 条事件派生新会话：
// 复制前缀事件（保持原 seq）、原子写入带血缘的 header、追加 forked marker。
// atSeq < 0 表示在父日志当前末尾分叉；子会话始终获得新的 Epoch。
func (s *Store) Fork(parentID, newID string, atSeq int) (entity.SessionHeader, error) {
	if newID == "" || newID == parentID {
		return entity.SessionHeader{}, fmt.Errorf("fork 子会话 ID 必须非空且不同于父会话")
	}
	parentLock := s.lockFor(parentID)
	parentLock.Lock()
	defer parentLock.Unlock()
	parentHeader, events, err := s.Load(parentID)
	if err != nil {
		return entity.SessionHeader{}, err
	}
	if atSeq < 0 {
		atSeq = len(events)
	}
	if atSeq > len(events) {
		return entity.SessionHeader{}, fmt.Errorf("%w: 切点 %d 超出父会话日志长度 %d", derr.ErrForkPointInvalid, atSeq, len(events))
	}
	epoch, err := newSessionEpoch()
	if err != nil {
		return entity.SessionHeader{}, err
	}
	header := entity.SessionHeader{
		FormatVersion:       entity.SessionFormatVersion,
		ID:                  newID,
		Title:               parentHeader.Title + " (fork)",
		CreatedAt:           time.Now(),
		Cwd:                 parentHeader.Cwd,
		Mode:                parentHeader.Mode,
		Model:               parentHeader.Model,
		ParentID:            parentID,
		Epoch:               epoch,
		InheritedEventCount: atSeq,
	}
	if err := os.MkdirAll(s.sessionDir(newID), 0o755); err != nil {
		return header, err
	}
	raw, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return header, err
	}
	if err := atomicWriteFile(s.headerPath(newID), raw, 0o600); err != nil {
		return header, err
	}

	// 复制继承前缀（不改写事件本身，seq 保持原值）。
	var b strings.Builder
	for _, ev := range events[:atSeq] {
		line, err := json.Marshal(ev)
		if err != nil {
			return header, err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := atomicWriteFile(s.eventsPath(newID), []byte(b.String()), 0o600); err != nil {
		return header, err
	}
	s.mu.Lock()
	s.seqs[newID] = atSeq
	s.mu.Unlock()

	_, err = s.Append(newID, entity.Event{
		Kind: entity.EventMarker,
		Marker: &entity.Marker{
			Type:     "forked",
			ParentID: parentID,
			AtSeq:    atSeq,
			Detail:   fmt.Sprintf("forked from %s at seq %d", parentID, atSeq),
		},
	})
	return header, err
}
