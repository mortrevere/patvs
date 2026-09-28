package patvs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
