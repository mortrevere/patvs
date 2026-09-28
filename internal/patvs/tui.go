package patvs

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type tuiTick time.Time
type tuiPeers []ReceiverInfo
type tuiStatus struct {
	receiverID string
	status     ReceiverStatus
	err        error
}
type tuiAction struct {
	receiverID string
	message    string
}

type tuiModel struct {
	ctx            context.Context
	client         controllerClient
	peers          []ReceiverInfo
	peerIndex      int
	selected       ReceiverInfo
	status         ReceiverStatus
	emitterIndex   int
	message        string
	peersFetching  bool
	statusFetching bool
	working        bool
}

func newTUI(ctx context.Context, client controllerClient) *tea.Program {
	model := &tuiModel{ctx: ctx, client: client, message: "Discovering receivers…", peersFetching: true}
	return tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(m.fetchPeers(), tuiTimer())
}

func tuiTimer() tea.Cmd {
	return tea.Tick(2*time.Second, func(at time.Time) tea.Msg { return tuiTick(at) })
}

func (m *tuiModel) fetchPeers() tea.Cmd {
	return func() tea.Msg {
		peers, _ := discover(m.ctx, m.client.cfg, 2200*time.Millisecond)
		peers = m.client.expandHints(m.ctx, peers)
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
		return tuiPeers(peers)
	}
}

func (m *tuiModel) fetchStatus() tea.Cmd {
	peer := m.selected
	return func() tea.Msg {
		var status ReceiverStatus
		err := m.client.request(m.ctx, peer, http.MethodGet, "/v1/status", nil, &status)
		return tuiStatus{receiverID: peer.ID, status: status, err: err}
	}
}

func (m *tuiModel) action(command, emitterID string, enabled bool) tea.Cmd {
	peer := m.selected
	return func() tea.Msg {
		var err error
		message := command + " complete"
		switch command {
		case "play":
			err = m.client.request(m.ctx, peer, http.MethodPut, "/v1/playback", map[string]string{"emitter_id": emitterID}, nil)
		case "stream":
			err = m.client.request(m.ctx, peer, http.MethodPut, "/v1/streams/"+emitterID, map[string]bool{"enabled": enabled}, nil)
		case "snapshot":
			var result map[string]string
			err = m.client.request(m.ctx, peer, http.MethodPost, "/v1/snapshots/"+emitterID, nil, &result)
			if err == nil {
				message = "Saved " + result["path"]
			}
		case "stop":
			err = m.client.request(m.ctx, peer, http.MethodDelete, "/v1/playback", nil, nil)
		}
		return tuiAction{receiverID: peer.ID, message: actionResult(message, err)}
	}
}

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tuiTick:
		commands := []tea.Cmd{tuiTimer()}
		if !m.peersFetching {
			m.peersFetching = true
			commands = append(commands, m.fetchPeers())
		}
		if m.selected.ID != "" && !m.statusFetching {
			m.statusFetching = true
			commands = append(commands, m.fetchStatus())
		}
		return m, tea.Batch(commands...)
	case tuiPeers:
		m.peersFetching = false
		previousID := ""
		if m.peerIndex < len(m.peers) {
			previousID = m.peers[m.peerIndex].ID
		}
		m.peers = []ReceiverInfo(msg)
		for i, peer := range m.peers {
			if peer.ID == previousID {
				m.peerIndex = i
			}
			if peer.ID == m.selected.ID {
				m.selected = peer
			}
		}
		if m.peerIndex >= len(m.peers) {
			m.peerIndex = max(0, len(m.peers)-1)
		}
		if m.selected.ID == "" && len(m.peers) == 0 {
			m.message = "No receivers found yet; checking again…"
		} else if m.selected.ID == "" {
			m.message = "Select a receiver"
		}
	case tuiStatus:
		if msg.receiverID != m.selected.ID {
			return m, nil
		}
		m.statusFetching = false
		if msg.err != nil {
			m.message = "Error: " + msg.err.Error()
			return m, nil
		}
		previousID := ""
		old := sortedEmitters(m.status.Emitters)
		if m.emitterIndex < len(old) {
			previousID = old[m.emitterIndex].ID
		}
		m.status = msg.status
		if m.message == "Loading…" || strings.HasPrefix(m.message, "Error: ") {
			m.message = "Ready"
		}
		emitters := sortedEmitters(m.status.Emitters)
		for i, emitter := range emitters {
			if emitter.ID == previousID {
				m.emitterIndex = i
				break
			}
		}
		if m.emitterIndex >= len(emitters) {
			m.emitterIndex = max(0, len(emitters)-1)
		}
	case tuiAction:
		if msg.receiverID != m.selected.ID {
			return m, nil
		}
		m.working = false
		m.message = msg.message
		if !m.statusFetching {
			m.statusFetching = true
			return m, m.fetchStatus()
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc", "backspace":
			if m.selected.ID != "" {
				m.selected = ReceiverInfo{}
				m.status = ReceiverStatus{}
				m.statusFetching = false
				m.working = false
				m.message = "Select a receiver"
			}
		case "up", "k":
			if m.selected.ID == "" && m.peerIndex > 0 {
				m.peerIndex--
			} else if m.selected.ID != "" && m.emitterIndex > 0 {
				m.emitterIndex--
			}
		case "down", "j":
			if m.selected.ID == "" && m.peerIndex+1 < len(m.peers) {
				m.peerIndex++
			} else if m.selected.ID != "" && m.emitterIndex+1 < len(m.status.Emitters) {
				m.emitterIndex++
			}
		case "enter":
			if m.selected.ID == "" && m.peerIndex < len(m.peers) {
				m.selected = m.peers[m.peerIndex]
				m.status = ReceiverStatus{}
				m.emitterIndex = 0
				m.message = "Loading…"
				m.statusFetching = true
				return m, m.fetchStatus()
			}
		case "r":
			if m.selected.ID != "" && !m.statusFetching {
				m.statusFetching = true
				return m, m.fetchStatus()
			}
			if m.selected.ID == "" && !m.peersFetching {
				m.peersFetching = true
				return m, m.fetchPeers()
			}
		case "p", "s", "n", "x":
			if m.selected.ID == "" || m.working {
				break
			}
			if msg.String() == "x" {
				m.working = true
				m.message = "Stopping playback…"
				return m, m.action("stop", "", false)
			}
			emitters := sortedEmitters(m.status.Emitters)
			if m.emitterIndex >= len(emitters) {
				break
			}
			emitter := emitters[m.emitterIndex]
			if !emitter.Online {
				m.message = emitter.Name + " is offline"
				break
			}
			m.working = true
			switch msg.String() {
			case "p":
				m.message = "Starting playback…"
				return m, m.action("play", emitter.ID, false)
			case "s":
				enabled := !m.status.Streams[emitter.ID]
				m.message = "Updating stream…"
				return m, m.action("stream", emitter.ID, enabled)
			case "n":
				m.message = "Taking snapshot…"
				return m, m.action("snapshot", emitter.ID, false)
			}
		}
	}
	return m, nil
}

