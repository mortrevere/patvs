package patvs

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type emitterDiskState struct {
	Identity  Identity                `json:"identity"`
	Receivers map[string]ReceiverInfo `json:"receivers,omitempty"`
}

func RunEmitter(ctx context.Context, cfg Config) error {
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
	if state.Receivers == nil {
		state.Receivers = make(map[string]ReceiverInfo)
	}
	for _, peer := range state.Receivers {
		cfg.Seeds = appendUnique(cfg.Seeds, peer.Address)
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
	hints := make(chan ReceiverInfo, 32)
	type activeSession struct {
		address string
		cancel  context.CancelFunc
	}
	active := make(map[string]activeSession)
	var mu sync.Mutex
	var wg sync.WaitGroup
	discoverTicker := time.NewTicker(3 * time.Second)
	defer discoverTicker.Stop()

	discoverNow := make(chan struct{}, 1)
	discoverNow <- struct{}{}
	remember := func(peer ReceiverInfo) {
		old, exists := state.Receivers[peer.ID]
		if exists && old.Address == peer.Address && old.Name == peer.Name {
			return
		}
		state.Receivers[peer.ID] = peer
		if err := saveJSON(cfg.StatePath, state); err != nil {
			slog.Warn("save remembered receiver", "error", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, session := range active {
				session.cancel()
			}
			mu.Unlock()
			wg.Wait()
			return nil
		case <-discoverTicker.C:
			select {
			case discoverNow <- struct{}{}:
			default:
			}
		case peer := <-hints:
			remember(peer)
			mu.Lock()
			_, found := active[peer.ID]
			if !found {
				sessionCtx, cancel := context.WithCancel(ctx)
				active[peer.ID] = activeSession{address: peer.Address, cancel: cancel}
				wg.Add(1)
				go func(peer ReceiverInfo) {
					defer wg.Done()
					runEmitterSession(sessionCtx, cfg, identity, manager, peer, hints)
					mu.Lock()
					delete(active, peer.ID)
					mu.Unlock()
					select {
					case discoverNow <- struct{}{}:
					default:
					}
				}(peer)
			}
			mu.Unlock()
		case <-discoverNow:
			peers, _ := discover(ctx, cfg, 2200*time.Millisecond)
			for _, peer := range peers {
				remember(peer)
				mu.Lock()
				_, found := active[peer.ID]
				if found {
					mu.Unlock()
					continue
				}
				sessionCtx, cancel := context.WithCancel(ctx)
				active[peer.ID] = activeSession{address: peer.Address, cancel: cancel}
				mu.Unlock()
				wg.Add(1)
				go func(peer ReceiverInfo) {
					defer wg.Done()
					runEmitterSession(sessionCtx, cfg, identity, manager, peer, hints)
					mu.Lock()
					if current, ok := active[peer.ID]; ok && current.address == peer.Address {
						delete(active, peer.ID)
					}
					mu.Unlock()
					select {
					case discoverNow <- struct{}{}:
					default:
					}
				}(peer)
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

func runEmitterSession(ctx context.Context, cfg Config, identity Identity, manager *captureManager, peer ReceiverInfo, hints chan<- ReceiverInfo) {
	delay := 250 * time.Millisecond
	for attempt := 0; ctx.Err() == nil && attempt < 3; attempt++ {
		err := connectEmitter(ctx, cfg, identity, manager, peer, hints)
		if ctx.Err() != nil {
			return
		}
		slog.Debug("receiver session ended", "receiver", peer.Name, "error", err)
		jitter := time.Duration(rand.IntN(200)) * time.Millisecond
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay + jitter):
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
}

func connectEmitter(ctx context.Context, cfg Config, identity Identity, manager *captureManager, peer ReceiverInfo, hints chan<- ReceiverInfo) error {
	endpoint := url.URL{Scheme: "ws", Host: peer.Address, Path: "/v1/session"}
	header := http.Header{"Authorization": []string{"Bearer " + cfg.Secret}}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, endpoint.String(), header)
	if err != nil {
		if response != nil {
			return fmt.Errorf("connect to %s: %s", peer.Address, response.Status)
		}
		return err
	}
	defer conn.Close()
	key := peer.ID + "@" + peer.Address
	frames := manager.add(key)
	defer manager.remove(key)
	if err := conn.WriteJSON(sessionMessage{Type: "register", ID: identity.ID, Name: identity.Name, Camera: manager.camera}); err != nil {
		return err
	}
	slog.Info("connected to receiver", "receiver", peer.Name, "address", peer.Address)
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case command := <-commands:
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
				streaming = command.Stream
			case "snapshot":
				snapshots++
			}
			manager.setDemand(key, streaming || snapshots > 0)
		case frame := <-frames:
			if !streaming && snapshots == 0 {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return err
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
				return err
			}
		}
	}
}
