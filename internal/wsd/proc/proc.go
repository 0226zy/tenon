// Package proc tracks owned process groups and cleans untracked child processes.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type processInfo struct {
	pid   int
	ppid  int
	pgid  int
	state string
}

type Registry struct {
	mu     sync.Mutex
	self   int
	ownPG  int
	owned  map[string]int
	groups map[int]string
}

func New() *Registry {
	ownPG, _ := syscall.Getpgid(os.Getpid())
	return &Registry{self: os.Getpid(), ownPG: ownPG, owned: make(map[string]int), groups: make(map[int]string)}
}

func (r *Registry) Own(name string, pgid int) {
	if name == "" || pgid <= 0 {
		return
	}
	r.mu.Lock()
	if old, ok := r.owned[name]; ok && old != pgid {
		delete(r.groups, old)
	}
	r.owned[name] = pgid
	r.groups[pgid] = name
	r.mu.Unlock()
}

func (r *Registry) Disown(name string) {
	r.mu.Lock()
	if pgid, ok := r.owned[name]; ok {
		delete(r.owned, name)
		delete(r.groups, pgid)
	}
	r.mu.Unlock()
}

func (r *Registry) ownedGroups() map[int]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int]bool, len(r.groups))
	for pgid := range r.groups {
		out[pgid] = true
	}
	return out
}

func (r *Registry) processes() ([]processInfo, error) {
	return listProcesses()
}

func (r *Registry) Orphans() ([]int, error) {
	processes, err := r.processes()
	if err != nil {
		return nil, err
	}
	owned := r.ownedGroups()
	seen := make(map[int]bool)
	out := make([]int, 0)
	for _, p := range processes {
		if p.ppid == r.self && p.pid != r.self && !owned[p.pgid] && !seen[p.pid] {
			seen[p.pid] = true
			out = append(out, p.pid)
		}
	}
	return out, nil
}

func (r *Registry) orphanProcesses() ([]processInfo, error) {
	processes, err := r.processes()
	if err != nil {
		return nil, err
	}
	owned := r.ownedGroups()
	out := make([]processInfo, 0)
	for _, p := range processes {
		if p.ppid == r.self && p.pid != r.self && !owned[p.pgid] {
			out = append(out, p)
		}
	}
	return out, nil
}

// Reap terminates orphaned groups without ever targeting a registered dshd or
// preview group. It waits for actual disappearance and reaps direct zombies.
func (r *Registry) Reap(ctx context.Context, grace time.Duration) (killed, remaining int, err error) {
	initial, err := r.orphanProcesses()
	if err != nil {
		return 0, 0, err
	}
	for _, p := range initial {
		if p.state == "Z" {
			_, _ = syscall.Wait4(p.pid, nil, syscall.WNOHANG, nil)
			continue
		}
		if err := terminate(p, r.ownPG, syscall.SIGTERM); err == nil || errors.Is(err, syscall.ESRCH) {
			killed++
		}
	}
	if err := r.waitNoOrphans(ctx, grace); err == nil {
		return killed, 0, nil
	}
	left, scanErr := r.orphanProcesses()
	if scanErr != nil {
		return killed, 0, scanErr
	}
	for _, p := range left {
		if p.state == "Z" {
			_, _ = syscall.Wait4(p.pid, nil, syscall.WNOHANG, nil)
			continue
		}
		_ = terminate(p, r.ownPG, syscall.SIGKILL)
	}
	killCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = r.waitNoOrphans(killCtx, 3*time.Second)
	left, scanErr = r.orphanProcesses()
	if scanErr != nil {
		return killed, 0, scanErr
	}
	remaining = len(left)
	if remaining > 0 {
		return killed, remaining, fmt.Errorf("%d orphan processes remain", remaining)
	}
	return killed, 0, nil
}

func terminate(p processInfo, ownPG int, sig syscall.Signal) error {
	// A normal child can share wsd's own process group. Never group-kill that
	// group; detached setsid children get a safe independent group kill instead.
	if p.pgid > 0 && p.pgid != ownPG && p.pgid != p.pid {
		if err := syscall.Kill(-p.pgid, sig); err == nil || !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	if p.pgid == p.pid && p.pgid != ownPG {
		return syscall.Kill(-p.pgid, sig)
	}
	return syscall.Kill(p.pid, sig)
}

func (r *Registry) waitNoOrphans(ctx context.Context, wait time.Duration) error {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		left, err := r.orphanProcesses()
		if err != nil {
			return err
		}
		for _, p := range left {
			if p.state == "Z" {
				_, _ = syscall.Wait4(p.pid, nil, syscall.WNOHANG, nil)
			}
		}
		if len(left) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return context.DeadlineExceeded
		case <-ticker.C:
		}
	}
}

// StartReaper reaps only unregistered children, avoiding Cmd.Wait races for the
// registered dshd and preview processes.
func (r *Registry) StartReaper(ctx context.Context) {
	sig := make(chan os.Signal, 1)
	signalNotifySIGCHLD(sig)
	defer signalStop(sig)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sig:
		case <-ticker.C:
		}
		children, err := r.orphanProcesses()
		if err != nil {
			continue
		}
		for _, p := range children {
			if p.state == "Z" {
				_, _ = syscall.Wait4(p.pid, nil, syscall.WNOHANG, nil)
			}
		}
	}
}

// StartProcess configures a child as the leader of an independent process group.
func StartProcess(cmd *exec.Cmd) (int, error) {
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	return cmd.Process.Pid, nil
}

// StopGroup terminates only a specifically registered process group.
func StopGroup(ctx context.Context, pgid int, grace time.Duration) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if waitGroupGone(ctx, pgid, grace) == nil {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return waitGroupGone(context.Background(), pgid, 3*time.Second)
}

func waitGroupGone(ctx context.Context, pgid int, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !groupAlive(pgid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return context.DeadlineExceeded
		case <-ticker.C:
		}
	}
}

func groupAlive(pgid int) bool {
	processes, err := listProcesses()
	if err == nil {
		for _, p := range processes {
			if p.pgid == pgid && p.state != "Z" {
				return true
			}
		}
		return false
	}
	return syscall.Kill(-pgid, 0) == nil
}

func parseStatFields(pid int, fields string) (processInfo, error) {
	parts := strings.Fields(fields)
	if len(parts) < 3 {
		return processInfo{}, fmt.Errorf("short process stat for %d", pid)
	}
	ppid, err := strconv.Atoi(parts[1])
	if err != nil {
		return processInfo{}, err
	}
	pgid, err := strconv.Atoi(parts[2])
	if err != nil {
		return processInfo{}, err
	}
	return processInfo{pid: pid, ppid: ppid, pgid: pgid, state: parts[0]}, nil
}
