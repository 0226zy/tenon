// Package store owns all control-plane SQL and transaction boundaries.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/migrations"
	"github.com/0226zy/proto-platform/pkg/contract"
	mysql "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrActiveRunExists = errors.New("active run exists")
	ErrDuplicate       = errors.New("duplicate")
	ErrConflict        = errors.New("conflict")
)

type Querier interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Tx struct {
	*sql.Tx
	mu    sync.Mutex
	after []func()
}

func (t *Tx) AfterCommit(fn func()) { t.mu.Lock(); t.after = append(t.after, fn); t.mu.Unlock() }

type Store struct {
	db      *sql.DB
	dialect string
}

func Open(ctx context.Context, dialect, dsn string) (*Store, error) {
	if dialect == "sqlite" {
		if err := os.MkdirAll(filepath.Dir(dsn), 0o700); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(dsn, "file:") {
			dsn = "file:" + dsn
		}
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	} else if dialect == "mysql" {
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			return nil, err
		}
		cfg.ParseTime = true
		dsn = cfg.FormatDSN()
	} else {
		return nil, fmt.Errorf("unknown database dialect %q", dialect)
	}
	db, err := sql.Open(dialect, dsn)
	if err != nil {
		return nil, err
	}
	if dialect == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(50)
		db.SetMaxIdleConns(10)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrations.Migrate(ctx, db, dialect); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dialect: dialect}, nil
}
func (s *Store) DB() *sql.DB     { return s.db }
func (s *Store) Dialect() string { return s.dialect }
func (s *Store) Close() error    { return s.db.Close() }
func (s *Store) WithTx(ctx context.Context, fn func(*Tx) error) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return err
		}
		t := &Tx{Tx: tx}
		err = fn(t)
		if err != nil {
			_ = tx.Rollback()
			last = err
		} else if err = tx.Commit(); err != nil {
			last = err
		} else {
			t.mu.Lock()
			callbacks := append([]func(){}, t.after...)
			t.mu.Unlock()
			for _, cb := range callbacks {
				cb()
			}
			return nil
		}
		if !retryable(last) {
			return last
		}
	}
	return last
}
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1213 {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") || strings.Contains(text, "database table is locked")
}
func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "duplicate entry")
}

type User struct {
	ID                    int64
	UserID, Name, PwdHash string
	Status, MaxConv       int
	CreatedAt, ModifiedAt time.Time
}
type AuthSession struct {
	Hash, UserID         string
	ExpiresAt, CreatedAt time.Time
}
type Conversation struct {
	ID                                                 int64
	ConvID, UserID, Title, Model, Template, PreviewKey string
	AgentSessionID, RuntimeEpoch                       string
	RtNextSeq                                          int
	LastSeq, ExecGeneration                            int64
	State                                              int
	LastActiveAt, CreatedAt, ModifiedAt                time.Time
}
type Message struct {
	ID                   int64
	MsgID, ConvID, RunID string
	Role                 int
	Content              string
	CreatedAt            time.Time
}
type Run struct {
	ID                                                          int64
	RunID, ConvID, UserID, RequestID, InputHash, Model          string
	Status                                                      contract.RunStatus
	Active                                                      bool
	Generation                                                  int64
	CancelReq, StopConfirmed                                    bool
	CheckpointStatus                                            int
	Commit                                                      string
	RtSeqStart, RtSeqEnd, Iterations, PromptTokens, ComplTokens int
	ErrorCode, ErrorMessage                                     string
	StartedAt, EndedAt                                          *time.Time
	CreatedAt, ModifiedAt                                       time.Time
}
type Event struct {
	ID                 int64
	ConvID             string
	Seq                int64
	RunID              string
	Source             int
	Type               string
	RtEpoch, RtSession string
	RtSeq              *int
	Payload            []byte
	CreatedAt          time.Time
}
type Sandbox struct {
	ID                                                    int64
	ConvID, InstID                                        string
	Status                                                int
	ContainerID, VolumeName, ImageVer, NetAddr, TokenHash string
	TokenExpiresAt                                        *time.Time
	PreviewStatus                                         int
	PreviewGeneration                                     int64
	PreviewCommit                                         string
	LastActiveAt, CreatedAt, ModifiedAt                   time.Time
}
type UsageRow struct {
	ConvID, RunID, UserID, Model                  string
	PromptTokens, ComplTokens, CostMS, HTTPStatus int
	CreatedAt                                     time.Time
}
type UsageAgg struct {
	UserID, Model                       string
	PromptTokens, ComplTokens, Requests int
}

func CheckpointName(v int) string {
	switch v {
	case 1:
		return "done"
	case 2:
		return "failed"
	default:
		return "pending"
	}
}
func CheckpointCode(v string) int {
	switch v {
	case "done":
		return 1
	case "failed":
		return 2
	default:
		return 0
	}
}
func SandboxStatusName(v int) string {
	switch v {
	case 1:
		return "creating"
	case 2:
		return "running"
	case 3:
		return "stopped"
	case 4:
		return "error"
	case 5:
		return "removed"
	default:
		return "absent"
	}
}
func SandboxStatusCode(v string) int {
	switch v {
	case "creating":
		return 1
	case "running":
		return 2
	case "stopped":
		return 3
	case "error":
		return 4
	case "removed":
		return 5
	default:
		return 0
	}
}
func PreviewStatusName(v int) string {
	switch v {
	case 1:
		return "starting"
	case 2:
		return "ready"
	case 3:
		return "failed"
	default:
		return "none"
	}
}
func PreviewStatusCode(v string) int {
	switch v {
	case "starting":
		return 1
	case "ready":
		return 2
	case "failed":
		return 3
	default:
		return 0
	}
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
func scanTime(v *time.Time, dest **time.Time) {
	if v == nil || v.IsZero() {
		*dest = nil
		return
	}
	x := v.UTC()
	*dest = &x
}
func parseCursor(s string) (time.Time, string, error) {
	if s == "" {
		return time.Time{}, "", nil
	}
	parts := strings.SplitN(s, "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return t, parts[1], nil
}
func encodeCursor(t time.Time, id string) string {
	// JSON 返回原始不透明游标；HTTP 客户端在放入查询参数时编码一次。
	return t.UTC().Format(time.RFC3339Nano) + "|" + id
}
