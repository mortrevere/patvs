package patvs

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxFrameSize = 8 << 20

type receiverDiskState struct {
	Identity Identity               `json:"identity"`
	Emitters map[string]EmitterInfo `json:"emitters"`
	Streams  map[string]bool        `json:"streams"`
	Playback string                 `json:"playback,omitempty"`
}

type emitterSession struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *emitterSession) send(message sessionMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	err := s.conn.WriteJSON(message)
	if err != nil {
		slog.Warn("send emitter command failed", "address", s.conn.RemoteAddr(), "command", message.Type, "error", err)
	} else {
		slog.Debug("emitter command sent", "address", s.conn.RemoteAddr(), "command", message.Type, "stream", message.Stream, "request", message.Request)
	}
	return err
}

type receiver struct {
	cfg       Config
	mu        sync.RWMutex
	state     receiverDiskState
	sessions  map[string]*emitterSession
	sessionWG sync.WaitGroup
	latest    map[string][]byte
	waiters   map[string][]chan []byte
	listeners map[string]map[chan []byte]struct{}
	peers     map[string]ReceiverInfo
	player    *exec.Cmd
	playerErr string
	playerTry int
	playerAt  time.Time
	dirty     bool
}

func RunReceiver(ctx context.Context, cfg Config) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	slog.Info("receiver starting", "name", cfg.Name, "discovery_port", cfg.DiscoveryPort)
	slog.Info("state file", "role", "receiver", "path", cfg.StatePath)
	defer slog.Info("receiver stopped")
	r := &receiver{
		cfg: cfg, sessions: make(map[string]*emitterSession), latest: make(map[string][]byte),
		waiters: make(map[string][]chan []byte), listeners: make(map[string]map[chan []byte]struct{}), peers: make(map[string]ReceiverInfo),
	}
	if err := loadJSON(cfg.StatePath, &r.state); err != nil {
		return err
	}
	if r.state.Identity.ID == "" {
		identity, err := loadIdentity(cfg.StatePath, cfg.Name)
		if err != nil {
			return err
		}
		r.state.Identity = identity
	}
	r.state.Identity.Name = cfg.Name
	if cfg.Reset {
		slog.Info("forgetting remembered emitters and playback settings", "emitters", len(r.state.Emitters))
		r.state.Emitters = nil
		r.state.Streams = nil
		r.state.Playback = ""
	}
	if r.state.Emitters == nil {
		r.state.Emitters = make(map[string]EmitterInfo)
	}
	if r.state.Streams == nil {
		r.state.Streams = make(map[string]bool)
	}
	for id, emitter := range r.state.Emitters {
		emitter.Online = false
		emitter.Streaming = false
		r.state.Emitters[id] = emitter
	}
	if err := r.save(); err != nil {
		return err
	}
	baseContext := func(net.Listener) context.Context { return ctx }
	api := &http.Server{Addr: cfg.APIAddr, Handler: r.apiHandler(), ReadHeaderTimeout: 5 * time.Second, BaseContext: baseContext}
	stream := &http.Server{Addr: cfg.StreamAddr, Handler: r.streamHandler(), ReadHeaderTimeout: 5 * time.Second, BaseContext: baseContext}
	var wg sync.WaitGroup
	defer func() {
		slog.Info("receiver shutting down")
		cancel()
		// Shutdown does not close hijacked WebSocket connections.
		r.mu.Lock()
		for _, session := range r.sessions {
			_ = session.conn.Close()
		}
		r.mu.Unlock()
		shutdownCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		if api.Shutdown(shutdownCtx) != nil {
			_ = api.Close()
		}
		if stream.Shutdown(shutdownCtx) != nil {
			_ = stream.Close()
		}
		wg.Wait()
		r.sessionWG.Wait()
		r.stopPlayer()
		err = errors.Join(err, r.save())
	}()
	if err := startDiscoveryResponder(ctx, cfg, r.state.Identity); err != nil {
		return err
	}
	errorsCh := make(chan error, 2)
	wg.Add(4)
	go func() { defer wg.Done(); errorsCh <- normalizeServerError(api.ListenAndServe()) }()
	go func() { defer wg.Done(); errorsCh <- normalizeServerError(stream.ListenAndServe()) }()
	go func() { defer wg.Done(); r.maintenance(ctx) }()
	go func() { defer wg.Done(); r.discoverPeers(ctx) }()

	slog.Info("receiver ready", "id", r.state.Identity.ID, "api", cfg.APIAddr, "streams", cfg.StreamAddr)
	select {
	case <-ctx.Done():
		return nil
	case err := <-errorsCh:
		return err
	}
}

