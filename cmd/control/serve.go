package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/0226zy/protoforge/internal/control/app"
	"github.com/0226zy/protoforge/internal/kit/config"
)

type controlServer struct {
	name     string
	server   *http.Server
	listener net.Listener
}

func serve(ctx context.Context, cfg *config.Config, stdout io.Writer) error {
	a, err := app.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	newServer := func(name, address string, handler http.Handler) controlServer {
		return controlServer{name: name, server: &http.Server{
			Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
			BaseContext: func(net.Listener) context.Context { return requestCtx },
		}}
	}
	servers := []controlServer{
		newServer("api", cfg.Listen.API, a.APIHandler),
		newServer("preview", cfg.Listen.Preview, a.PreviewHandler),
		newServer("internal", cfg.Listen.Internal, a.InternalHandler),
		newServer("metrics", cfg.Listen.Metrics, a.MetricsHandler),
	}
	closeListeners := func() {
		for i := range servers {
			if servers[i].listener != nil {
				_ = servers[i].listener.Close()
			}
		}
	}
	defer closeListeners()
	for i := range servers {
		listener, err := net.Listen("tcp", servers[i].server.Addr)
		if err != nil {
			return fmt.Errorf("listen %s on %s: %w", servers[i].name, servers[i].server.Addr, err)
		}
		servers[i].listener = listener
	}
	if err := a.Start(ctx); err != nil {
		return err
	}
	errCh := make(chan error, len(servers))
	var wg sync.WaitGroup
	for i := range servers {
		s := &servers[i]
		fmt.Fprintf(stdout, "%s listening on %s\n", s.name, s.listener.Addr())
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s server: %w", s.name, err)
			}
		}()
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}
	cancelRequests()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	for i := range servers {
		if err := servers[i].server.Shutdown(shutdownCtx); err != nil {
			_ = servers[i].server.Close()
			serveErr = errors.Join(serveErr, err)
		}
	}
	cancel()
	wg.Wait()
	if ctx.Err() != nil && serveErr == nil {
		fmt.Fprintln(stdout, "control stopped")
	}
	return serveErr
}
