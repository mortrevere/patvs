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
type tuiHints []ReceiverInfo
type tuiStatus struct {
	receiverID string
	status     ReceiverStatus
	err        error
}
type tuiPeerStatus tuiStatus
type tuiAction struct {
	receiverID string
	message    string
}

type tuiModel struct {
	ctx            context.Context
	scanCtx        context.Context
	stopScan       context.CancelFunc
	client         controllerClient
	peers          []ReceiverInfo
	peerIndex      int
	overview       map[string]ReceiverStatus
	overviewFailed map[string]bool
	overviewBusy   map[string]bool
	overviewAt     map[string]time.Time
	selected       ReceiverInfo
	status         ReceiverStatus
	emitterIndex   int
	message        string
	peersFetching  bool
	statusFetching bool
	working        bool
	scanEvents     chan privateScanUpdate
	scan           privateScanUpdate
	scanStopped    bool
}

func newTUI(ctx context.Context, client controllerClient) *tea.Program {
	scanCtx, stopScan := context.WithCancel(ctx)
	model := &tuiModel{
		ctx: ctx, scanCtx: scanCtx, stopScan: stopScan,
		client: client, message: "Discovering receivers…",
		peersFetching: true, scanEvents: make(chan privateScanUpdate, 32),
		scan:     privateScanUpdate{Phase: "starting", Prefix: "10.0.0.0/24"},
		overview: make(map[string]ReceiverStatus), overviewFailed: make(map[string]bool),
		overviewBusy: make(map[string]bool), overviewAt: make(map[string]time.Time),
	}
	return tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
}

func (m *tuiModel) Init() tea.Cmd {
	scanCtx, events := m.scanCtx, m.scanEvents
	go scanPrivate(scanCtx, apiPort(m.client.cfg.APIAddr), func(update privateScanUpdate) {
		if update.Peer == nil {
			select {
			case events <- update:
			default:
			}
			return
		}
		select {
		case events <- update:
		case <-scanCtx.Done():
		}
	})
	return tea.Batch(m.fetchPeers(scanCtx), tuiTimer(), m.nextScanEvent())
}

func (m *tuiModel) nextScanEvent() tea.Cmd {
	scanCtx, events := m.scanCtx, m.scanEvents
	return func() tea.Msg {
		select {
		case update := <-events:
			return update
		case <-scanCtx.Done():
			return nil
		}
	}
}

func (m *tuiModel) fetchHints(peer ReceiverInfo) tea.Cmd {
	scanCtx := m.scanCtx
	return func() tea.Msg { return tuiHints(m.client.expandHints(scanCtx, []ReceiverInfo{peer})) }
}

func tuiTimer() tea.Cmd {
	return tea.Tick(2*time.Second, func(at time.Time) tea.Msg { return tuiTick(at) })
}

func (m *tuiModel) fetchPeers(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		peers, _ := discover(ctx, m.client.cfg, 2200*time.Millisecond)
		peers = m.client.expandHints(ctx, peers)
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

func (m *tuiModel) fetchPeerStatus(peer ReceiverInfo) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		var status ReceiverStatus
		err := m.client.request(ctx, peer, http.MethodGet, "/v1/status", nil, &status)
		return tuiPeerStatus{receiverID: peer.ID, status: status, err: err}
	}
}

func (m *tuiModel) refreshOverview(force bool) tea.Cmd {
	var commands []tea.Cmd
	for _, peer := range m.peers {
		if m.overviewBusy[peer.ID] || !force && time.Since(m.overviewAt[peer.ID]) < 5*time.Second {
			continue
		}
		m.overviewBusy[peer.ID] = true
		commands = append(commands, m.fetchPeerStatus(peer))
	}
	if len(commands) == 0 {
		return nil
	}
	return tea.Batch(commands...)
}

