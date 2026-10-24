package llm

import (
	"context"
	"time"

	"github.com/0226zy/proto-platform/internal/control/store"
)

type UsageService struct{ St *store.Store }

func (u *UsageService) Record(ctx context.Context, v store.UsageRow) error {
	return u.St.RecordUsage(ctx, u.St.DB(), &v)
}
func (u *UsageService) SumByRun(ctx context.Context, runID string) (int, int, error) {
	return u.St.SumUsageByRun(ctx, u.St.DB(), runID)
}
func (u *UsageService) Report(ctx context.Context, userID string, from, to time.Time) ([]store.UsageAgg, error) {
	return u.St.ReportUsage(ctx, userID, from, to)
}
