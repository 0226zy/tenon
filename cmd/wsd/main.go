package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/0226zy/proto-platform/internal/wsd/boot"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{}))
	env, err := boot.LoadEnv()
	if err != nil {
		logger.Error("wsd configuration failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := boot.Run(ctx, env); err != nil {
		logger.Error("wsd exited", "sandbox_id", env.SandboxID, "error", err)
		os.Exit(1)
	}
}
