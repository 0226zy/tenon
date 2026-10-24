// Package metrics is a small dependency-free Prometheus text-format registry.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu     sync.RWMutex
	values map[string]float64
}

func New() *Registry { return &Registry{values: map[string]float64{}} }
func (r *Registry) Add(name string, delta float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key(name, labels)] += delta
}
func (r *Registry) Set(name string, value float64, labels ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key(name, labels)] = value
}
func (r *Registry) Snapshot() map[string]float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]float64, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out
}
func key(name string, labels []string) string {
	if len(labels) == 0 {
		return name
	}
	pairs := make([]string, 0, len(labels)/2)
	for i := 0; i+1 < len(labels); i += 2 {
		pairs = append(pairs, fmt.Sprintf("%s=%q", labels[i], labels[i+1]))
	}
	return name + "{" + strings.Join(pairs, ",") + "}"
}
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.mu.RLock()
		keys := make([]string, 0, len(r.values))
		for k := range r.values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		for _, k := range keys {
			fmt.Fprintf(w, "%s %g\n", k, r.values[k])
		}
		r.mu.RUnlock()
	})
}
