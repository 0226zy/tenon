package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const sandboxColumns = `Fid,Fconv_id,Finst_id,Fstatus,Fcontainer_id,Fvolume_name,Fimage_ver,Fnet_addr,Ftoken_hash,Ftoken_expire,Fpreview_status,Fpreview_gen,Fpreview_commit,Flast_active_time,Fcreate_time,Fmodify_time`

func scanSandbox(row interface{ Scan(...any) error }) (*Sandbox, error) {
	var b Sandbox
	var exp sql.NullTime
	err := row.Scan(&b.ID, &b.ConvID, &b.InstID, &b.Status, &b.ContainerID, &b.VolumeName, &b.ImageVer, &b.NetAddr, &b.TokenHash, &exp, &b.PreviewStatus, &b.PreviewGeneration, &b.PreviewCommit, &b.LastActiveAt, &b.CreatedAt, &b.ModifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if exp.Valid {
		t := exp.Time.UTC()
		b.TokenExpiresAt = &t
	}
	return &b, nil
}
func (s *Store) Sandbox(ctx context.Context, q Querier, convID string) (*Sandbox, error) {
	return scanSandbox(q.QueryRowContext(ctx, `SELECT `+sandboxColumns+` FROM t_sandbox WHERE Fconv_id=?`, convID))
}
func (s *Store) SandboxByToken(ctx context.Context, hash string) (*Sandbox, error) {
	return scanSandbox(s.db.QueryRowContext(ctx, `SELECT `+sandboxColumns+` FROM t_sandbox WHERE Ftoken_hash=? AND Fstatus IN (1,2)`, hash))
}
func (s *Store) PutSandbox(ctx context.Context, q Querier, b *Sandbox) error {
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.ModifiedAt = now
	if b.LastActiveAt.IsZero() {
		b.LastActiveAt = now
	}
	query := `INSERT INTO t_sandbox(Fconv_id,Finst_id,Fstatus,Fcontainer_id,Fvolume_name,Fimage_ver,Fnet_addr,Ftoken_hash,Ftoken_expire,Fpreview_status,Fpreview_gen,Fpreview_commit,Flast_active_time,Fcreate_time,Fmodify_time) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if s.dialect == "mysql" {
		query += ` ON DUPLICATE KEY UPDATE Finst_id=VALUES(Finst_id),Fstatus=VALUES(Fstatus),Fcontainer_id=VALUES(Fcontainer_id),Fvolume_name=VALUES(Fvolume_name),Fimage_ver=VALUES(Fimage_ver),Fnet_addr=VALUES(Fnet_addr),Ftoken_hash=VALUES(Ftoken_hash),Ftoken_expire=VALUES(Ftoken_expire),Fpreview_status=VALUES(Fpreview_status),Fpreview_gen=VALUES(Fpreview_gen),Fpreview_commit=VALUES(Fpreview_commit),Flast_active_time=VALUES(Flast_active_time),Fmodify_time=VALUES(Fmodify_time)`
	} else {
		query += ` ON CONFLICT(Fconv_id) DO UPDATE SET Finst_id=excluded.Finst_id,Fstatus=excluded.Fstatus,Fcontainer_id=excluded.Fcontainer_id,Fvolume_name=excluded.Fvolume_name,Fimage_ver=excluded.Fimage_ver,Fnet_addr=excluded.Fnet_addr,Ftoken_hash=excluded.Ftoken_hash,Ftoken_expire=excluded.Ftoken_expire,Fpreview_status=excluded.Fpreview_status,Fpreview_gen=excluded.Fpreview_gen,Fpreview_commit=excluded.Fpreview_commit,Flast_active_time=excluded.Flast_active_time,Fmodify_time=excluded.Fmodify_time`
	}
	_, err := q.ExecContext(ctx, query, b.ConvID, b.InstID, b.Status, b.ContainerID, b.VolumeName, b.ImageVer, b.NetAddr, b.TokenHash, nullTime(b.TokenExpiresAt), b.PreviewStatus, b.PreviewGeneration, b.PreviewCommit, b.LastActiveAt, b.CreatedAt, b.ModifiedAt)
	return err
}
func (s *Store) UpdateSandboxStatus(ctx context.Context, q Querier, convID string, status int, addr, containerID string) error {
	_, err := q.ExecContext(ctx, `UPDATE t_sandbox SET Fstatus=?,Fnet_addr=?,Fcontainer_id=?,Fmodify_time=? WHERE Fconv_id=?`, status, addr, containerID, time.Now().UTC(), convID)
	return err
}
func (s *Store) RevokeSandbox(ctx context.Context, q Querier, convID string, status int) error {
	_, err := q.ExecContext(ctx, `UPDATE t_sandbox SET Fstatus=?,Ftoken_hash='',Ftoken_expire=NULL,Fmodify_time=? WHERE Fconv_id=?`, status, time.Now().UTC(), convID)
	return err
}
func (s *Store) TouchSandbox(ctx context.Context, convID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE t_sandbox SET Flast_active_time=? WHERE Fconv_id=?`, time.Now().UTC(), convID)
	return err
}
func (s *Store) UpdateSandboxPreview(ctx context.Context, q Querier, convID, status, commit string, generation int64) error {
	_, err := q.ExecContext(ctx, `UPDATE t_sandbox SET Fpreview_status=?,Fpreview_commit=?,Fpreview_gen=?,Fmodify_time=? WHERE Fconv_id=? AND Fpreview_gen<=?`, PreviewStatusCode(status), commit, generation, time.Now().UTC(), convID, generation)
	return err
}
func (s *Store) SandboxCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT Fstatus,COUNT(*) FROM t_sandbox GROUP BY Fstatus`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status, n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[SandboxStatusName(status)] = n
	}
	return out, rows.Err()
}
func (s *Store) ListSandboxes(ctx context.Context) ([]Sandbox, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sandboxColumns+` FROM t_sandbox`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sandbox{}
	for rows.Next() {
		b, err := scanSandbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}