func (m *tuiModel) action(command, emitterID string) tea.Cmd {
	peer := m.selected
	return func() tea.Msg {
		var err error
		message := command + " complete"
		switch command {
		case "play":
			err = m.client.request(m.ctx, peer, http.MethodPut, "/v1/playback", map[string]string{"emitter_id": emitterID}, nil)
		case "snapshot":
			var result map[string]string
			err = m.client.request(m.ctx, peer, http.MethodPost, "/v1/snapshots/"+emitterID, nil, &result)
			if err == nil {
				message = "Saved " + result["path"]
			}
		case "stop":
			err = m.client.request(m.ctx, peer, http.MethodDelete, "/v1/playback", nil, nil)
		case "delete":
			err = m.client.request(m.ctx, peer, http.MethodDelete, "/v1/emitters/"+emitterID, nil, nil)
			message = "Offline emitter deleted"
		}
		return tuiAction{receiverID: peer.ID, message: actionResult(message, err)}
	}
}

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tuiTick:
		commands := []tea.Cmd{tuiTimer()}
		if command := m.refreshOverview(false); command != nil {
			commands = append(commands, command)
		}
		if !m.scanStopped && !m.peersFetching {
			m.peersFetching = true
			commands = append(commands, m.fetchPeers(m.scanCtx))
		}
		if m.selected.ID != "" && !m.statusFetching {
			m.statusFetching = true
			commands = append(commands, m.fetchStatus())
		}
		return m, tea.Batch(commands...)
	case tuiPeers:
		m.peersFetching = false
		m.mergePeers([]ReceiverInfo(msg))
		if !m.scanStopped {
			if m.selected.ID == "" && len(m.peers) == 0 {
				m.message = "No receivers found yet; checking again…"
			} else if m.selected.ID == "" {
				m.message = "Select a receiver"
			}
		}
		return m, m.refreshOverview(false)
	case tuiHints:
		m.mergePeers([]ReceiverInfo(msg))
		return m, m.refreshOverview(false)
	case privateScanUpdate:
		if m.scanStopped {
			return m, nil
		}
		m.scan = msg
		if msg.Peer != nil {
			m.mergePeers([]ReceiverInfo{*msg.Peer})
			return m, tea.Batch(m.nextScanEvent(), m.fetchHints(*msg.Peer), m.refreshOverview(false))
		}
		return m, m.nextScanEvent()
	case tuiPeerStatus:
		m.overviewBusy[msg.receiverID] = false
		m.overviewAt[msg.receiverID] = time.Now()
		m.overviewFailed[msg.receiverID] = msg.err != nil
		if msg.err == nil {
			m.overview[msg.receiverID] = msg.status
		} else {
			delete(m.overview, msg.receiverID)
		}
	case tuiStatus:
		if msg.receiverID != m.selected.ID {
			return m, nil
		}
		m.statusFetching = false
		if msg.err != nil {
			m.overviewFailed[msg.receiverID] = true
			delete(m.overview, msg.receiverID)
			m.message = "Error: " + msg.err.Error()
			return m, nil
		}
		previousID := ""
		old := sortedEmitters(m.status.Emitters)
		if m.emitterIndex < len(old) {
			previousID = old[m.emitterIndex].ID
		}
		m.status = msg.status
		m.overview[msg.receiverID] = msg.status
		m.overviewAt[msg.receiverID] = time.Now()
		m.overviewFailed[msg.receiverID] = false
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
		case "k":
			if !m.scanStopped {
				m.scanStopped = true
				m.stopScan()
				m.message = "Discovery stopped; press r for a single refresh"
			}
		case "esc", "backspace":
			if m.selected.ID != "" {
				m.selected = ReceiverInfo{}
				m.status = ReceiverStatus{}
				m.statusFetching = false
				m.working = false
				m.message = "Select a receiver"
			}
		case "up":
			if m.selected.ID == "" && m.peerIndex > 0 {
				m.peerIndex--
			} else if m.selected.ID != "" && m.emitterIndex > 0 {
				m.emitterIndex--
			}
		case "down":
			if m.selected.ID == "" && m.peerIndex+1 < len(m.peers) {
				m.peerIndex++
			} else if m.selected.ID != "" && m.emitterIndex+1 < len(m.status.Emitters) {
				m.emitterIndex++
			}
		case "enter", "p", "n", "x", "d":
			if m.selected.ID == "" && msg.String() == "enter" && m.peerIndex < len(m.peers) {
				m.selected = m.peers[m.peerIndex]
				m.status = ReceiverStatus{}
				m.emitterIndex = 0
				m.message = "Loading…"
				m.statusFetching = true
				return m, m.fetchStatus()
			}
			if m.selected.ID == "" || m.working {
				break
			}
			if msg.String() == "x" {
				m.working = true
				m.message = "Stopping playback…"
				return m, m.action("stop", "")
			}
			emitters := sortedEmitters(m.status.Emitters)
			if m.emitterIndex >= len(emitters) {
				break
			}
			emitter := emitters[m.emitterIndex]
			if msg.String() == "d" {
				if emitter.Online {
					m.message = emitter.Name + " is online; only offline emitters can be deleted"
					break
				}
				m.working = true
				m.message = "Deleting " + emitter.Name + "…"
				return m, m.action("delete", emitter.ID)
			}
			if !emitter.Online {
				m.message = emitter.Name + " is offline"
				break
			}
			m.working = true
			switch msg.String() {
			case "enter", "p":
				m.message = "Starting playback…"
				return m, m.action("play", emitter.ID)
			case "n":
				m.message = "Taking snapshot…"
				return m, m.action("snapshot", emitter.ID)
			}
		case "r":
			if m.selected.ID != "" && !m.statusFetching {
				m.statusFetching = true
				return m, m.fetchStatus()
			}
			if m.selected.ID == "" && !m.peersFetching {
				m.peersFetching = true
				var peers tea.Cmd
				if m.scanStopped {
					peers = m.fetchPeers(m.ctx)
				} else {
					peers = m.fetchPeers(m.scanCtx)
				}
				return m, tea.Batch(peers, m.refreshOverview(true))
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
			summary := "checking…"
			if m.overviewFailed[peer.ID] {
				summary = "status unavailable"
			} else if status, ok := m.overview[peer.ID]; ok {
				summary = receiverFeedSummary(status)
			}
			fmt.Fprintf(&view, "%s%-20s %-21s %s\n", cursor, peer.Name, peer.Address, summary)
		}
		if len(m.peers) == 0 {
			if m.scanStopped {
				view.WriteString("  No receivers discovered\n")
			} else {
				view.WriteString("  Searching…\n")
			}
		}
		view.WriteString("\n↑/↓ select · Enter open · k stop scan · r refresh · q quit\n")
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
		view.WriteString("\nHost FFmpeg/VLC processes:\n")
		if len(m.status.Processes) == 0 {
			view.WriteString("  none\n")
		}
		for _, process := range m.status.Processes {
			owned := ""
			if process.PID == m.status.PlayerPID {
				owned = " (patvs player)"
			}
			fmt.Fprintf(&view, "  %s pid=%d%s\n", process.Name, process.PID, owned)
			command := []rune(process.Command)
			if len(command) > 110 {
				command = append(command[:109], '…')
			}
			fmt.Fprintf(&view, "    %s\n", string(command))
		}
		view.WriteString("\n↑/↓ select · Enter play in VLC · n snapshot · x stop playback\nd delete offline emitter · Esc back · k stop scan · r refresh · q quit\n")
	}
	fmt.Fprintf(&view, "\n%s\n", m.message)
	if m.scanStopped {
		fmt.Fprintf(&view, "RFC1918 scan stopped: %d / %d probes · %d receivers\n",
			m.scan.Checked, privateScanTotal, len(m.peers))
	} else if m.scan.Done {
		fmt.Fprintf(&view, "RFC1918 scan complete: %d / %d addresses\n", m.scan.Checked, privateScanTotal)
	} else {
		fmt.Fprintf(&view, "RFC1918 scan %s: %d / %d probes · %s · %d receivers\n",
			m.scan.Phase, m.scan.Checked, privateScanTotal, m.scan.Prefix, len(m.peers))
	}
	return view.String()
}

