package cpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/proto-platform/pkg/contract"
)

func TestConfigBuildSlotAndRelease(t *testing.T) {
	var acquireCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sandbox-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/internal/sandbox/config":
			var cfg contract.SandboxConfig
			cfg.Models = []contract.SandboxModel{{ID: "model-a", Default: true}}
			cfg.Preview.BuildTimeoutS = 90
			_ = json.NewEncoder(w).Encode(cfg)
		case "/internal/build/acquire":
			if acquireCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_ = json.NewEncoder(w).Encode(contract.BuildAcquireResult{SlotID: "slot-1"})
		case "/internal/build/release":
			var req contract.BuildReleaseRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SlotID != "slot-1" {
				t.Errorf("release = %+v, %v", req, err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := New(server.URL, "sandbox-token")
	cfg, err := client.SandboxConfig(context.Background())
	if err != nil || len(cfg.Models) != 1 || cfg.Models[0].ID != "model-a" {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	_, retryAfter, err := client.AcquireBuild(context.Background(), "preview")
	if !errors.Is(err, ErrBuildBusy) || retryAfter != 2*time.Second {
		t.Fatalf("busy = %v, retry=%s", err, retryAfter)
	}
	slot, _, err := client.AcquireBuild(context.Background(), "preview")
	if err != nil || slot != "slot-1" {
		t.Fatalf("acquire = %q, %v", slot, err)
	}
	if err := client.ReleaseBuild(context.Background(), slot); err != nil {
		t.Fatal(err)
	}
}
