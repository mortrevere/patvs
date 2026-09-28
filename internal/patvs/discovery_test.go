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