func receiverFeedSummary(status ReceiverStatus) string {
	active := make([]string, 0, 1)
	waiting := make([]string, 0, 1)
	ids := make(map[string]bool)
	if status.Playback != "" {
		ids[status.Playback] = true
	}
	for id, enabled := range status.Streams {
		if enabled {
			ids[id] = true
		}
	}
	for id := range ids {
		emitter := status.Emitters[id]
		name := emitter.Name
		if name == "" {
			name = id
		}
		if emitter.Streaming {
			if status.Playback == id && status.PlayerPID != 0 {
				name += " (VLC)"
			}
			active = append(active, name)
		} else {
			waiting = append(waiting, name)
		}
	}
	sort.Strings(active)
	sort.Strings(waiting)
	if len(active) == 0 && len(waiting) == 0 {
		return "idle"
	}
	parts := make([]string, 0, 2)
	if len(active) != 0 {
		parts = append(parts, "← "+strings.Join(active, ", "))
	}
	if len(waiting) != 0 {
		parts = append(parts, "waiting: "+strings.Join(waiting, ", "))
	}
	return strings.Join(parts, " · ")
}

func (m *tuiModel) mergePeers(incoming []ReceiverInfo) {
	selectedID := ""
	if m.peerIndex < len(m.peers) {
		selectedID = m.peers[m.peerIndex].ID
	}
	known := make(map[string]ReceiverInfo, len(m.peers)+len(incoming))
	for _, peer := range m.peers {
		known[peer.ID] = peer
	}
	for _, peer := range incoming {
		old, exists := known[peer.ID]
		if !exists || routableEndpoint(peer.Address) && !routableEndpoint(old.Address) ||
			peer.LastSeen.After(old.LastSeen) && routableEndpoint(peer.Address) == routableEndpoint(old.Address) {
			known[peer.ID] = peer
		}
	}
	m.peers = m.peers[:0]
	for _, peer := range known {
		m.peers = append(m.peers, peer)
	}
	sort.Slice(m.peers, func(i, j int) bool { return m.peers[i].Name < m.peers[j].Name })
	for i, peer := range m.peers {
		if peer.ID == selectedID {
			m.peerIndex = i
		}
		if peer.ID == m.selected.ID {
			m.selected = peer
		}
	}
	if m.peerIndex >= len(m.peers) {
		m.peerIndex = max(0, len(m.peers)-1)
	}
}