func (m *tuiModel) View() string {
	var view strings.Builder
	view.WriteString("patvs controller\n\n")
	if m.selected.ID == "" {
		for i, peer := range m.peers {
			cursor := "  "
			if i == m.peerIndex {
				cursor = "> "
			}
			fmt.Fprintf(&view, "%s%-20s %s\n", cursor, peer.Name, peer.Address)
		}
		if len(m.peers) == 0 {
			view.WriteString("  Searching…\n")
		}
		view.WriteString("\n↑/↓ or j/k select · Enter open · r refresh · q quit\n")
	} else {
		fmt.Fprintf(&view, "Receiver: %s (%s)\n\n", m.selected.Name, m.selected.Address)
		emitters := sortedEmitters(m.status.Emitters)
		for i, emitter := range emitters {
			cursor, state := "  ", "offline"
			if i == m.emitterIndex {
				cursor = "> "
			}
			if emitter.Online {
				state = "online"
			}
			flags := ""
			if m.status.Streams[emitter.ID] {
				flags += " stream"
			}
			if m.status.Playback == emitter.ID {
				flags += " playing"
			}
			seen := "never"
			if !emitter.LastSeen.IsZero() {
				seen = emitter.LastSeen.Format("15:04:05")
			}
			fmt.Fprintf(&view, "%s%-20s %-7s %dx%d %s seen %s%s\n", cursor, emitter.Name, state, emitter.Camera.Width, emitter.Camera.Height, emitter.Camera.Format, seen, flags)
			if emitter.LastError != "" {
				fmt.Fprintf(&view, "    Camera error: %s\n", emitter.LastError)
			}
		}
		if len(emitters) == 0 {
			view.WriteString("  No emitters seen yet\n")
		}
		if m.status.PlayerErr != "" {
			fmt.Fprintf(&view, "\nPlayer error: %s\n", m.status.PlayerErr)
		}
		view.WriteString("\n↑/↓ select · p play · s stream on/off · n snapshot · x stop playback\nEsc back · r refresh · q quit\n")
	}
	fmt.Fprintf(&view, "\n%s\n", m.message)
	return view.String()
}
