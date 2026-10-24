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

const productionDshdAddress = "127.0.0.1:3080"

type dshdSupervisor struct {
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

func newDshdSupervisor(cmd *exec.Cmd, registry *proc.Registry, address string) *dshdSupervisor {
	return &dshdSupervisor{
		cmd: cmd, registry: registry, done: make(chan error, 1), address: address, healthURL: "http://" + address + "/healthz",
		client: &http.Client{Timeout: time.Second},
	}
}

func allocateDshdAddress(local bool) (string, error) {
	if !local {
		return productionDshdAddress, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("allocate local dshd address: %w", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", fmt.Errorf("release local dshd address probe: %w", err)
	}
	return address, nil
}

func (d *dshdSupervisor) Start(ctx context.Context) error {
	// Never adopt an unrelated, already healthy process as "our" dshd. This is
	// especially important for the explicit local driver on a developer host.
	listener, err := net.Listen("tcp", d.address)
	if err != nil {
		return fmt.Errorf("dshd address %s is unavailable: %w", d.address, err)
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
	d.registry.Own("dshd", pgid)
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
				err = errors.New("dshd exited before becoming healthy")
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
					err = errors.New("dshd exited during health check")
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
				err = errors.New("dshd exited before becoming healthy")
			}
			return err
		case <-ticker.C:
		}
	}
}

func (d *dshdSupervisor) Done() <-chan error { return d.done }

func (d *dshdSupervisor) Wait() error {
	<-d.done
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.waitErr
}

func (d *dshdSupervisor) Healthy(ctx context.Context) bool {
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

func (d *dshdSupervisor) Stop(ctx context.Context) error {
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
	d.registry.Disown("dshd")
	return err
}
