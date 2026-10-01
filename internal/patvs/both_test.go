package patvs

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBothConfigs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state with spaces")
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	for _, path := range []string{"", filepath.Join(root, "node.json"), filepath.Join(root, "node"), filepath.Join(root, "node.custom.json")} {
		cfg := Config{Secret: "secret", DiscoveryPort: 7412, StatePath: path, Reset: true, Name: "node"}
		configs, err := bothConfigs(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range configs {
			want := filepath.Join(root, "patvs", role.Mode+".json")
			if path != "" {
				stem := strings.TrimSuffix(path, ".json")
				want = stem + "-" + role.Mode + ".json"
			}
			if role.StatePath != want || !role.Reset || role.Name != cfg.Name {
				t.Fatalf("role config: %#v, want state %q", role, want)
			}
			if role.SnapshotDir != filepath.Join(filepath.Dir(want), "snapshots") {
				t.Fatalf("snapshot directory %q", role.SnapshotDir)
			}
		}
	}
}

func testBothConfig(t *testing.T) Config {
	t.Helper()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	_ = udp.Close()
	return Config{Name: "combined node", Secret: "test-secret", StatePath: filepath.Join(t.TempDir(), "node.json"),
		APIAddr: strings.TrimPrefix(freeTCPAddress(t), "127.0.0.1"), StreamAddr: freeTCPAddress(t), DiscoveryPort: port, Camera: "lavfi:color=c=red"}
}

func TestRunBothRegistersAndPreservesIdentities(t *testing.T) {
	cfg := testBothConfig(t)
	configs, err := bothConfigs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var identities [2]Identity
	for attempt := range 2 {
		func() {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- RunBoth(ctx, cfg) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(8 * time.Second):
					t.Error("combined mode failed to stop")
				}
			}()
			client := &http.Client{Timeout: time.Second}
			deadline := time.Now().Add(10 * time.Second)
			for {
				request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1"+cfg.APIAddr+"/v1/emitters", nil)
				request.Header.Set("Authorization", "Bearer "+cfg.Secret)
				response, err := client.Do(request)
				if err == nil {
					var emitters map[string]EmitterInfo
					decodeErr := json.NewDecoder(response.Body).Decode(&emitters)
					_ = response.Body.Close()
					if decodeErr == nil && len(emitters) == 1 {
						for _, emitter := range emitters {
							if emitter.Online && emitter.Name == cfg.Name {
								return
							}
						}
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("local emitter did not register through discovery")
				}
				time.Sleep(25 * time.Millisecond)
			}
		}()
		assertBothPortsReleased(t, cfg)
		for i, role := range configs {
			var state struct{ Identity Identity }
			if err := loadJSON(role.StatePath, &state); err != nil || state.Identity.ID == "" {
				t.Fatalf("%s identity missing: %#v, %v", role.Mode, state, err)
			}
			if attempt == 0 {
				identities[i] = state.Identity
			} else if identities[i] != state.Identity {
				t.Fatalf("%s identity changed after restart", role.Mode)
			}
		}
	}
	if identities[0].ID == identities[1].ID {
		t.Fatal("receiver and emitter share an identity")
	}
}

func TestRunBothFatalErrorStopsOtherRole(t *testing.T) {
	for _, mode := range []string{"receiver", "emitter"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testBothConfig(t)
			if mode == "receiver" {
				listener, err := net.Listen("tcp", cfg.APIAddr)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			} else {
				configs, _ := bothConfigs(cfg)
				if err := os.WriteFile(configs[1].StatePath, []byte("invalid JSON"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			if err := RunBoth(ctx, cfg); err == nil || !strings.Contains(err.Error(), mode+":") {
				t.Fatalf("expected fatal %s error, got %v", mode, err)
			}
			if ctx.Err() != nil {
				t.Fatal("fatal error did not promptly stop combined mode")
			}
			// The occupied API port remains owned by the test listener.
			if mode == "receiver" {
				cfg.APIAddr = ""
			}
			assertBothPortsReleased(t, cfg)
		})
	}
}

func assertBothPortsReleased(t *testing.T, cfg Config) {
	t.Helper()
	for _, addr := range []string{cfg.APIAddr, cfg.StreamAddr} {
		if addr == "" {
			continue
		}
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("TCP listener leaked at %s: %v", addr, err)
		}
		_ = listener.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		listener, err := net.ListenUDP("udp4", &net.UDPAddr{Port: cfg.DiscoveryPort})
		if err == nil {
			_ = listener.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("UDP discovery listener leaked: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
