package boot

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/0226zy/proto-platform/internal/wsd/proc"
)

type Supervisor interface {
	Start(context.Context) error
	Wait() error
	Healthy(context.Context) bool
}

const productionAgentRuntimeAddress = "127.0.0.1:3080"

type agentRuntimeSupervisor struct {
	cmd       *exec.Cmd
	registry  *proc.Registry
	client    *http.Client
	address   string
	healthURL string

	mu      sync.Mutex
	pgid    int
	done    chan error
	waitErr error
}

func newAgentRuntimeSupervisor(cmd *exec.Cmd, registry *proc.Registry, address string) *agentRuntimeSupervisor {
	return &agentRuntimeSupervisor{
		cmd: cmd, registry: registry, done: make(chan error, 1), address: address, healthURL: "http://" + address + "/healthz",
		client: &http.Client{Timeout: time.Second},
	}
}

func allocateAgentRuntimeAddress(local bool) (string, error) {
	if !local {
		return productionAgentRuntimeAddress, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate local agent-runtime address: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("release local agent-runtime address probe: %w", err)
	}
	return address, nil
}

func (d *agentRuntimeSupervisor) Start(ctx context.Context) error {
	// Never adopt an unrelated, already healthy process as "our" agent-runtime.
	// This is especially important for the explicit local driver on a developer host.
	listener, err := net.Listen("tcp", d.address)
	if err != nil {
		return fmt.Errorf("agent-runtime address %s is unavailable: %w", d.address, err)
	}
	if err := listener.Close(); err != nil {
		return err
	}
	pgid, err := proc.StartProcess(d.cmd)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.pgid = pgid
	d.mu.Unlock()
	d.registry.Own("agent-runtime", pgid)
	go func() {
		err := d.cmd.Wait()
		d.mu.Lock()
		d.waitErr = err
		d.mu.Unlock()
		d.done <- err
		close(d.done)
	}()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-d.done:
			if err == nil {
				err = errors.New("agent-runtime exited before becoming healthy")
			}
			return err
		default:
		}
		if d.Healthy(ctx) {
			// Close the bind-failure race where an unrelated listener answered
			// just before our child reported its exit.
			select {
			case err := <-d.done:
				if err == nil {
					err = errors.New("agent-runtime exited during health check")
				}
				return err
			default:
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-d.done:
			if err == nil {
				err = errors.New("agent-runtime exited before becoming healthy")
			}
			return err
		case <-ticker.C:
		}
	}
}

func (d *agentRuntimeSupervisor) Done() <-chan error { return d.done }

func (d *agentRuntimeSupervisor) Wait() error {
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waitErr
}

func (d *agentRuntimeSupervisor) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (d *agentRuntimeSupervisor) Stop(ctx context.Context) error {
	d.mu.Lock()
	pgid := d.pgid
	d.mu.Unlock()
	if pgid == 0 {
		return nil
	}
	err := proc.StopGroup(ctx, pgid, 5*time.Second)
	select {
	case <-d.done:
	case <-ctx.Done():
	}
	d.registry.Disown("agent-runtime")
	return err
}
