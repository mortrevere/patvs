package patvs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestEmitterForgetsUnreachableReceiver(t *testing.T) {
	t.Parallel()
	var attempts, probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/session":
			attempts.Add(1)
		case "/v1/health":
			probes.Add(1)
		}
		http.Error(w, "receiver unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "emitter.json")
	identity := Identity{ID: "camera-id", Name: "camera"}
	peer := ReceiverInfo{ID: "receiver-id", Name: "screen", Address: strings.TrimPrefix(server.URL, "http://")}
	if err := saveJSON(path, emitterDiskState{Identity: identity, Receivers: map[string]ReceiverInfo{peer.ID: peer}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- RunEmitter(ctx, Config{Name: identity.Name, StatePath: path, Camera: "lavfi:color=c=red", APIAddr: peer.Address})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state emitterDiskState
		if err := loadJSON(path, &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Receivers) == 0 {
			if state.Identity != identity {
				t.Fatalf("identity changed: got %#v, want %#v", state.Identity, identity)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("receiver was not forgotten after %d attempts", attempts.Load())
		case <-ticker.C:
		}
	}
	if got := attempts.Load(); got != 10 {
		t.Fatalf("got %d connection attempts, want 10", got)
	}
	previousProbes := probes.Load()
	// Allow another discovery cycle to catch either kind of stale retry.
	time.Sleep(3500 * time.Millisecond)
	if attempts.Load() != 10 || probes.Load() != previousProbes {
		t.Fatalf("forgotten receiver still retried: attempts=%d, probes=%d (was %d)", attempts.Load(), probes.Load(), previousProbes)
	}
}

func TestEmitterReconnectSuccessResetsFailures(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) != 1 {
			http.Error(w, "receiver unavailable", http.StatusServiceUnavailable)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var registration sessionMessage
		if err := conn.ReadJSON(&registration); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer := ReceiverInfo{ID: "receiver-id", Address: strings.TrimPrefix(server.URL, "http://")}
	manager := newCaptureManager(Config{}, Camera{})
	failures := runEmitterSession(ctx, Config{}, Identity{ID: "camera-id"}, manager, peer, nil, 9)
	if failures != 2 || attempts.Load() != 3 {
		t.Fatalf("successful connection did not reset failures: failures=%d, attempts=%d", failures, attempts.Load())
	}
}
