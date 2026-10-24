package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const eventColumns = `Fid,Fconv_id,Fseq,Frun_id,Fsource,Ftype,Fruntime_epoch,Fagent_session_id,Frt_seq,Fpayload,Fcreate_time`

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func scanEvent(row interface{ Scan(...any) error }) (*Event, error) {
	var e Event
	var epoch, session sql.NullString
	var seq sql.NullInt64
	err := row.Scan(&e.ID, &e.ConvID, &e.Seq, &e.RunID, &e.Source, &e.Type, &epoch, &session, &seq, &e.Payload, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.RtEpoch, e.RtSession = epoch.String, session.String
	if seq.Valid {
		n := int(seq.Int64)
		e.RtSeq = &n
	}
	return &e, nil
}
func (s *Store) appendEventTx(ctx context.Context, tx *Tx, e *Event, sourceOnce bool) (int64, bool, error) {
	if sourceOnce && e.RtSeq != nil {
		var existing int64
		err := tx.QueryRowContext(ctx, `SELECT Fseq FROM t_event WHERE Fruntime_epoch=? AND Fagent_session_id=? AND Frt_seq=?`, e.RtEpoch, e.RtSession, *e.RtSeq).Scan(&existing)
		if err == nil {
			return existing, false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
	}
	q := `SELECT Flast_seq FROM t_conversation WHERE Fconv_id=?`
	if s.dialect == "mysql" {
		q += ` FOR UPDATE`
	}
	var last int64
	if err := tx.QueryRowContext(ctx, q, e.ConvID).Scan(&last); err != nil {
		return 0, false, err
	}
	seq := last + 1
	now := time.Now().UTC()
	e.Seq, e.CreatedAt = seq, now
	res, err := tx.ExecContext(ctx, `INSERT INTO t_event(Fconv_id,Fseq,Frun_id,Fsource,Ftype,Fruntime_epoch,Fagent_session_id,Frt_seq,Fpayload,Fcreate_time) VALUES(?,?,?,?,?,?,?,?,?,?)`, e.ConvID, seq, e.RunID, e.Source, e.Type, nullableString(e.RtEpoch), nullableString(e.RtSession), e.RtSeq, e.Payload, now)
	if isDuplicate(err) {
		return 0, false, ErrDuplicate
	}
	if err != nil {
		return 0, false, err
	}
	e.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `UPDATE t_conversation SET Flast_seq=?,Flast_active_time=?,Fmodify_time=? WHERE Fconv_id=?`, seq, now, now, e.ConvID); err != nil {
		return 0, false, err
	}
	return seq, true, nil
}
func (s *Store) AppendEventTx(ctx context.Context, tx *Tx, e *Event) (int64, error) {
	seq, _, err := s.appendEventTx(ctx, tx, e, false)
	return seq, err
}
func (s *Store) AppendSourceOnceTx(ctx context.Context, tx *Tx, e *Event) (int64, bool, error) {
	return s.appendEventTx(ctx, tx, e, true)
}
func (s *Store) ListEventsAfter(ctx context.Context, convID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM t_event WHERE Fconv_id=? AND Fseq>? ORDER BY Fseq LIMIT ?`, convID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEvents(rows)
}
func (s *Store) ListEventsBefore(ctx context.Context, convID string, before int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	if before <= 0 {
		before = 1 << 62
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+eventColumns+` FROM t_event WHERE Fconv_id=? AND Fseq<? ORDER BY Fseq DESC LIMIT ?`, convID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events, err := collectEvents(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}
func collectEvents(rows *sql.Rows) ([]Event, error) {
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}
func (s *Store) HighWater(ctx context.Context, convID string) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT Flast_seq FROM t_conversation WHERE Fconv_id=?`, convID).Scan(&v)
	return v, err
}
func (s *Store) EventSeqByRunType(ctx context.Context, q Querier, runID, typ string) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT Fseq FROM t_event WHERE Frun_id=? AND Ftype=? ORDER BY Fseq LIMIT 1`, runID, typ).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return seq, err
}
func (s *Store) LatestAssistantContent(ctx context.Context, q Querier, convID, runID string) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT Fpayload FROM t_event WHERE Fconv_id=? AND Frun_id=? AND Fsource=0 AND Ftype='message' ORDER BY Fseq DESC`, convID, runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return "", err
		}
		text, ok := extractAssistant(raw)
		if ok && text != "" {
			return text, nil
		}
	}
	return "", rows.Err()
}
func extractAssistant(raw []byte) (string, bool) {
	// Avoid a store -> runtime JSON dependency beyond the stable wire fields.
	var v struct {
		Message *struct {
			Role, Content string
			ToolCalls     []any `json:"tool_calls"`
		} `json:"message"`
	}
	if err := jsonUnmarshal(raw, &v); err != nil || v.Message == nil {
		return "", false
	}
	return v.Message.Content, v.Message.Role == "assistant" && len(v.Message.ToolCalls) == 0
}

var jsonUnmarshal = func(data []byte, v any) error { return json.Unmarshal(data, v) }

func (s *Store) RequireEvents(convID string) error {
	if convID == "" {
		return fmt.Errorf("empty conversation")
	}
	return nil
}
