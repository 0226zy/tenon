package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/0226zy/protoforge/pkg/contract"
)

const runColumns = `Fid,Frun_id,Fconv_id,Fuser_id,Frequest_id,Finput_hash,Fmodel,Fstatus,Factive,Fgeneration,Fcancel_req,Fstop_confirmed,Fcheckpoint_status,Fcommit,Frt_seq_start,Frt_seq_end,Fiterations,Fprompt_tokens,Fcompl_tokens,Ferror_code,Ferror_msg,Fstart_time,Fend_time,Fcreate_time,Fmodify_time`

func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var status int
	var active, start, end sql.NullInt64
	err := row.Scan(&r.ID, &r.RunID, &r.ConvID, &r.UserID, &r.RequestID, &r.InputHash, &r.Model, &status, &active, &r.Generation, &r.CancelReq, &r.StopConfirmed, &r.CheckpointStatus, &r.Commit, &r.RtSeqStart, &r.RtSeqEnd, &r.Iterations, &r.PromptTokens, &r.ComplTokens, &r.ErrorCode, &r.ErrorMessage, &start, &end, &r.CreatedAt, &r.ModifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = contract.RunStatusFromCode(status)
	r.Active = active.Valid && active.Int64 == 1
	if start.Valid {
		t := time.Unix(0, start.Int64)
		_ = t
	} // overwritten by scanRunTimes below for drivers that return DATETIME
	return &r, nil
}