func normalizeServerError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (r *receiver) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, r.state.Identity) })
	mux.Handle("GET /v1/status", r.auth(http.HandlerFunc(r.handleStatus)))
	mux.Handle("GET /v1/emitters", r.auth(http.HandlerFunc(r.handleEmitters)))
	mux.Handle("PUT /v1/streams/{id}", r.auth(http.HandlerFunc(r.handleStreamIntent)))
	mux.Handle("POST /v1/snapshots/{id}", r.auth(http.HandlerFunc(r.handleSnapshot)))
	mux.Handle("PUT /v1/playback", r.auth(http.HandlerFunc(r.handlePlayback)))
	mux.Handle("DELETE /v1/playback", r.auth(http.HandlerFunc(r.handlePlayback)))
	mux.Handle("GET /v1/session", r.auth(http.HandlerFunc(r.handleSession)))
	return mux
}

func (r *receiver) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(r.cfg.Secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(r.cfg.Secret)) != 1 {
			slog.Warn("unauthorized API request", "address", request.RemoteAddr, "method", request.Method, "path", request.URL.Path)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (r *receiver) handleStatus(w http.ResponseWriter, _ *http.Request) {
	r.mu.RLock()
	status := ReceiverStatus{Identity: r.state.Identity, Emitters: cloneEmitters(r.state.Emitters), Streams: cloneBools(r.state.Streams), Playback: r.state.Playback, PlayerErr: r.playerErr, Receivers: r.receiverHintsLocked()}
	if r.player != nil && r.player.Process != nil {
		status.PlayerPID = r.player.Process.Pid
	}
	r.mu.RUnlock()
	status.Processes = listMediaProcesses()
	writeJSON(w, status)
}

func (r *receiver) handleEmitters(w http.ResponseWriter, _ *http.Request) {
	r.mu.RLock()
	emitters := cloneEmitters(r.state.Emitters)
	r.mu.RUnlock()
	writeJSON(w, emitters)
}

func (r *receiver) handleStreamIntent(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(w, request, &body); err != nil {
		return
	}
	id := request.PathValue("id")
	r.mu.Lock()
	_, known := r.state.Emitters[id]
	if known {
		r.state.Streams[id] = body.Enabled
		if !body.Enabled && r.state.Playback != id {
			emitter := r.state.Emitters[id]
			emitter.Streaming = false
			r.state.Emitters[id] = emitter
		}
		r.dirty = true
	}
	wantStream := body.Enabled || r.state.Playback == id
	session := r.sessions[id]
	r.mu.Unlock()
	if !known {
		http.Error(w, "unknown emitter", http.StatusNotFound)
		return
	}
	slog.Info("stream requested", "emitter_id", id, "enabled", body.Enabled, "online", session != nil)
	if session != nil {
		_ = session.send(sessionMessage{Type: "demand", Stream: wantStream})
	}
	_ = r.save()
	writeJSON(w, map[string]bool{"enabled": body.Enabled})
}

func (r *receiver) handleSnapshot(w http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	slog.Info("snapshot requested", "emitter_id", id)
	result := make(chan []byte, 1)
	r.mu.Lock()
	session := r.sessions[id]
	if session != nil {
		r.waiters[id] = append(r.waiters[id], result)
	}
	r.mu.Unlock()
	if session == nil {
		slog.Warn("snapshot failed; emitter offline", "emitter_id", id)
		http.Error(w, "emitter is offline", http.StatusServiceUnavailable)
		return
	}
	defer r.removeWaiter(id, result)
	if err := session.send(sessionMessage{Type: "snapshot", Request: fmt.Sprintf("%d", time.Now().UnixNano())}); err != nil {
		http.Error(w, "request snapshot: "+err.Error(), http.StatusBadGateway)
		return
	}
	select {
	case frame := <-result:
		path, err := r.saveSnapshot(id, frame)
		if err != nil {
			slog.Warn("save snapshot failed", "emitter_id", id, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("snapshot saved", "emitter_id", id, "path", path, "bytes", len(frame))
		writeJSON(w, map[string]string{"path": path})
	case <-request.Context().Done():
		slog.Debug("snapshot request canceled", "emitter_id", id)
	case <-time.After(10 * time.Second):
		slog.Warn("snapshot timed out", "emitter_id", id)
		http.Error(w, "snapshot timed out", http.StatusGatewayTimeout)
	}
}

func (r *receiver) handlePlayback(w http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodDelete {
		r.mu.Lock()
		old := r.state.Playback
		r.state.Playback = ""
		delete(r.state.Streams, old)
		if emitter, ok := r.state.Emitters[old]; ok {
			emitter.Streaming = false
			r.state.Emitters[old] = emitter
		}
		session := r.sessions[old]
		r.playerTry = 0
		r.playerErr = ""
		r.dirty = true
		r.mu.Unlock()
		slog.Info("playback stop requested", "emitter_id", old)
		r.stopPlayer()
		if session != nil {
			_ = session.send(sessionMessage{Type: "demand", Stream: false})
		}
		_ = r.save()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		EmitterID string `json:"emitter_id"`
	}
	if err := decodeJSON(w, request, &body); err != nil {
		return
	}
	r.mu.Lock()
	_, known := r.state.Emitters[body.EmitterID]
	old := r.state.Playback
	if known {
		r.state.Playback = body.EmitterID
		if old != "" && old != body.EmitterID {
			delete(r.state.Streams, old)
			emitter := r.state.Emitters[old]
			emitter.Streaming = false
			r.state.Emitters[old] = emitter
		}
		r.playerTry = 0
		r.playerAt = time.Time{}
		r.dirty = true
	}
	session := r.sessions[body.EmitterID]
	oldSession := r.sessions[old]
	r.mu.Unlock()
	if !known {
		http.Error(w, "unknown emitter", http.StatusNotFound)
		return
	}
	slog.Info("playback requested", "emitter_id", body.EmitterID, "previous_emitter_id", old, "online", session != nil)
	if old != body.EmitterID && oldSession != nil {
		_ = oldSession.send(sessionMessage{Type: "demand", Stream: false})
	}
	if session != nil {
		_ = session.send(sessionMessage{Type: "demand", Stream: true})
	}
	if err := r.startPlayer(body.EmitterID); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = r.save()
	writeJSON(w, map[string]string{"emitter_id": body.EmitterID})
}

var upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(*http.Request) bool { return true }}

func (r *receiver) handleSession(w http.ResponseWriter, request *http.Request) {
	conn, err := upgrader.Upgrade(w, request, nil)
	if err != nil {
		slog.Warn("emitter session upgrade failed", "address", request.RemoteAddr, "error", err)
		return
	}
	defer conn.Close()
	stopClose := context.AfterFunc(request.Context(), func() { _ = conn.Close() })
	defer stopClose()
	conn.SetReadLimit(maxFrameSize)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var register sessionMessage
	if err := conn.ReadJSON(&register); err != nil || register.Type != "register" || register.ID == "" {
		slog.Warn("invalid emitter registration", "address", request.RemoteAddr, "type", register.Type, "error", err)
		return
	}
	session := &emitterSession{conn: conn}
	now := time.Now()
	r.mu.Lock()
	if request.Context().Err() != nil {
		r.mu.Unlock()
		return
	}
	r.sessionWG.Add(1)
	defer r.sessionWG.Done()
	if old := r.sessions[register.ID]; old != nil {
		slog.Info("replacing emitter session", "emitter_id", register.ID)
		_ = old.conn.Close()
	}
	r.sessions[register.ID] = session
	emitter := r.state.Emitters[register.ID]
	emitter.ID, emitter.Name, emitter.Camera = register.ID, register.Name, register.Camera
	emitter.Online, emitter.LastSeen, emitter.LastError = true, now, ""
	r.state.Emitters[register.ID] = emitter
	wantStream := r.state.Streams[register.ID]
	wantPlayback := r.state.Playback == register.ID
	r.dirty = true
	r.mu.Unlock()
	r.mu.RLock()
	hints := r.receiverHintsLocked()
	r.mu.RUnlock()
	_ = session.send(sessionMessage{Type: "demand", Stream: wantStream || wantPlayback, Receivers: hints})
	if wantPlayback {
		_ = r.startPlayer(register.ID)
	}
	slog.Info("emitter connected", "id", register.ID, "name", register.Name, "address", request.RemoteAddr, "camera", register.Camera, "stream", wantStream || wantPlayback)

	defer func() {
		r.mu.Lock()
		if r.sessions[register.ID] == session {
			delete(r.sessions, register.ID)
			emitter := r.state.Emitters[register.ID]
			emitter.Online, emitter.Streaming = false, false
			r.state.Emitters[register.ID] = emitter
			r.dirty = true
		}
		r.mu.Unlock()
		slog.Info("emitter disconnected", "emitter_id", register.ID, "address", request.RemoteAddr)
	}()

	firstFrame := true
	for {
		_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		kind, data, err := conn.ReadMessage()
		if err != nil {
			slog.Debug("emitter session read ended", "emitter_id", register.ID, "error", err)
			return
		}
		switch kind {
		case websocket.TextMessage:
			var message sessionMessage
			if json.Unmarshal(data, &message) != nil || message.Type != "heartbeat" {
				continue
			}
			r.mu.Lock()
			emitter := r.state.Emitters[register.ID]
			if emitter.LastError != message.Error {
				if message.Error != "" {
					slog.Warn("emitter capture error", "emitter_id", register.ID, "error", message.Error)
				} else {
					slog.Info("emitter capture recovered", "emitter_id", register.ID)
				}
			}
			emitter.Online, emitter.LastSeen, emitter.Camera, emitter.LastError = true, time.Now(), message.Camera, message.Error
			r.state.Emitters[register.ID] = emitter
			r.dirty = true
			r.mu.Unlock()
		case websocket.BinaryMessage:
			if !validJPEGFrame(data) {
				slog.Debug("invalid JPEG frame discarded", "emitter_id", register.ID, "bytes", len(data))
				continue
			}
			if firstFrame {
				slog.Debug("first frame received", "emitter_id", register.ID, "bytes", len(data))
				firstFrame = false
			}
			r.acceptFrame(register.ID, data)
		}
	}
}

func validJPEGFrame(data []byte) bool {
	return len(data) >= 4 && len(data) <= maxFrameSize && data[0] == 0xff && data[1] == 0xd8 && data[len(data)-2] == 0xff && data[len(data)-1] == 0xd9
}

func (r *receiver) acceptFrame(id string, data []byte) {
	frame := append([]byte(nil), data...)
	r.mu.Lock()
	r.latest[id] = frame
	emitter := r.state.Emitters[id]
	emitter.Streaming = r.state.Streams[id] || r.state.Playback == id
	r.state.Emitters[id] = emitter
	waiters := r.waiters[id]
	delete(r.waiters, id)
	listeners := make([]chan []byte, 0, len(r.listeners[id]))
	for listener := range r.listeners[id] {
		listeners = append(listeners, listener)
	}
	r.mu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter <- frame:
		default:
		}
	}
	for _, listener := range listeners {
		select {
		case listener <- frame:
		default:
			select {
			case <-listener:
			default:
			}
			select {
			case listener <- frame:
			default:
			}
		}
	}
}

