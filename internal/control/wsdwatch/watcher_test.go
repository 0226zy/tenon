package wsdwatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0226zy/tenon/internal/control/agentclient"
	"github.com/0226zy/tenon/internal/control/event"
	"github.com/0226zy/tenon/internal/control/sandbox"
	"github.com/0226zy/tenon/internal/kit/config"
	"github.com/0226zy/tenon/pkg/contract"
)

type noopPreviewUpdater struct{}

func (noopPreviewUpdater) UpdatePreview(context.Context, string, contract.PreviewState) error {
	return nil
}

func TestWatcherConnectionFailuresUseBackoff(t *testing.T) {
	requests := make(chan time.Time, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			http.NotFound(w, r)
			return
		}
		recordAttempt(requests)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	watcher := newTestWatcher(server, event.NewHub())
	watcher.initialBackoff = 50 * time.Millisecond
	watcher.maxBackoff = 75 * time.Millisecond
	watcher.Attach(testHandle(server))
	defer watcher.Close()

	attempts := awaitAttempts(t, requests, 4)
	watcher.Close()
	assertMinimumIntervals(t, attempts, []time.Duration{
		50 * time.Millisecond,
		75 * time.Millisecond,
		75 * time.Millisecond,
	})
}

func TestWatcherEstablishedStreamFailuresUseBackoff(t *testing.T) {
	requests := make(chan time.Time, 32)
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			http.NotFound(w, r)
			return
		}
		connection := connections.Add(1)
		recordAttempt(requests)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if connection == 1 {
			_, _ = io.WriteString(w, "event: file_changed\ndata: {\"paths\":[\"src/main.go\"]}\n\n")
		}
		w.(http.Flusher).Flush()
	}))
	defer server.Close()

	hub := event.NewHub()
	subscription := hub.Subscribe("conv")
	defer subscription.Close()
	watcher := newTestWatcher(server, hub)
	watcher.initialBackoff = 50 * time.Millisecond
	watcher.maxBackoff = 100 * time.Millisecond
	watcher.Attach(testHandle(server))
	defer watcher.Close()

	attempts := awaitAttempts(t, requests, 3)
	watcher.Close()
	assertMinimumIntervals(t, attempts, []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
	})

	select {
	case frame := <-subscription.Temp:
		if len(frame.Items) != 1 || frame.Items[0].Type != "files_changed" {
			t.Fatalf("forwarded frame = %#v, want one files_changed item", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the event received before the stream failed")
	}
}

func TestWatcherBackoffIsCancelable(t *testing.T) {
	for _, test := range []struct {
		name            string
		establishStream bool
	}{
		{name: "connection failure"},
		{name: "established stream failure", establishStream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan time.Time, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recordAttempt(requests)
				if !test.establishStream {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
			}))
			defer server.Close()

			watcher := newTestWatcher(server, event.NewHub())
			watcher.initialBackoff = 5 * time.Second
			watcher.maxBackoff = 5 * time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				watcher.loop(ctx, testHandle(server))
			}()

			awaitAttempts(t, requests, 1)
			cancel()
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("watcher did not cancel its reconnect backoff promptly")
			}
		})
	}
}

func TestNextBackoffIsBounded(t *testing.T) {
	for _, test := range []struct {
		name    string
		current time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{name: "double", current: time.Second, max: 30 * time.Second, want: 2 * time.Second},
		{name: "round down to cap", current: 16 * time.Second, max: 30 * time.Second, want: 30 * time.Second},
		{name: "stay at cap", current: 30 * time.Second, max: 30 * time.Second, want: 30 * time.Second},
		{name: "non power of two cap", current: 50 * time.Millisecond, max: 75 * time.Millisecond, want: 75 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nextBackoff(test.current, test.max); got != test.want {
				t.Fatalf("nextBackoff(%v, %v) = %v, want %v", test.current, test.max, got, test.want)
			}
		})
	}
}

func newTestWatcher(server *httptest.Server, hub *event.Hub) *Watcher {
	return New(
		agentclient.New(config.AgentClient{}),
		hub,
		event.Mapper{},
		noopPreviewUpdater{},
	)
}

func testHandle(server *httptest.Server) sandbox.Handle {
	return sandbox.Handle{ConvID: "conv", Addr: server.Listener.Addr().String()}
}

func recordAttempt(requests chan<- time.Time) {
	select {
	case requests <- time.Now():
	default:
	}
}

func awaitAttempts(t *testing.T, requests <-chan time.Time, count int) []time.Time {
	t.Helper()
	attempts := make([]time.Time, 0, count)
	for len(attempts) < count {
		select {
		case attempt := <-requests:
			attempts = append(attempts, attempt)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for request %d; got %d", len(attempts)+1, len(attempts))
		}
	}
	return attempts
}

func assertMinimumIntervals(t *testing.T, attempts []time.Time, want []time.Duration) {
	t.Helper()
	if len(attempts) != len(want)+1 {
		t.Fatalf("got %d attempts, want %d", len(attempts), len(want)+1)
	}
	for i, minimum := range want {
		if interval := attempts[i+1].Sub(attempts[i]); interval < minimum {
			t.Errorf("reconnect %d interval = %v, want at least %v", i+1, interval, minimum)
		}
	}
}