// runScanner is separate because database/sql scans DATETIME into time.Time (or
// sql.NullTime), never Unix integers. Keeping it here avoids nullable-time bugs.
func scanRunTimeRow(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var status int
	var active sql.NullInt64
	var start, end sql.NullTime
	err := row.Scan(&r.ID, &r.RunID, &r.ConvID, &r.UserID, &r.RequestID, &r.InputHash, &r.Model, &status, &active, &r.Generation, &r.CancelReq, &r.StopConfirmed, &r.CheckpointStatus, &r.Commit, &r.RtSeqStart, &r.RtSeqEnd, &r.Iterations, &r.PromptTokens, &r.ComplTokens, &r.ErrorCode, &r.ErrorMessage, &start, &end, &r.CreatedAt, &r.ModifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Status = contract.RunStatusFromCode(status)
	r.Active = active.Valid && active.Int64 == 1
	if start.Valid {
		t := start.Time.UTC()
		r.StartedAt = &t
	}
	if end.Valid {
		t := end.Time.UTC()
		r.EndedAt = &t
	}
	return &r, nil
}
func (s *Store) CreateRun(ctx context.Context, q Querier, r *Run) error {
	now := time.Now().UTC()
	r.CreatedAt, r.ModifiedAt = now, now
	r.Active = true
	res, err := q.ExecContext(ctx, `INSERT INTO t_run(Frun_id,Fconv_id,Fuser_id,Frequest_id,Finput_hash,Fmodel,Fstatus,Factive,Fgeneration,Fcancel_req,Fstop_confirmed,Fcheckpoint_status,Fcommit,Frt_seq_start,Frt_seq_end,Fiterations,Fprompt_tokens,Fcompl_tokens,Ferror_code,Ferror_msg,Fstart_time,Fend_time,Fcreate_time,Fmodify_time) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.RunID, r.ConvID, r.UserID, r.RequestID, r.InputHash, r.Model, r.Status.Code(), 1, r.Generation, 0, 0, r.CheckpointStatus, r.Commit, r.RtSeqStart, r.RtSeqEnd, r.Iterations, r.PromptTokens, r.ComplTokens, r.ErrorCode, r.ErrorMessage, nil, nil, now, now)
	if isDuplicate(err) {
		return ErrDuplicate
	}
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}
func (s *Store) Run(ctx context.Context, q Querier, runID string) (*Run, error) {
	return scanRunTimeRow(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Frun_id=?`, runID))
}
func (s *Store) RunOwned(ctx context.Context, q Querier, userID, convID, runID string) (*Run, error) {
	return scanRunTimeRow(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Frun_id=? AND Fconv_id=? AND Fuser_id=?`, runID, convID, userID))
}
func (s *Store) RunByRequest(ctx context.Context, q Querier, convID, requestID string) (*Run, error) {
	return scanRunTimeRow(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Fconv_id=? AND Frequest_id=?`, convID, requestID))
}
func (s *Store) ActiveRun(ctx context.Context, q Querier, convID string) (*Run, error) {
	return scanRunTimeRow(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Fconv_id=? AND Factive=1`, convID))
}
func (s *Store) LatestSuccessfulRun(ctx context.Context, q Querier, convID string) (*Run, error) {
	return scanRunTimeRow(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Fconv_id=? AND Fstatus=2 AND Fcheckpoint_status=1 ORDER BY Fid DESC LIMIT 1`, convID))
}
func (s *Store) ActiveRunningRun(ctx context.Context, q Querier, convID string) (*Run, error) {
	return scanRunTimeRow(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Fconv_id=? AND Factive=1 AND Fstatus=1`, convID))
}
func (s *Store) ListActiveRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM t_run WHERE Factive=1 ORDER BY Fcreate_time`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRunTimeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}
func (s *Store) TransitionRun(ctx context.Context, q Querier, runID string, from []contract.RunStatus, to contract.RunStatus) (bool, error) {
	where := `Frun_id=? AND Factive=1`
	args := []any{to.Code(), time.Now().UTC(), runID}
	if len(from) > 0 {
		where += ` AND Fstatus IN (` + strings.Repeat("?,", len(from))
		where = strings.TrimSuffix(where, ",") + `)`
		for _, f := range from {
			args = append(args, f.Code())
		}
	}
	res, err := q.ExecContext(ctx, `UPDATE t_run SET Fstatus=?,Fmodify_time=? WHERE `+where, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (s *Store) MarkDispatching(ctx context.Context, q Querier, runID string, generation int64) error {
	_, err := q.ExecContext(ctx, `UPDATE t_run SET Fstatus=?,Fgeneration=?,Fmodify_time=? WHERE Frun_id=? AND Factive=1`, contract.RunDispatching.Code(), generation, time.Now().UTC(), runID)
	return err
}
func (s *Store) MarkRunning(ctx context.Context, q Querier, runID string, now time.Time) error {
	_, err := q.ExecContext(ctx, `UPDATE t_run SET Fstatus=?,Fstart_time=COALESCE(Fstart_time,?),Fmodify_time=? WHERE Frun_id=? AND Factive=1`, contract.RunRunning.Code(), now, now, runID)
	return err
}
func (s *Store) RequestRunCancel(ctx context.Context, q Querier, runID string) error {
	_, err := q.ExecContext(ctx, `UPDATE t_run SET Fcancel_req=1,Fstatus=CASE WHEN Fstatus IN (0,1,5) THEN ? ELSE Fstatus END,Fmodify_time=? WHERE Frun_id=? AND Factive=1`, contract.RunCancelling.Code(), time.Now().UTC(), runID)
	return err
}
func (s *Store) MarkStopConfirmed(ctx context.Context, q Querier, runID string) error {
	_, err := q.ExecContext(ctx, `UPDATE t_run SET Fstop_confirmed=1,Fmodify_time=? WHERE Frun_id=?`, time.Now().UTC(), runID)
	return err
}
func (s *Store) SetRunFailure(ctx context.Context, q Querier, runID, code, message string) error {
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := q.ExecContext(ctx, `UPDATE t_run SET Ferror_code=?,Ferror_msg=?,Fmodify_time=? WHERE Frun_id=? AND Factive=1`, code, message, time.Now().UTC(), runID)
	return err
}
func (s *Store) SetRunRuntimeStats(ctx context.Context, q Querier, runID string, seqEnd, iterations int) error {
	_, err := q.ExecContext(ctx, `UPDATE t_run SET Frt_seq_end=?,Fiterations=?,Fmodify_time=? WHERE Frun_id=?`, seqEnd, iterations, time.Now().UTC(), runID)
	return err
}
func (s *Store) FinalizeRun(ctx context.Context, q Querier, r *Run, terminal contract.RunStatus, checkpoint, commit, errorCode, errorMessage string, prompt, completion int, ended time.Time) (bool, error) {
	res, err := q.ExecContext(ctx, `UPDATE t_run SET Fstatus=?,Factive=NULL,Fstop_confirmed=1,Fcheckpoint_status=?,Fcommit=?,Fprompt_tokens=?,Fcompl_tokens=?,Ferror_code=?,Ferror_msg=?,Fend_time=?,Fmodify_time=? WHERE Frun_id=? AND Factive=1`, terminal.Code(), CheckpointCode(checkpoint), commit, prompt, completion, errorCode, errorMessage, ended, ended, r.RunID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (s *Store) ActiveRunCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT Fstatus,COUNT(*) FROM t_run WHERE Factive=1 GROUP BY Fstatus`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var code, n int
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		out[string(contract.RunStatusFromCode(code))] = n
	}
	return out, rows.Err()
}