func (r *receiver) removeWaiter(id string, target chan []byte) {
	r.mu.Lock()
	waiters := r.waiters[id]
	for index, waiter := range waiters {
		if waiter == target {
			waiters = append(waiters[:index], waiters[index+1:]...)
			break
		}
	}
	if len(waiters) == 0 {
		delete(r.waiters, id)
	} else {
		r.waiters[id] = waiters
	}
	r.mu.Unlock()
}

func (r *receiver) streamHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /streams/", r.handleMJPEG)
	return mux
}

func (r *receiver) handleMJPEG(w http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(request.URL.Path, "/streams/")
	if !strings.HasSuffix(name, ".mjpg") || strings.Contains(strings.TrimSuffix(name, ".mjpg"), "/") {
		http.NotFound(w, request)
		return
	}
	id := strings.TrimSuffix(name, ".mjpg")
	if id == "" {
		http.NotFound(w, request)
		return
	}
	frames := make(chan []byte, 1)
	r.mu.Lock()
	if r.listeners[id] == nil {
		r.listeners[id] = make(map[chan []byte]struct{})
	}
	r.listeners[id][frames] = struct{}{}
	latest := r.latest[id]
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.listeners[id], frames)
		r.mu.Unlock()
	}()
	if latest != nil {
		frames <- latest
	}
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=patvs")
	w.Header().Set("Cache-Control", "no-store")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	for {
		select {
		case frame := <-frames:
			if _, err := fmt.Fprintf(w, "--patvs\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame)); err != nil {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\r\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (r *receiver) saveSnapshot(id string, frame []byte) (string, error) {
	if err := os.MkdirAll(r.cfg.SnapshotDir, 0o755); err != nil {
		return "", fmt.Errorf("create snapshot directory: %w", err)
	}
	name := fmt.Sprintf("%s-%s.jpg", safeName(id), time.Now().Format("20060102-150405.000"))
	path := filepath.Join(r.cfg.SnapshotDir, name)
	tmp, err := os.CreateTemp(r.cfg.SnapshotDir, ".snapshot-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(frame); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		return "", fmt.Errorf("save snapshot: %w", err)
	}
	return path, nil
}

func (r *receiver) playerCommand(id string) *exec.Cmd {
	url := "http://" + r.cfg.StreamAddr + "/streams/" + id + ".mjpg"
	args := []string{"--ignore-config", "--intf=dummy", "--no-embedded-video", "--video-title=patvs-playback", "--fullscreen", "--autoscale", "--no-video-title-show", "--network-caching=150", "--no-audio", url}
	if r.cfg.DisplayAspectRatio != "" {
		args = append([]string{"--aspect-ratio=" + r.cfg.DisplayAspectRatio}, args...)
	}
	if runtime.GOOS == "windows" {
		args = append([]string{"--no-one-instance"}, args...)
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		_, drmErr := os.Stat("/dev/dri/card0")
		_, fbErr := os.Stat("/dev/fb0")
		switch {
		case drmErr == nil && fbErr == nil:
			args = append([]string{"--vout=drm_vout,fb", "--no-fb-tty", "--fbdev=/dev/fb0"}, args...)
		case drmErr == nil:
			args = append([]string{"--vout=drm_vout"}, args...)
		case fbErr == nil:
			args = append([]string{"--vout=fb", "--no-fb-tty", "--fbdev=/dev/fb0"}, args...)
		}
	}
	if r.cfg.Debug {
		args = append([]string{"--verbose=2"}, args...)
	}
	return mediaCommand(context.Background(), r.cfg.Player, args...)
}

func (r *receiver) startPlayer(id string) error {
	r.stopPlayer()
	cmd := r.playerCommand(id)
	slog.Info("starting VLC", "emitter_id", id, "command", cmd.String(), "args", cmd.Args[1:], "display", os.Getenv("DISPLAY"), "wayland_display", os.Getenv("WAYLAND_DISPLAY"), "desktop", os.Getenv("XDG_CURRENT_DESKTOP"))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		slog.Warn("player start failed", "emitter_id", id, "executable", r.cfg.Player, "error", err)
		r.mu.Lock()
		r.playerErr = err.Error()
		r.schedulePlayerRetryLocked()
		r.mu.Unlock()
		return fmt.Errorf("start VLC: %w", err)
	}
	r.mu.Lock()
	r.player, r.playerErr = cmd, ""
	r.playerAt = time.Time{}
	r.mu.Unlock()
	slog.Info("player started", "emitter_id", id, "player_pid", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		r.mu.Lock()
		if r.player == cmd {
			slog.Warn("player exited", "emitter_id", id, "player_pid", cmd.Process.Pid, "error", err)
			r.player = nil
			if err != nil {
				r.playerErr = err.Error()
			} else {
				r.playerErr = "player exited"
			}
			if r.state.Playback != "" {
				r.schedulePlayerRetryLocked()
			}
		}
		r.mu.Unlock()
	}()
	return nil
}

func (r *receiver) schedulePlayerRetryLocked() {
	if r.playerTry >= 5 {
		return
	}
	r.playerTry++
	delay := time.Second << min(r.playerTry-1, 4)
	r.playerAt = time.Now().Add(delay)
	slog.Info("player retry scheduled", "emitter_id", r.state.Playback, "attempt", r.playerTry, "retry_delay", delay)
}

func (r *receiver) stopPlayer() {
	r.mu.Lock()
	cmd := r.player
	r.player = nil
	r.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		slog.Info("player stopping", "player_pid", cmd.Process.Pid)
		if runtime.GOOS == "windows" {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(os.Interrupt)
			time.AfterFunc(2*time.Second, func() { _ = cmd.Process.Kill() })
		}
	}
}

