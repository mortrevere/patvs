package patvs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeReceiverAcceptsOnlyPatvsHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, Identity{ID: "receiver-id", Name: "screen"})
	}))
	defer server.Close()
	peer, ok := probeReceiver(context.Background(), server.Client(), strings.TrimPrefix(server.URL, "http://"))
	if !ok || peer.ID != "receiver-id" || peer.Name != "screen" {
		t.Fatalf("unexpected probe result: %#v, %t", peer, ok)
	}
}

func TestProbeReceiverRejectsUnrelatedHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"not-patvs"}`))
	}))
	defer server.Close()
	if _, ok := probeReceiver(context.Background(), server.Client(), strings.TrimPrefix(server.URL, "http://")); ok {
		t.Fatal("accepted response without a patvs receiver ID")
	}
}

func TestRoutableEndpoint(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"192.168.1.20:7411":     true,
		"receiver.example:7411": true,
		"127.0.0.1:7411":        false,
		"[fe80::1%eth0]:7411":   false,
	} {
		if got := routableEndpoint(endpoint); got != want {
			t.Errorf("routableEndpoint(%q) = %t, want %t", endpoint, got, want)
		}
	}
}
