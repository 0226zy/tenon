// Package logx carries a structured logger through request and worker contexts.
package logx

import (
	"context"
	"log/slog"
)

type key struct{}

func With(ctx context.Context, kv ...any) context.Context {
	l := From(ctx).With(kv...)
	return context.WithValue(ctx, key{}, l)
}

func From(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(key{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}
