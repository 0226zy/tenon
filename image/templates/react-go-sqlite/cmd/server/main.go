package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"prototype.local/react-go-sqlite/internal/httpapi"
	"prototype.local/react-go-sqlite/internal/store"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
		log.Fatal("PORT must be a valid TCP port")
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	repository, err := store.Open(dataDir)
	if err != nil {
		log.Fatalf("open SQLite store: %v", err)
	}
	defer repository.Close()

	server := &http.Server{
		Addr: "0.0.0.0:" + port, Handler: httpapi.NewHandler(repository, "web/dist"),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("prototype listening on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}
