package patvs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
