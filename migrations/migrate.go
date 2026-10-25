// Package migrations applies the versioned MySQL and SQLite control-plane DDL.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
)

//go:embed *.sql
var files embed.FS

func Migrate(ctx context.Context, db *sql.DB, dialect string) error {
	if dialect != "mysql" && dialect != "sqlite" {
		return fmt.Errorf("unsupported migration dialect %q", dialect)
	}
	if err := ensureMigrationTable(ctx, db, dialect); err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	names := make([]string, 0)
	suffix := "." + dialect + ".sql"
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var one int
		err := db.QueryRowContext(ctx, "SELECT 1 FROM t_schema_migration WHERE version=?", name).Scan(&one)
		if err == nil {
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		raw, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if err := apply(ctx, db, dialect, string(raw)); err != nil {
			return fmt.Errorf("migration %s（DDL 需要管理员账号先执行，应用账号只有 SELECT/INSERT/UPDATE）: %w", name, err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO t_schema_migration(version) VALUES(?)", name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}

// ensureMigrationTable 先探测表是否存在，避免对只有 DML 权限的账号执行 CREATE TABLE。
func ensureMigrationTable(ctx context.Context, db *sql.DB, dialect string) error {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM t_schema_migration").Scan(&n)
	if err == nil {
		return nil
	}
	if !isMissingTable(err) {
		return fmt.Errorf("inspect migration table: %w", err)
	}
	ddl := "CREATE TABLE IF NOT EXISTS t_schema_migration (version TEXT NOT NULL PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)"
	if dialect == "mysql" {
		ddl = "CREATE TABLE IF NOT EXISTS t_schema_migration (version VARCHAR(255) NOT NULL PRIMARY KEY, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create migration table（应用账号无 DDL 权限，请先用管理员账号建表并登记版本）: %w", err)
	}
	return nil
}

func isMissingTable(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1146
	}
	return strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func apply(ctx context.Context, db *sql.DB, dialect, script string) error {
	var tx *sql.Tx
	var err error
	if dialect == "sqlite" {
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
	}
	exec := func(q string) (sql.Result, error) {
		if tx != nil {
			return tx.ExecContext(ctx, q)
		}
		return db.ExecContext(ctx, q)
	}
	for _, stmt := range strings.Split(script, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := exec(stmt); err != nil {
			if tx != nil {
				_ = tx.Rollback()
			}
			return err
		}
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}
