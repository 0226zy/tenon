package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const convColumns = `Fid,Fconv_id,Fuser_id,Ftitle,Fmodel,Ftemplate,Fpreview_key,Fagent_session_id,Fruntime_epoch,Frt_next_seq,Flast_seq,Fexec_generation,Flstate,Flast_active_time,Fcreate_time,Fmodify_time`

func scanConversation(row interface{ Scan(...any) error }) (*Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.ConvID, &c.UserID, &c.Title, &c.Model, &c.Template, &c.PreviewKey, &c.AgentSessionID, &c.RuntimeEpoch, &c.RtNextSeq, &c.LastSeq, &c.ExecGeneration, &c.State, &c.LastActiveAt, &c.CreatedAt, &c.ModifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}
func (s *Store) CreateConversation(ctx context.Context, q Querier, c *Conversation) error {
	now := time.Now().UTC()
	c.CreatedAt, c.ModifiedAt, c.LastActiveAt = now, now, now
	c.State = 1
	res, err := q.ExecContext(ctx, `INSERT INTO t_conversation(Fconv_id,Fuser_id,Ftitle,Fmodel,Ftemplate,Fpreview_key,Fagent_session_id,Fruntime_epoch,Frt_next_seq,Flast_seq,Fexec_generation,Flstate,Flast_active_time,Fcreate_time,Fmodify_time) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, c.ConvID, c.UserID, c.Title, c.Model, c.Template, c.PreviewKey, c.AgentSessionID, c.RuntimeEpoch, c.RtNextSeq, c.LastSeq, c.ExecGeneration, c.State, c.LastActiveAt, c.CreatedAt, c.ModifiedAt)
	if isDuplicate(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}
func (s *Store) ConversationOwned(ctx context.Context, q Querier, userID, convID string) (*Conversation, error) {
	return scanConversation(q.QueryRowContext(ctx, `SELECT `+convColumns+` FROM t_conversation WHERE Fuser_id=? AND Fconv_id=? AND Flstate=1`, userID, convID))
}
func (s *Store) Conversation(ctx context.Context, q Querier, convID string) (*Conversation, error) {
	return scanConversation(q.QueryRowContext(ctx, `SELECT `+convColumns+` FROM t_conversation WHERE Fconv_id=?`, convID))
}
func (s *Store) ConversationByPreviewKey(ctx context.Context, key string) (*Conversation, error) {
	return scanConversation(s.db.QueryRowContext(ctx, `SELECT `+convColumns+` FROM t_conversation WHERE Fpreview_key=? AND Flstate=1`, key))
}
func (s *Store) LockConversation(ctx context.Context, tx *Tx, userID, convID string) (*Conversation, error) {
	q := `SELECT ` + convColumns + ` FROM t_conversation WHERE Fuser_id=? AND Fconv_id=? AND Flstate=1`
	if s.dialect == "mysql" {
		q += ` FOR UPDATE`
	}
	return scanConversation(tx.QueryRowContext(ctx, q, userID, convID))
}
func (s *Store) CountConversations(ctx context.Context, q Querier, userID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_conversation WHERE Fuser_id=? AND Flstate=1`, userID).Scan(&n)
	return n, err
}
func (s *Store) ListConversations(ctx context.Context, userID, cursor string, limit int) ([]Conversation, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	t, id, err := parseCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	args := []any{userID}
	where := `Fuser_id=? AND Flstate=1`
	if cursor != "" {
		where += ` AND (Flast_active_time<? OR (Flast_active_time=? AND Fconv_id<?))`
		args = append(args, t, t, id)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT `+convColumns+` FROM t_conversation WHERE `+where+` ORDER BY Flast_active_time DESC,Fconv_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]Conversation, 0, limit)
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.LastActiveAt, last.ConvID)
	}
	return out, next, nil
}
func (s *Store) UpdateConversation(ctx context.Context, q Querier, convID string, title, model *string, lastActive *time.Time) error {
	set := `Fmodify_time=?`
	args := []any{time.Now().UTC()}
	if title != nil {
		set += `,Ftitle=?`
		args = append(args, *title)
	}
	if model != nil {
		set += `,Fmodel=?`
		args = append(args, *model)
	}
	if lastActive != nil {
		set += `,Flast_active_time=?`
		args = append(args, lastActive.UTC())
	}
	args = append(args, convID)
	_, err := q.ExecContext(ctx, `UPDATE t_conversation SET `+set+` WHERE Fconv_id=?`, args...)
	return err
}
func (s *Store) DeleteConversation(ctx context.Context, q Querier, convID string) error {
	_, err := q.ExecContext(ctx, `UPDATE t_conversation SET Flstate=2,Fmodify_time=? WHERE Fconv_id=? AND Flstate=1`, time.Now().UTC(), convID)
	return err
}
func (s *Store) SetRuntimeSession(ctx context.Context, q Querier, convID, sid, epoch string, next int) error {
	_, err := q.ExecContext(ctx, `UPDATE t_conversation SET Fagent_session_id=?,Fruntime_epoch=?,Frt_next_seq=?,Fmodify_time=? WHERE Fconv_id=?`, sid, epoch, next, time.Now().UTC(), convID)
	return err
}
func (s *Store) AdvanceRuntimeCursor(ctx context.Context, q Querier, convID, epoch string, next int) error {
	_, err := q.ExecContext(ctx, `UPDATE t_conversation SET Fruntime_epoch=?,Frt_next_seq=?,Fmodify_time=? WHERE Fconv_id=?`, epoch, next, time.Now().UTC(), convID)
	return err
}
func (s *Store) NextGeneration(ctx context.Context, q Querier, convID string) (int64, error) {
	if _, err := q.ExecContext(ctx, `UPDATE t_conversation SET Fexec_generation=Fexec_generation+1,Fmodify_time=? WHERE Fconv_id=?`, time.Now().UTC(), convID); err != nil {
		return 0, err
	}
	var gen int64
	err := q.QueryRowContext(ctx, `SELECT Fexec_generation FROM t_conversation WHERE Fconv_id=?`, convID).Scan(&gen)
	return gen, err
}
func (s *Store) CreateMessage(ctx context.Context, q Querier, m *Message) error {
	m.CreatedAt = time.Now().UTC()
	res, err := q.ExecContext(ctx, `INSERT INTO t_message(Fmsg_id,Fconv_id,Frun_id,Frole,Fcontent,Fcreate_time) VALUES(?,?,?,?,?,?)`, m.MsgID, m.ConvID, m.RunID, m.Role, m.Content, m.CreatedAt)
	if isDuplicate(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}
func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.MsgID, &m.ConvID, &m.RunID, &m.Role, &m.Content, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &m, err
}
func (s *Store) MessageByRunRole(ctx context.Context, q Querier, runID string, role int) (*Message, error) {
	return scanMessage(q.QueryRowContext(ctx, `SELECT Fid,Fmsg_id,Fconv_id,Frun_id,Frole,Fcontent,Fcreate_time FROM t_message WHERE Frun_id=? AND Frole=? ORDER BY Fid LIMIT 1`, runID, role))
}
func (s *Store) ListMessages(ctx context.Context, convID, before string, limit int) ([]Message, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	args := []any{convID}
	where := `Fconv_id=?`
	if before != "" {
		var id int64
		if _, err := fmt.Sscan(before, &id); err != nil {
			return nil, "", fmt.Errorf("invalid before cursor")
		}
		where += ` AND Fid<?`
		args = append(args, id)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT Fid,Fmsg_id,Fconv_id,Frun_id,Frole,Fcontent,Fcreate_time FROM t_message WHERE `+where+` ORDER BY Fid DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]Message, 0, limit)
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *m)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = fmt.Sprint(out[len(out)-1].ID)
	}
	return out, next, rows.Err()
}