func (r *receiver) maintenance(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			for id, emitter := range r.state.Emitters {
				if emitter.Online && time.Since(emitter.LastSeen) > 15*time.Second {
					slog.Warn("emitter heartbeat timed out", "emitter_id", id, "last_seen", emitter.LastSeen)
					emitter.Online, emitter.Streaming = false, false
					r.state.Emitters[id] = emitter
					r.dirty = true
				}
			}
			dirty := r.dirty
			retryPlayback := r.state.Playback
			retryPlayer := retryPlayback != "" && r.player == nil && r.playerTry > 0 && r.playerTry < 5 && !time.Now().Before(r.playerAt)
			sessions := make([]*emitterSession, 0, len(r.sessions))
			for _, session := range r.sessions {
				sessions = append(sessions, session)
			}
			hints := r.receiverHintsLocked()
			r.mu.Unlock()
			if retryPlayer {
				_ = r.startPlayer(retryPlayback)
			}
			for _, session := range sessions {
				_ = session.send(sessionMessage{Type: "peers", Receivers: hints})
			}
			if dirty {
				_ = r.save()
			}
		}
	}
}

func (r *receiver) discoverPeers(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		peers, _ := discover(ctx, r.cfg, 3*time.Second)
		slog.Debug("receiver discovery completed", "receivers", len(peers))
		r.mu.Lock()
		for _, peer := range peers {
			if peer.ID != r.state.Identity.ID && routableEndpoint(peer.Address) {
				if old, ok := r.peers[peer.ID]; !ok || old.Address != peer.Address {
					slog.Info("receiver peer discovered", "receiver_id", peer.ID, "receiver", peer.Name, "address", peer.Address)
				}
				r.peers[peer.ID] = peer
			}
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *receiver) receiverHintsLocked() []ReceiverInfo {
	result := make([]ReceiverInfo, 0, len(r.peers))
	for _, peer := range r.peers {
		result = append(result, peer)
	}
	return result
}

func (r *receiver) save() error {
	r.mu.Lock()
	state := r.state
	state.Emitters = cloneEmitters(r.state.Emitters)
	state.Streams = cloneBools(r.state.Streams)
	r.dirty = false
	r.mu.Unlock()
	err := saveJSON(r.cfg.StatePath, state)
	if err != nil {
		slog.Warn("save receiver state failed", "path", r.cfg.StatePath, "error", err)
	}
	return err
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("write JSON response", "error", err)
	}
}

func decodeJSON(w http.ResponseWriter, request *http.Request, target any) error {
	defer request.Body.Close()
	err := json.NewDecoder(http.MaxBytesReader(w, request.Body, 64<<10)).Decode(target)
	if err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
	}
	return err
}

func cloneEmitters(source map[string]EmitterInfo) map[string]EmitterInfo {
	result := make(map[string]EmitterInfo, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneBools(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func safeName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
}
