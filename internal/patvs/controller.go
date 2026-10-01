package patvs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type controllerClient struct {
	cfg  Config
	http http.Client
}

func RunController(ctx context.Context, cfg Config) error {
	client := controllerClient{cfg: cfg, http: http.Client{Timeout: 12 * time.Second}}
	if len(cfg.Args) == 0 || cfg.Args[0] == "tui" {
		return client.runTUI(ctx)
	}
	command := cfg.Args[0]
	args := cfg.Args[1:]
	if command == "receivers" {
		peers := client.findReceivers(ctx, 3*time.Second)
		peers = client.expandHints(ctx, peers)
		sort.Slice(peers, func(i, j int) bool { return peers[i].Name < peers[j].Name })
		return printValue(cfg.JSON, peers)
	}
	if len(args) == 0 {
		return fmt.Errorf("%s requires a receiver", command)
	}
	peer, err := client.resolveReceiver(ctx, args[0])
	if err != nil {
		return err
	}
	switch command {
	case "status":
		var status ReceiverStatus
		if err := client.request(ctx, peer, http.MethodGet, "/v1/status", nil, &status); err != nil {
			return err
		}
		return printValue(cfg.JSON, status)
	case "emitters":
		var emitters map[string]EmitterInfo
		if err := client.request(ctx, peer, http.MethodGet, "/v1/emitters", nil, &emitters); err != nil {
			return err
		}
		return printValue(cfg.JSON, sortedEmitters(emitters))
	case "snapshot":
		if len(args) != 2 {
			return fmt.Errorf("usage: snapshot <receiver> <emitter>")
		}
		id, err := client.resolveEmitter(ctx, peer, args[1])
		if err != nil {
			return err
		}
		var result map[string]string
		if err := client.request(ctx, peer, http.MethodPost, "/v1/snapshots/"+id, nil, &result); err != nil {
			return err
		}
		return printValue(cfg.JSON, result)
	case "stream":
		if len(args) != 3 || args[2] != "start" && args[2] != "stop" {
			return fmt.Errorf("usage: stream <receiver> <emitter> start|stop")
		}
		id, err := client.resolveEmitter(ctx, peer, args[1])
		if err != nil {
			return err
		}
		var result map[string]bool
		if err := client.request(ctx, peer, http.MethodPut, "/v1/streams/"+id, map[string]bool{"enabled": args[2] == "start"}, &result); err != nil {
			return err
		}
		return printValue(cfg.JSON, result)
	case "play":
		if len(args) != 2 {
			return fmt.Errorf("usage: play <receiver> <emitter>")
		}
		id, err := client.resolveEmitter(ctx, peer, args[1])
		if err != nil {
			return err
		}
		var result map[string]string
		if err := client.request(ctx, peer, http.MethodPut, "/v1/playback", map[string]string{"emitter_id": id}, &result); err != nil {
			return err
		}
		return printValue(cfg.JSON, result)
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("usage: stop <receiver>")
		}
		return client.request(ctx, peer, http.MethodDelete, "/v1/playback", nil, nil)
	case "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: delete <receiver> <emitter>")
		}
		id, err := client.resolveEmitter(ctx, peer, args[1])
		if err != nil {
			return err
		}
		return client.request(ctx, peer, http.MethodDelete, "/v1/emitters/"+id, nil, nil)
	default:
		return fmt.Errorf("unknown controller command %q", command)
	}
}

