package patvs

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPlayerCommandFullscreenWindow(t *testing.T) {
	t.Setenv("DISPLAY", ":1")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	r := &receiver{cfg: Config{Player: "vlc", StreamAddr: "127.0.0.1:7413"}}
	cmd := r.playerCommand("camera")
	for _, flag := range []string{"--video-title=patvs-playback", "--fullscreen", "--autoscale"} {
		if !slices.Contains(cmd.Args, flag) {
			t.Fatalf("required window option %s missing: %v", flag, cmd.Args)
		}
	}
	if slices.Contains(cmd.Args, "--verbose=2") {
		t.Fatal("verbose VLC logging enabled without debug")
	}
	r.cfg.Debug = true
	if !slices.Contains(r.playerCommand("camera").Args, "--verbose=2") {
		t.Fatal("debug mode did not enable VLC logging")
	}
}

func TestPlayerCommandDisplayAspectRatio(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		t.Run(fmt.Sprintf("desktop=%t", desktop), func(t *testing.T) {
			t.Setenv("DISPLAY", "")
			t.Setenv("WAYLAND_DISPLAY", "")
			if desktop {
				t.Setenv("WAYLAND_DISPLAY", "wayland-1")
			}
			r := &receiver{cfg: Config{Player: "vlc", StreamAddr: "127.0.0.1:7413"}}
			for _, ratio := range []string{"", "4:3", "16:9"} {
				r.cfg.DisplayAspectRatio = ratio
				cmd := r.playerCommand("camera")
				var aspectArgs []string
				for _, arg := range cmd.Args {
					if strings.HasPrefix(arg, "--aspect-ratio=") {
						aspectArgs = append(aspectArgs, arg)
					}
				}
				if ratio == "" {
					if len(aspectArgs) != 0 {
						t.Fatalf("unexpected ratio override: %v", aspectArgs)
					}
				} else if len(aspectArgs) != 1 || aspectArgs[0] != "--aspect-ratio="+ratio {
					t.Fatalf("incorrect ratio override: %v", aspectArgs)
				}
			}
		})
	}
}

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

func TestDeleteEmitter(t *testing.T) {
	for _, test := range []struct {
		name, id, secret             string
		online, session, saveFailure bool
		want                         int
	}{
		{name: "offline", id: "camera", secret: "secret", want: http.StatusNoContent},
		{name: "unauthorized", id: "camera", secret: "wrong", want: http.StatusUnauthorized},
		{name: "unknown", id: "missing", secret: "secret", want: http.StatusNotFound},
		{name: "online", id: "camera", secret: "secret", online: true, want: http.StatusConflict},
		{name: "connected but stale", id: "camera", secret: "secret", session: true, want: http.StatusConflict},
		{name: "save failure", id: "camera", secret: "secret", saveFailure: true, want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := testReceiver()
			r.cfg.StatePath = filepath.Join(t.TempDir(), "receiver.json")
			if test.saveFailure {
				r.cfg.StatePath = t.TempDir() // Cannot replace a directory with the state file.
			}
			r.state.Emitters["camera"] = EmitterInfo{ID: "camera", Online: test.online}
			r.state.Emitters["other"] = EmitterInfo{ID: "other"}
			r.state.Streams["camera"], r.state.Streams["other"] = true, true
			r.state.Playback = "camera"
			r.latest["camera"] = []byte("cached frame")
			r.playerTry, r.playerErr = 2, "old error"
			if test.session {
				r.sessions["camera"] = &emitterSession{}
			}
			request := httptest.NewRequest(http.MethodDelete, "/v1/emitters/"+test.id, nil)
			request.Header.Set("Authorization", "Bearer "+test.secret)
			response := httptest.NewRecorder()
			r.apiHandler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("got %d: %s, want %d", response.Code, response.Body.String(), test.want)
			}
			if test.saveFailure {
				if !r.dirty {
					t.Fatal("failed save was not marked for retry")
				}
				return
			}
			if test.want != http.StatusNoContent {
				if len(r.state.Emitters) != 2 || !r.state.Streams["camera"] || r.state.Playback != "camera" || r.latest["camera"] == nil {
					t.Fatal("rejected deletion changed receiver state")
				}
				return
			}
			var saved receiverDiskState
			if err := loadJSON(r.cfg.StatePath, &saved); err != nil {
				t.Fatal(err)
			}
			if len(saved.Emitters) != 1 || saved.Emitters["other"].ID != "other" || len(saved.Streams) != 1 || !saved.Streams["other"] || saved.Playback != "" {
				t.Fatalf("incorrect persisted state: %#v", saved)
			}
			if r.latest["camera"] != nil || r.playerTry != 0 || r.playerErr != "" {
				t.Fatal("cached frame or playback retry was not cleared")
			}
		})
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

func TestEmitterSessionReceivesRoutableReceiverHints(t *testing.T) {
	r := testReceiver()
	r.peers["remote"] = ReceiverInfo{ID: "remote", Name: "remote", Address: "10.80.0.4:7411"}
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
	if len(demand.Receivers) != 1 || demand.Receivers[0].ID != "remote" || demand.Receivers[0].Address != "10.80.0.4:7411" {
		t.Fatalf("unexpected receiver hints: %#v", demand.Receivers)
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
