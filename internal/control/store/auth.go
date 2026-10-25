package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) CreateUser(ctx context.Context, q Querier, u *User) error {
	now := time.Now().UTC()
	u.CreatedAt, u.ModifiedAt = now, now
	res, err := q.ExecContext(ctx, `INSERT INTO t_user(Fuser_id,Fname,Fpwd_hash,Fstatus,Fmax_conv,Fcreate_time,Fmodify_time) VALUES(?,?,?,?,?,?,?)`, u.UserID, u.Name, u.PwdHash, u.Status, u.MaxConv, now, now)
	if isDuplicate(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}
func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.UserID, &u.Name, &u.PwdHash, &u.Status, &u.MaxConv, &u.CreatedAt, &u.ModifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

const userColumns = `Fid,Fuser_id,Fname,Fpwd_hash,Fstatus,Fmax_conv,Fcreate_time,Fmodify_time`

func (s *Store) User(ctx context.Context, q Querier, userID string) (*User, error) {
	return scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM t_user WHERE Fuser_id=?`, userID))
}
func (s *Store) LockUser(ctx context.Context, tx *Tx, userID string) (*User, error) {
	query := `SELECT ` + userColumns + ` FROM t_user WHERE Fuser_id=?`
	if s.dialect == "mysql" {
		query += ` FOR UPDATE`
	}
	return scanUser(tx.QueryRowContext(ctx, query, userID))
}
func (s *Store) SetUserPassword(ctx context.Context, userID, hash string) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE t_user SET Fpwd_hash=?,Fmodify_time=? WHERE Fuser_id=?`, hash, time.Now().UTC(), userID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		// 受限账号没有 DELETE 权限：把会话过期时间提前即可使其失效。
		_, err = tx.ExecContext(ctx, `UPDATE t_auth_session SET Fexpire_time=? WHERE Fuser_id=?`, time.Now().UTC(), userID)
		return err
	})
}
func (s *Store) DisableUser(ctx context.Context, userID string) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE t_user SET Fstatus=2,Fmodify_time=? WHERE Fuser_id=?`, time.Now().UTC(), userID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE t_auth_session SET Fexpire_time=? WHERE Fuser_id=?`, time.Now().UTC(), userID)
		return err
	})
}
func (s *Store) PutSession(ctx context.Context, q Querier, v *AuthSession) error {
	v.CreatedAt = time.Now().UTC()
	_, err := q.ExecContext(ctx, `INSERT INTO t_auth_session(Fsession_hash,Fuser_id,Fexpire_time,Fcreate_time) VALUES(?,?,?,?)`, v.Hash, v.UserID, v.ExpiresAt, v.CreatedAt)
	return err
}
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE t_auth_session SET Fexpire_time=? WHERE Fsession_hash=?`, time.Now().UTC(), hash)
	return err
}
func (s *Store) UserBySession(ctx context.Context, hash string, now time.Time) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT u.`+`Fid,u.Fuser_id,u.Fname,u.Fpwd_hash,u.Fstatus,u.Fmax_conv,u.Fcreate_time,u.Fmodify_time FROM t_auth_session a JOIN t_user u ON u.Fuser_id=a.Fuser_id WHERE a.Fsession_hash=? AND a.Fexpire_time>? AND u.Fstatus=1`, hash, now))
}
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	if s.dialect == "mysql" {
		// 应用账号只有 SELECT/INSERT/UPDATE：过期会话行保留但不再通过 UserBySession 校验。
		// 需要物理清理时用管理员账号执行：DELETE FROM t_auth_session WHERE Fexpire_time<=UTC_TIMESTAMP()。
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM t_auth_session WHERE Fexpire_time<=?`, now)
	return err
}
