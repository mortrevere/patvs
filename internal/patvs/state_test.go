package patvs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEmitterResetRememberedReceivers(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%t", reset), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "emitter.json")
			identity := Identity{ID: "camera-id", Name: "camera"}
			state := emitterDiskState{Identity: identity, Receivers: map[string]ReceiverInfo{
				"receiver-id": {ID: "receiver-id", Name: "screen", Address: "127.0.0.1:7411"},
			}}
			if err := saveJSON(path, state); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cfg := Config{Name: identity.Name, StatePath: path, Camera: "lavfi:color=c=red", Reset: reset}
			if err := RunEmitter(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			var got emitterDiskState
			if err := loadJSON(path, &got); err != nil {
				t.Fatal(err)
			}
			if got.Identity != identity {
				t.Fatalf("identity changed: got %#v, want %#v", got.Identity, identity)
			}
			wantPeers := 1
			if reset {
				wantPeers = 0
			}
			if len(got.Receivers) != wantPeers {
				t.Fatalf("got %d remembered receivers, want %d", len(got.Receivers), wantPeers)
			}
		})
	}
}

func TestJSONStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	want := Identity{ID: "abc", Name: "camera"}
	if err := saveJSON(path, want); err != nil {
		t.Fatal(err)
	}
	var got Identity
	if err := loadJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions are %o", info.Mode().Perm())
	}
}

func TestReceiverResetRememberedEmitters(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%t", reset), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receiver.json")
			identity := Identity{ID: "screen-id", Name: "screen"}
			state := receiverDiskState{
				Identity: identity,
				Emitters: map[string]EmitterInfo{"camera-id": {ID: "camera-id", Name: "camera"}},
				Streams:  map[string]bool{"camera-id": true}, Playback: "camera-id",
			}
			if err := saveJSON(path, state); err != nil {
				t.Fatal(err)
			}
			// State is saved before discovery starts; port zero stops startup there.
			cfg := Config{Name: identity.Name, StatePath: path, APIAddr: ":0", Reset: reset}
			if err := RunReceiver(context.Background(), cfg); err == nil {
				t.Fatal("expected invalid discovery API port to stop startup")
			}
			var got receiverDiskState
			if err := loadJSON(path, &got); err != nil {
				t.Fatal(err)
			}
			if got.Identity != identity {
				t.Fatalf("identity changed: got %#v, want %#v", got.Identity, identity)
			}
			if reset {
				if len(got.Emitters) != 0 || len(got.Streams) != 0 || got.Playback != "" {
					t.Fatalf("remembered emitters or playback settings retained: %#v", got)
				}
			} else if len(got.Emitters) != 1 || !got.Streams["camera-id"] || got.Playback != "camera-id" {
				t.Fatalf("remembered emitters or playback settings changed: %#v", got)
			}
		})
	}
}

func TestReceiverHandlersConstructAndRejectInvalidStreamPath(t *testing.T) {
	r := &receiver{listeners: make(map[string]map[chan []byte]struct{})}
	_ = r.apiHandler()
	handler := r.streamHandler()
	request := httptest.NewRequest(http.MethodGet, "/streams/nested/name.mjpg", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestPlayerRetriesAreBounded(t *testing.T) {
	r := &receiver{}
	for range 10 {
		r.schedulePlayerRetryLocked()
	}
	if r.playerTry != 5 {
		t.Fatalf("got %d retry attempts, want 5", r.playerTry)
	}
	if r.playerAt.IsZero() {
		t.Fatal("retry deadline was not set")
	}
}

func TestRemoveSnapshotWaiter(t *testing.T) {
	waiter := make(chan []byte, 1)
	r := &receiver{waiters: map[string][]chan []byte{"camera": {waiter}}}
	r.removeWaiter("camera", waiter)
	if _, exists := r.waiters["camera"]; exists {
		t.Fatal("timed-out snapshot waiter was retained")
	}
}

func TestLegacyEmitterIdentityCanMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "emitter.json")
	legacy := Identity{ID: "legacy-id", Name: "old-name"}
	if err := saveJSON(path, legacy); err != nil {
		t.Fatal(err)
	}
	var state emitterDiskState
	if err := loadJSON(path, &state); err != nil {
		t.Fatal(err)
	}
	if state.Identity.ID != "" {
		t.Fatal("legacy flat identity unexpectedly decoded as nested state")
	}
	identity, err := loadIdentity(path, "new-name")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != legacy.ID || identity.Name != "new-name" {
		t.Fatalf("migration produced %#v", identity)
	}
}
