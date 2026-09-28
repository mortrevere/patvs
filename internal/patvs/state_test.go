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
