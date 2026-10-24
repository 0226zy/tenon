// Package sandbox manages sandbox lifecycle independently of HTTP request contexts.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

var ErrDriverNotConfigured = errors.New("sandbox driver is not configured")

type Handle struct{ ConvID, InstID, Addr, WSDToken string }

func (h Handle) BaseURL() string {
	if _, _, err := net.SplitHostPort(h.Addr); err == nil {
		return "http://" + h.Addr
	}
	return "http://" + net.JoinHostPort(h.Addr, "7000")
}
func (h Handle) Host() string {
	if host, _, err := net.SplitHostPort(h.Addr); err == nil {
		return host
	}
	return h.Addr
}

type Spec struct{ ConvID, InstID, ProxyToken, WSDToken, InternalURL, ImageVer string }
type Instance struct {
	ID, Addr string
	PID      int
}
type State struct {
	Running  bool
	Addr     string
	ExitCode int
}
type Driver interface {
	Create(context.Context, Spec) (Instance, error)
	Stop(context.Context, string) error
	Remove(context.Context, string, bool) error
	Inspect(context.Context, string) (State, error)
	Close() error
}
type DockerUnavailable struct{}

func (DockerUnavailable) Create(context.Context, Spec) (Instance, error) {
	return Instance{}, ErrDriverNotConfigured
}
func (DockerUnavailable) Stop(context.Context, string) error         { return ErrDriverNotConfigured }
func (DockerUnavailable) Remove(context.Context, string, bool) error { return ErrDriverNotConfigured }
func (DockerUnavailable) Inspect(context.Context, string) (State, error) {
	return State{}, ErrDriverNotConfigured
}
func (DockerUnavailable) Close() error { return nil }

type Principal struct{ ConvID, UserID, InstID string }
type contextKey struct{}

func WithPrincipal(r *http.Request, p Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), contextKey{}, p))
}
func PrincipalFromRequest(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(contextKey{}).(Principal)
	return p, ok
}
func SandboxStarting() error { return fmt.Errorf("sandbox starting") }
