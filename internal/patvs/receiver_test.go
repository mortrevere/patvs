package patvs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestReceiverAPIRejectsWrongSecret(t *testing.T) {
	r := &receiver{
		cfg: Config{Secret: "correct"},
		state: receiverDiskState{
			Emitters: make(map[string]EmitterInfo),
			Streams:  make(map[string]bool),
		},
		peers: make(map[string]ReceiverInfo),
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response := httptest.NewRecorder()
	r.apiHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestEmitterSessionRejectsMalformedRegistration(t *testing.T) {
	r := testReceiver()
	server := httptest.NewServer(r.apiHandler())
	defer server.Close()
	conn := dialTestSession(t, server.URL, "secret")
	defer conn.Close()
	if err := conn.WriteJSON(sessionMessage{Type: "heartbeat", ID: "not-registered"}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("session remained open after malformed registration")
	}
}

func TestEmitterSessionDropsMalformedFrame(t *testing.T) {
	r := testReceiver()
	server := httptest.NewServer(r.apiHandler())
	defer server.Close()
	conn := dialTestSession(t, server.URL, "secret")
	defer conn.Close()
	if err := conn.WriteJSON(sessionMessage{Type: "register", ID: "camera", Name: "camera"}); err != nil {
		t.Fatal(err)
	}
	var demand sessionMessage
	if err := conn.ReadJSON(&demand); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("not a JPEG")); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(sessionMessage{Type: "heartbeat"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.RLock()
		seen := !r.state.Emitters["camera"].LastSeen.IsZero()
		_, stored := r.latest["camera"]
		r.mu.RUnlock()
		if seen {
			if stored {
				t.Fatal("receiver stored malformed binary frame")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("receiver did not process heartbeat after malformed frame")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testReceiver() *receiver {
	return &receiver{
		cfg: Config{Secret: "secret"},
		state: receiverDiskState{
			Identity: Identity{ID: "receiver", Name: "receiver"},
			Emitters: make(map[string]EmitterInfo),
			Streams:  make(map[string]bool),
		},
		sessions:  make(map[string]*emitterSession),
		latest:    make(map[string][]byte),
		waiters:   make(map[string][]chan []byte),
		listeners: make(map[string]map[chan []byte]struct{}),
		peers:     make(map[string]ReceiverInfo),
	}
}

func dialTestSession(t *testing.T, serverURL, secret string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Authorization": []string{"Bearer " + secret}}
	url := "ws" + strings.TrimPrefix(serverURL, "http") + "/v1/session"
	conn, response, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		if response != nil {
			t.Fatalf("dial session: %s: %v", response.Status, err)
		}
		t.Fatal(err)
	}
	return conn
}

func TestReceiverAPIRejectsOversizedJSON(t *testing.T) {
	r := &receiver{
		cfg: Config{Secret: "secret"},
		state: receiverDiskState{
			Emitters: map[string]EmitterInfo{"camera": {ID: "camera"}},
			Streams:  make(map[string]bool),
		},
	}
	body := `{"enabled":true,"padding":"` + strings.Repeat("x", 70<<10) + `"}`
	request := httptest.NewRequest(http.MethodPut, "/v1/streams/camera", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	r.apiHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestReceiverIgnoresInvalidJPEGFrames(t *testing.T) {
	for name, test := range map[string]struct {
		frame []byte
		want  bool
	}{
		"valid":           {[]byte{0xff, 0xd8, 0xff, 0xd9}, true},
		"missing start":   {[]byte{0, 0, 0xff, 0xd9}, false},
		"missing end":     {[]byte{0xff, 0xd8, 0, 0}, false},
		"too short":       {[]byte{0xff, 0xd8}, false},
		"oversized frame": {make([]byte, maxFrameSize+1), false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := validJPEGFrame(test.frame); got != test.want {
				t.Fatalf("got %t, want %t", got, test.want)
			}
		})
	}
}
