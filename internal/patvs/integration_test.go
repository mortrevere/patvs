package patvs

import (
	"context"
	"image/jpeg"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// PATVS_INTEGRATION=1 enables a real FFmpeg check on Linux and Windows.
// PATVS_TEST_CAMERA=auto substitutes a webcam; PATVS_TEST_PLAYER=vlc also tests playback.
func TestLocalVideoIntegration(t *testing.T) {
	if os.Getenv("PATVS_INTEGRATION") != "1" {
		t.Skip("set PATVS_INTEGRATION=1 with FFmpeg installed")
	}
	t.Run("separate", func(t *testing.T) { testLocalVideoIntegration(t, false) })
	t.Run("combined", func(t *testing.T) { testLocalVideoIntegration(t, true) })
}

func testLocalVideoIntegration(t *testing.T, combined bool) {
	root := filepath.Join(t.TempDir(), "state with spaces")
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	discoveryPort := udp.LocalAddr().(*net.UDPAddr).Port
	_ = udp.Close()
	apiEndpoint := freeTCPAddress(t)
	cfg := Config{Mode: "receiver", Name: "test receiver", Secret: "integration-secret",
		APIAddr: strings.TrimPrefix(apiEndpoint, "127.0.0.1"), StreamAddr: freeTCPAddress(t), DiscoveryPort: discoveryPort,
		StatePath: filepath.Join(root, "receiver.json"), SnapshotDir: filepath.Join(root, "snapshots"),
		Player: os.Getenv("PATVS_TEST_PLAYER")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	emitter := cfg
	emitter.Mode, emitter.Name = "emitter", "test camera"
	emitter.StatePath = filepath.Join(root, "emitter.json")
	emitter.Seeds = []string{apiEndpoint}
	emitter.Camera = os.Getenv("PATVS_TEST_CAMERA")
	if emitter.Camera == "" {
		emitter.Camera = "lavfi:color=c=red:s=640x480:r=25"
	}
	roles := 2
	if combined {
		cfg.StatePath = filepath.Join(root, "node.json")
		cfg.Camera = emitter.Camera
		emitter.Name = cfg.Name
		roles = 1
		go func() { done <- RunBoth(ctx, cfg) }()
	} else {
		go func() { done <- RunReceiver(ctx, cfg) }()
		go func() { done <- RunEmitter(ctx, emitter) }()
	}
	defer func() {
		cancel()
		for range roles {
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(8 * time.Second):
				t.Error("daemon failed to stop")
			}
		}
	}()
	client := controllerClient{cfg: cfg, http: http.Client{Timeout: 12 * time.Second}}
	peer := ReceiverInfo{Address: apiEndpoint}
	var id string
	deadline := time.Now().Add(15 * time.Second)
	for id == "" && time.Now().Before(deadline) {
		var status ReceiverStatus
		if client.request(ctx, peer, http.MethodGet, "/v1/status", nil, &status) == nil {
			for key, camera := range status.Emitters {
				if camera.Online && camera.Name == emitter.Name {
					id = key
				}
			}
		}
		if id == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if id == "" {
		t.Fatal("emitter did not register")
	}
	// Prove loopback UDP discovery, independent of the explicit seed.
	peers, _ := discover(ctx, cfg, time.Second)
	if len(peers) == 0 {
		t.Fatal("UDP discovery did not find receiver")
	}
	var snapshot map[string]string
	if err := client.request(ctx, peer, http.MethodPost, "/v1/snapshots/"+id, nil, &snapshot); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(snapshot["path"])
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(file)
	_ = file.Close()
	if err != nil || img.Bounds().Dx() < 1 {
		t.Fatalf("invalid snapshot: %v", err)
	}
	if os.Getenv("PATVS_TEST_CAMERA") == "" {
		r, g, b, _ := img.At(0, 0).RGBA()
		if r < 200*257 || g > 30*257 || b > 30*257 {
			t.Fatalf("snapshot is not red: %d %d %d", r, g, b)
		}
	}
	if err := client.request(ctx, peer, http.MethodPut, "/v1/streams/"+id, map[string]bool{"enabled": true}, nil); err != nil {
		t.Fatal(err)
	}
	response, err := client.http.Get("http://" + cfg.StreamAddr + "/streams/" + id + ".mjpg")
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(response.Body, "patvs").NextPart()
	if err == nil {
		_, err = jpeg.Decode(part)
	}
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("stream failed: status %d, error %v", response.StatusCode, err)
	}
	if cfg.Player != "" {
		if err := client.request(ctx, peer, http.MethodPut, "/v1/playback", map[string]string{"emitter_id": id}, nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
		var status ReceiverStatus
		if err := client.request(ctx, peer, http.MethodGet, "/v1/status", nil, &status); err != nil || status.PlayerPID == 0 {
			t.Fatalf("player did not stay alive: %#v, error %v", status, err)
		}
		found := false
		for _, process := range status.Processes {
			found = found || (process.PID == status.PlayerPID && process.Name == "vlc")
		}
		if !found {
			t.Fatal("media process inspection did not find managed VLC")
		}
		if err := client.request(ctx, peer, http.MethodDelete, "/v1/playback", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	client.cfg.Secret = "wrong-secret"
	if err := client.request(ctx, peer, http.MethodGet, "/v1/status", nil, nil); err == nil {
		t.Fatal("wrong secret accepted")
	}
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}
