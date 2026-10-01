package patvs

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxReceiverReconnectAttempts = 10

type emitterDiskState struct {
	Identity  Identity                `json:"identity"`
	Receivers map[string]ReceiverInfo `json:"receivers,omitempty"`
}

func RunEmitter(ctx context.Context, cfg Config) error {
	slog.Info("emitter starting", "name", cfg.Name, "camera", cfg.Camera, "discovery_port", cfg.DiscoveryPort)
	slog.Info("state file", "role", "emitter", "path", cfg.StatePath)
	defer slog.Info("emitter stopped")
	var state emitterDiskState
	if err := loadJSON(cfg.StatePath, &state); err != nil {
		return err
	}
	if state.Identity.ID == "" {
		identity, err := loadIdentity(cfg.StatePath, cfg.Name)
		if err != nil {
			return err
		}
		state.Identity = identity
	}
	state.Identity.Name = cfg.Name
	if cfg.Reset {
		slog.Info("forgetting remembered receivers", "receivers", len(state.Receivers))
		state.Receivers = nil
	}
	if state.Receivers == nil {
		state.Receivers = make(map[string]ReceiverInfo)
	}
	if err := saveJSON(cfg.StatePath, state); err != nil {
		return err
	}
	identity := state.Identity
	camera, err := selectCamera(ctx, cfg.Camera)
	if err != nil {
		slog.Warn("camera is unavailable; discovery will continue", "error", err)
		camera = Camera{Device: cfg.Camera}
	}
	manager := newCaptureManager(cfg, camera)
	slog.Info("emitter ready", "id", identity.ID, "camera", camera, "seeds", cfg.Seeds)
	hints := make(chan ReceiverInfo, 32)
	type sessionResult struct {
		peer     ReceiverInfo
		failures int
		actual   ReceiverInfo
	}
	results := make(chan sessionResult)
	active := make(map[string]context.CancelFunc)
	failures := make(map[string]int)
	forgotten := make(map[string]string)
	var wg sync.WaitGroup
	discoverTicker := time.NewTicker(3 * time.Second)
	defer discoverTicker.Stop()

	discoverNow := make(chan struct{}, 1)
	discoverNow <- struct{}{}
	remember := func(peer ReceiverInfo) {
		changed := false
		for id, known := range state.Receivers {
			if id != peer.ID && known.Address == peer.Address {
				if cancel := active[id]; cancel != nil {
					cancel()
				}
				delete(state.Receivers, id)
				delete(failures, id)
				delete(forgotten, id)
				changed = true
				slog.Info("forgetting replaced receiver", "receiver_id", id, "replacement_id", peer.ID, "address", peer.Address)
			}
		}
		old, exists := state.Receivers[peer.ID]
		if !changed && exists && old.Address == peer.Address && old.Name == peer.Name {
			return
		}
		if old.Address != peer.Address {
			delete(failures, peer.ID)
		}
		state.Receivers[peer.ID] = peer
		slog.Info("receiver discovered", "receiver_id", peer.ID, "receiver", peer.Name, "address", peer.Address)
		if err := saveJSON(cfg.StatePath, state); err != nil {
			slog.Warn("save remembered receiver", "error", err)
		}
	}
	startSession := func(peer ReceiverInfo) {
		if _, found := active[peer.ID]; found {
			return
		}
		sessionCtx, cancel := context.WithCancel(ctx)
		active[peer.ID] = cancel
		previousFailures := failures[peer.ID]
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			count, actual := runEmitterSession(sessionCtx, cfg, identity, manager, peer, hints, previousFailures)
			select {
			case results <- sessionResult{peer: peer, failures: count, actual: actual}:
			case <-ctx.Done():
			}
		}()
	}
	for {
		select {
		case <-ctx.Done():
			slog.Info("emitter shutting down")
			for _, cancel := range active {
				cancel()
			}
			wg.Wait()
			manager.captureWG.Wait()
			return nil
		case <-discoverTicker.C:
			select {
			case discoverNow <- struct{}{}:
			default:
			}
		case result := <-results:
			peer := result.peer
			delete(active, peer.ID)
			if current, ok := state.Receivers[peer.ID]; !ok || current.Address != peer.Address {
				continue
			}
			if result.actual.ID != "" {
				remember(result.actual)
				startSession(result.actual)
				continue
			}
			failures[peer.ID] = result.failures
			if result.failures >= maxReceiverReconnectAttempts {
				delete(state.Receivers, peer.ID)
				delete(failures, peer.ID)
				forgotten[peer.ID] = peer.Address
				slog.Info("forgetting unreachable receiver", "receiver_id", peer.ID, "address", peer.Address, "attempts", result.failures)
				if err := saveJSON(cfg.StatePath, state); err != nil {
					slog.Warn("save remembered receiver", "error", err)
				}
			}
			select {
			case discoverNow <- struct{}{}:
			default:
			}
		case peer := <-hints:
			// Hints may outlive a receiver's identity or address assignment.
			verified, ok := probeReceiver(ctx, &http.Client{Timeout: 2 * time.Second}, peer.Address)
			if !ok {
				continue
			}
			peer = verified
			if forgotten[peer.ID] == peer.Address {
				continue
			}
			remember(peer)
			startSession(peer)
		case <-discoverNow:
			discoveryCfg := cfg
			discoveryCfg.Seeds = append([]string(nil), cfg.Seeds...)
			for _, peer := range state.Receivers {
				discoveryCfg.Seeds = appendUnique(discoveryCfg.Seeds, peer.Address)
			}
			peers, _ := discover(ctx, discoveryCfg, 2200*time.Millisecond)
			slog.Debug("emitter discovery completed", "receivers", len(peers))
			for _, peer := range peers {
				delete(forgotten, peer.ID)
				remember(peer)
			}
			for _, peer := range state.Receivers {
				startSession(peer)
			}
		}
	}
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func runEmitterSession(ctx context.Context, cfg Config, identity Identity, manager *captureManager, peer ReceiverInfo, hints chan<- ReceiverInfo, failures int) (int, ReceiverInfo) {
	delay := 250 * time.Millisecond
	for attempt := 0; ctx.Err() == nil && attempt < 3 && failures < maxReceiverReconnectAttempts; attempt++ {
		slog.Debug("connecting to receiver", "receiver_id", peer.ID, "receiver", peer.Name, "address", peer.Address, "attempt", failures+1)
		// Check before opening a session: aliases with old IDs otherwise evict
		// the connection to the same receiver's current ID indefinitely.
		if actual, ok := probeReceiver(ctx, &http.Client{Timeout: 2 * time.Second}, peer.Address); ok && actual.ID != peer.ID {
			return failures, actual
		}
		connected, err := connectEmitter(ctx, cfg, identity, manager, peer, hints)
		if ctx.Err() != nil {
			return failures, ReceiverInfo{}
		}
		if connected {
			failures = 0
		} else {
			failures++
		}
		if failures >= maxReceiverReconnectAttempts {
			break
		}
		jitter := time.Duration(rand.IntN(200)) * time.Millisecond
		slog.Warn("receiver session ended; retrying", "receiver_id", peer.ID, "receiver", peer.Name, "address", peer.Address, "failed_attempts", failures, "retry_delay", delay+jitter, "error", err)
		select {
		case <-ctx.Done():
			return failures, ReceiverInfo{}
		case <-time.After(delay + jitter):
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
	if ctx.Err() == nil {
		slog.Debug("receiver session retries exhausted; rediscovering", "receiver_id", peer.ID, "address", peer.Address)
	}
	return failures, ReceiverInfo{}
}

func connectEmitter(ctx context.Context, cfg Config, identity Identity, manager *captureManager, peer ReceiverInfo, hints chan<- ReceiverInfo) (bool, error) {
	endpoint := url.URL{Scheme: "ws", Host: peer.Address, Path: "/v1/session"}
	header := http.Header{"Authorization": []string{"Bearer " + cfg.Secret}}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 3 * time.Second
	dialer.NetDialContext = (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 15 * time.Second}).DialContext
	conn, response, err := dialer.DialContext(ctx, endpoint.String(), header)
	if err != nil {
		if response != nil {
			return false, fmt.Errorf("connect to %s: %s", peer.Address, response.Status)
		}
		return false, err
	}
	defer conn.Close()
	key := peer.ID + "@" + peer.Address
	frames := manager.add(key)
	defer manager.remove(key)
	if err := conn.WriteJSON(sessionMessage{Type: "register", ID: identity.ID, Name: identity.Name, Camera: manager.camera}); err != nil {
		return false, err
	}
	slog.Info("connected to receiver", "receiver_id", peer.ID, "receiver", peer.Name, "address", peer.Address)
	defer slog.Info("disconnected from receiver", "receiver_id", peer.ID, "address", peer.Address)
	commands := make(chan sessionMessage, 4)
	readErr := make(chan error, 1)
	go func() {
		for {
			var command sessionMessage
			if err := conn.ReadJSON(&command); err != nil {
				readErr <- err
				return
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return
			}
		}
	}()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	streaming, snapshots := false, 0
	firstFrame := true
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-readErr:
			return true, err
		case command := <-commands:
			slog.Debug("receiver command received", "receiver_id", peer.ID, "command", command.Type, "stream", command.Stream, "request", command.Request, "peers", len(command.Receivers))
			for _, hintedPeer := range command.Receivers {
				if hintedPeer.ID == "" || hintedPeer.ID == peer.ID || !routableEndpoint(hintedPeer.Address) {
					continue
				}
				select {
				case hints <- hintedPeer:
				default:
				}
			}
			switch command.Type {
			case "demand":
				if streaming != command.Stream {
					slog.Info("stream demand changed", "receiver_id", peer.ID, "enabled", command.Stream)
				}
				streaming = command.Stream
			case "snapshot":
				snapshots++
				slog.Info("snapshot requested", "receiver_id", peer.ID, "request", command.Request, "pending", snapshots)
			}
			manager.setDemand(key, streaming || snapshots > 0)
		case frame := <-frames:
			if !streaming && snapshots == 0 {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return true, err
			}
			if firstFrame {
				slog.Debug("first frame sent", "receiver_id", peer.ID, "bytes", len(frame))
				firstFrame = false
			}
			if snapshots > 0 {
				snapshots--
				manager.setDemand(key, streaming || snapshots > 0)
			}
		case <-heartbeat.C:
			manager.mu.Lock()
			captureErr := manager.lastError
			camera := manager.camera
			manager.mu.Unlock()
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if err := conn.WriteJSON(sessionMessage{Type: "heartbeat", Camera: camera, Error: strings.TrimSpace(captureErr)}); err != nil {
				return true, err
			}
		}
	}
}
