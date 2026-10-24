package store

import (
	"context"
	"time"
)

func (s *Store) RecordUsage(ctx context.Context, q Querier, u *UsageRow) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	_, err := q.ExecContext(ctx, `INSERT INTO t_llm_usage(Fconv_id,Frun_id,Fuser_id,Fmodel,Fprompt_tokens,Fcompl_tokens,Fcost_ms,Fhttp_status,Fcreate_time) VALUES(?,?,?,?,?,?,?,?,?)`, u.ConvID, u.RunID, u.UserID, u.Model, u.PromptTokens, u.ComplTokens, u.CostMS, u.HTTPStatus, u.CreatedAt)
	return err
}
func (s *Store) SumUsageByRun(ctx context.Context, q Querier, runID string) (int, int, error) {
	var p, c int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(Fprompt_tokens),0),COALESCE(SUM(Fcompl_tokens),0) FROM t_llm_usage WHERE Frun_id=?`, runID).Scan(&p, &c)
	return p, c, err
}
func (s *Store) ReportUsage(ctx context.Context, userID string, from, to time.Time) ([]UsageAgg, error) {
	from, to = from.UTC(), to.UTC()
	where := `Fcreate_time>=? AND Fcreate_time<?`
	args := []any{from, to}
	if userID != "" {
		where += ` AND Fuser_id=?`
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT Fuser_id,Fmodel,SUM(Fprompt_tokens),SUM(Fcompl_tokens),COUNT(*) FROM t_llm_usage WHERE `+where+` GROUP BY Fuser_id,Fmodel ORDER BY Fuser_id,Fmodel`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageAgg{}
	for rows.Next() {
		var a UsageAgg
		if err := rows.Scan(&a.UserID, &a.Model, &a.PromptTokens, &a.ComplTokens, &a.Requests); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
