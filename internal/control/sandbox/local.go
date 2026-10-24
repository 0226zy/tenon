package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type localProcess struct {
	cmd  *exec.Cmd
	done chan error
	log  *os.File
}
type LocalDriver struct {
	root, wsdBin, templateDir, dshdBin string
	goProxy                            string
	mu                                 sync.Mutex
	procs                              map[string]*localProcess
}

func NewLocalDriver(root, wsdBin, templateDir, dshdBin, goProxy string) *LocalDriver {
	return &LocalDriver{root: root, wsdBin: wsdBin, templateDir: templateDir, dshdBin: dshdBin, goProxy: goProxy, procs: map[string]*localProcess{}}
}

var safeConv = regexp.MustCompile(`^[a-z2-7]{12}$`)

func (d *LocalDriver) dir(conv string) (string, error) {
	if !safeConv.MatchString(conv) {
		return "", fmt.Errorf("invalid conversation id")
	}
	return filepath.Join(d.root, conv), nil
}
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func (d *LocalDriver) Create(ctx context.Context, spec Spec) (Instance, error) {
	dir, err := d.dir(spec.ConvID)
	if err != nil {
		return Instance{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o750); err != nil {
		return Instance{}, err
	}
	// A stale process from a crashed control process must never share the volume
	// with a newly started wsd. Stop it first; the directory itself is preserved.
	_ = d.Stop(ctx, spec.ConvID)
	port, err := freePort()
	if err != nil {
		return Instance{}, err
	}
	log, err := os.OpenFile(filepath.Join(dir, "wsd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Instance{}, err
	}
	cmd := exec.Command(d.wsdBin)
	cmd.Dir = dir
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LANG=" + os.Getenv("LANG"), "WSD_LOCAL=true", "WSD_LISTEN=127.0.0.1:" + strconv.Itoa(port), "WORKSPACE_ROOT=" + filepath.Join(dir, "workspace"), "WSD_TEMPLATE_DIR=" + d.templateDir, "WSD_DSHD_BIN=" + d.dshdBin, "WSD_IMAGE_VER=" + spec.ImageVer, "WSD_PREVIEW_PORT=0", "SANDBOX_ID=" + spec.InstID, "CP_INTERNAL_URL=" + spec.InternalURL, "LLM_PROXY_TOKEN=" + spec.ProxyToken, "WSD_TOKEN=" + spec.WSDToken}
	for _, k := range []string{"GOPROXY", "GOFLAGS", "GOMODCACHE", "npm_config_registry", "npm_config_cache"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	if os.Getenv("GOPROXY") == "" && d.goProxy != "" {
		env = append(env, "GOPROXY="+d.goProxy)
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		log.Close()
		return Instance{}, err
	}
	p := &localProcess{cmd: cmd, done: make(chan error, 1), log: log}
	d.mu.Lock()
	d.procs[spec.ConvID] = p
	d.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "wsd.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		_ = cmd.Process.Kill()
		log.Close()
		return Instance{}, err
	}
	go func() { err := cmd.Wait(); p.done <- err; close(p.done); _ = log.Close() }()
	return Instance{ID: strconv.Itoa(cmd.Process.Pid), Addr: "127.0.0.1:" + strconv.Itoa(port), PID: cmd.Process.Pid}, nil
}
func (d *LocalDriver) Stop(ctx context.Context, conv string) error {
	d.mu.Lock()
	p := d.procs[conv]
	delete(d.procs, conv)
	d.mu.Unlock()
	if p != nil {
		return stopProcess(ctx, p)
	}
	dir, err := d.dir(conv)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "wsd.pid"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid wsd pid file")
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
				return nil
			}
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return err
			}
			return nil
		}
	}
}
func stopProcess(ctx context.Context, p *localProcess) error {
	if p.cmd.Process == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		return ctx.Err()
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("wsd process did not exit")
	}
}
func (d *LocalDriver) Inspect(ctx context.Context, conv string) (State, error) {
	d.mu.Lock()
	p := d.procs[conv]
	d.mu.Unlock()
	if p != nil {
		select {
		case <-p.done:
			return State{Running: false}, nil
		default:
			return State{Running: true, Addr: ""}, nil
		}
	}
	dir, err := d.dir(conv)
	if err != nil {
		return State{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "wsd.pid"))
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	pid, _ := strconv.Atoi(string(raw))
	if pid <= 0 {
		return State{}, nil
	}
	err = syscall.Kill(pid, 0)
	return State{Running: err == nil}, nil
}
func (d *LocalDriver) Remove(ctx context.Context, conv string, keepVolume bool) error {
	if err := d.Stop(ctx, conv); err != nil {
		return err
	}
	if keepVolume {
		return nil
	}
	dir, err := d.dir(conv)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
func (d *LocalDriver) Close() error {
	d.mu.Lock()
	ids := make([]string, 0, len(d.procs))
	for id := range d.procs {
		ids = append(ids, id)
	}
	d.mu.Unlock()
	var out error
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		out = errors.Join(out, d.Stop(ctx, id))
		cancel()
	}
	return out
}