func (c controllerClient) resolveReceiver(ctx context.Context, selector string) (ReceiverInfo, error) {
	if strings.Contains(selector, ":") {
		if peer, ok := probeReceiver(ctx, &c.http, selector); ok {
			return peer, nil
		}
	}
	peers := c.findReceivers(ctx, 3*time.Second)
	peers = c.expandHints(ctx, peers)
	var matches []ReceiverInfo
	for _, peer := range peers {
		if peer.ID == selector || peer.Name == selector || peer.Address == selector || strings.HasPrefix(peer.ID, selector) {
			matches = append(matches, peer)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return ReceiverInfo{}, fmt.Errorf("receiver %q is ambiguous", selector)
	}
	address := selector
	if !strings.Contains(address, ":") {
		address = net.JoinHostPort(address, "7411")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/health", nil)
	if err != nil {
		return ReceiverInfo{}, fmt.Errorf("unknown receiver %q", selector)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return ReceiverInfo{}, fmt.Errorf("unknown receiver %q", selector)
	}
	defer response.Body.Close()
	var identity Identity
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&identity) != nil || identity.ID == "" {
		return ReceiverInfo{}, fmt.Errorf("%q is not a patvs receiver", selector)
	}
	return ReceiverInfo{ID: identity.ID, Name: identity.Name, Address: address, LastSeen: time.Now()}, nil
}

func (c controllerClient) expandHints(ctx context.Context, peers []ReceiverInfo) []ReceiverInfo {
	known := make(map[string]ReceiverInfo, len(peers))
	queue := append([]ReceiverInfo(nil), peers...)
	for _, peer := range peers {
		known[peer.ID] = peer
	}
	for len(queue) > 0 && len(known) < 64 {
		peer := queue[0]
		queue = queue[1:]
		var status ReceiverStatus
		if c.request(ctx, peer, http.MethodGet, "/v1/status", nil, &status) != nil {
			continue
		}
		for _, hint := range status.Receivers {
			if _, exists := known[hint.ID]; exists || !routableEndpoint(hint.Address) {
				continue
			}
			verified, ok := probeReceiver(ctx, &c.http, hint.Address)
			if !ok || verified.ID != hint.ID {
				continue
			}
			known[verified.ID] = verified
			queue = append(queue, verified)
		}
	}
	result := make([]ReceiverInfo, 0, len(known))
	for _, peer := range known {
		result = append(result, peer)
	}
	return result
}

func (c controllerClient) resolveEmitter(ctx context.Context, peer ReceiverInfo, selector string) (string, error) {
	var emitters map[string]EmitterInfo
	if err := c.request(ctx, peer, http.MethodGet, "/v1/emitters", nil, &emitters); err != nil {
		return "", err
	}
	var matches []string
	for id, emitter := range emitters {
		if id == selector || emitter.Name == selector || strings.HasPrefix(id, selector) {
			matches = append(matches, id)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("unknown emitter %q", selector)
	}
	return "", fmt.Errorf("emitter %q is ambiguous", selector)
}

func (c controllerClient) request(ctx context.Context, peer ReceiverInfo, method, path string, body, target any) error {
	var source io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		source = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+peer.Address+path, source)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.cfg.Secret)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", peer.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("%s: %s: %s", peer.Name, response.Status, strings.TrimSpace(string(message)))
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode %s response: %w", peer.Name, err)
	}
	return nil
}

func (c controllerClient) runTUI(ctx context.Context) error {
	tuiCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err := newTUI(tuiCtx, c).Run()
	return err
}

func actionResult(success string, err error) string {
	if err != nil {
		return "Error: " + err.Error()
	}
	return success
}

func sortedEmitters(source map[string]EmitterInfo) []EmitterInfo {
	result := make([]EmitterInfo, 0, len(source))
	for _, emitter := range source {
		result = append(result, emitter)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func printValue(asJSON bool, value any) error {
	if asJSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	switch typed := value.(type) {
	case []ReceiverInfo:
		for _, receiver := range typed {
			fmt.Printf("%-32s  %-20s  %s\n", receiver.ID, receiver.Name, receiver.Address)
		}
	case []EmitterInfo:
		for _, emitter := range typed {
			fmt.Printf("%-32s  %-20s  online=%-5t  %dx%d %s\n", emitter.ID, emitter.Name, emitter.Online, emitter.Camera.Width, emitter.Camera.Height, emitter.Camera.Format)
		}
	case map[string]string:
		for key, item := range typed {
			fmt.Printf("%s: %s\n", key, item)
		}
	case map[string]bool:
		for key, item := range typed {
			fmt.Printf("%s: %t\n", key, item)
		}
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	}
	return nil
}
