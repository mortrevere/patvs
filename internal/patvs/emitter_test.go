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
		if r.URL.Path == "/v1/health" {
			writeJSON(w, Identity{ID: "receiver-id"})
			return
		}
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
	failures, _ := runEmitterSession(ctx, Config{}, Identity{ID: "camera-id"}, manager, peer, nil, 9)
	if failures != 2 || attempts.Load() != 3 {
		t.Fatalf("successful connection did not reset failures: failures=%d, attempts=%d", failures, attempts.Load())
	}
}

func TestEmitterReplacesStaleReceiverIdentities(t *testing.T) {
	r := testReceiver()
	var registrations atomic.Int32
	handler := r.apiHandler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/session" {
			registrations.Add(1)
		}
		handler.ServeHTTP(w, request)
	}))
	defer server.Close()
	endpoint := strings.TrimPrefix(server.URL, "http://")
	alias := strings.Replace(endpoint, "127.0.0.1", "localhost", 1)
	// A stale LAN alias can survive discovery choosing another address for
	// the current ID. A neighbor can also keep sending the obsolete hint.
	r.peers["stale-hint"] = ReceiverInfo{ID: "old-receiver", Address: alias}
	path := filepath.Join(t.TempDir(), "emitter.json")
	identity := Identity{ID: "camera", Name: "camera"}
	if err := saveJSON(path, emitterDiskState{Identity: identity, Receivers: map[string]ReceiverInfo{
		"receiver":     {ID: "receiver", Address: endpoint},
		"old-receiver": {ID: "old-receiver", Address: alias},
		"another-old":  {ID: "another-old", Address: endpoint},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- RunEmitter(ctx, Config{Name: "camera", Secret: "secret", StatePath: path, Camera: "lavfi:color=c=red", APIAddr: endpoint})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for {
		var state emitterDiskState
		if err := loadJSON(path, &state); err != nil {
			t.Fatal(err)
		}
		if len(state.Receivers) == 1 && state.Receivers["receiver"].ID == "receiver" {
			if state.Identity != identity {
				t.Fatal("emitter identity changed while removing stale receivers")
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("stale receivers survived: %#v", state.Receivers)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Include another discovery cycle and a repeated stale peer hint.
	deadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(deadline) {
		r.mu.RLock()
		session := r.sessions["camera"]
		r.mu.RUnlock()
		if session != nil {
			_ = session.send(sessionMessage{Type: "peers", Receivers: []ReceiverInfo{{ID: "old-receiver", Address: alias}}})
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := registrations.Load(); got != 1 {
		t.Fatalf("receiver accepted %d sessions for one emitter, want one stable session", got)
	}
	var state emitterDiskState
	if err := loadJSON(path, &state); err != nil || len(state.Receivers) != 1 || state.Receivers["receiver"].ID != "receiver" {
		t.Fatalf("stale hints restored obsolete state: %#v, %v", state.Receivers, err)
	}
}
