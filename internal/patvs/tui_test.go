package patvs

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIDeletesSelectedOfflineEmitter(t *testing.T) {
	r := testReceiver()
	r.cfg.StatePath = filepath.Join(t.TempDir(), "receiver.json")
	for _, id := range []string{"a", "b", "c"} {
		r.state.Emitters[id] = EmitterInfo{ID: id, Name: "duplicate-camera"}
	}
	server := httptest.NewServer(r.apiHandler())
	defer server.Close()
	m := &tuiModel{
		ctx: context.Background(), client: controllerClient{cfg: Config{Secret: "secret"}, http: *server.Client()},
		selected: ReceiverInfo{ID: "receiver", Address: strings.TrimPrefix(server.URL, "http://")},
		status:   ReceiverStatus{Emitters: cloneEmitters(r.state.Emitters)}, emitterIndex: 1,
		overview: make(map[string]ReceiverStatus), overviewAt: make(map[string]time.Time), overviewFailed: make(map[string]bool),
	}
	for range 20 {
		emitters := sortedEmitters(m.status.Emitters)
		if emitters[0].ID != "a" || emitters[1].ID != "b" || emitters[2].ID != "c" {
			t.Fatalf("duplicate names have unstable order: %#v", emitters)
		}
	}
	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	if !strings.Contains(m.View(), "d delete offline emitter") {
		t.Fatal("delete key missing from help")
	}
	_, command := m.Update(key)
	if command == nil || !m.working {
		t.Fatal("d did not start deletion")
	}
	_, refresh := m.Update(command())
	if refresh == nil || m.working || m.message != "Offline emitter deleted" {
		t.Fatalf("deletion did not finish and refresh: %s", m.message)
	}
	m.Update(refresh())
	if len(m.status.Emitters) != 2 || m.status.Emitters["a"].ID != "a" || m.status.Emitters["c"].ID != "c" || m.emitterIndex != 1 {
		t.Fatalf("wrong row removed or selection invalid after refresh: %#v", m.status)
	}
	online := m.status.Emitters["c"]
	online.Online = true
	m.status.Emitters["c"] = online
	if _, command := m.Update(key); command != nil || m.working || !strings.Contains(m.message, "is online") {
		t.Fatal("TUI allowed deletion of an online emitter")
	}
	m.status.Emitters = nil
	m.emitterIndex = 0
	if _, command := m.Update(key); command != nil || m.working {
		t.Fatal("TUI attempted deletion with no emitter selected")
	}
}
