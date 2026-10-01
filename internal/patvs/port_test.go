package patvs

import (
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
)

func TestDShowDeviceListing(t *testing.T) {
	for _, listing := range []string{
		`[dshow @ 1] "USB Camera" (video)
[dshow @ 1]   Alternative name "@device_pnp_123"
[dshow @ 1] "Caméra intégrée" (video)
[dshow @ 1] "Microphone" (audio)`,
		`[dshow @ 1] DirectShow video devices
[dshow @ 1] "USB Camera"
[dshow @ 1]   Alternative name "@device_pnp_123"
[dshow @ 1] "Caméra intégrée"
[dshow @ 1] DirectShow audio devices
[dshow @ 1] "Microphone"`,
	} {
		if got := parseDShowDevices(listing); !reflect.DeepEqual(got, []string{"USB Camera", "Caméra intégrée"}) {
			t.Fatalf("devices: %v", got)
		}
	}
}

func TestDShowFormatsAndCaptureArguments(t *testing.T) {
	listing := `[dshow @ 1] vcodec=mjpeg min s=320x240 fps=5 max s=320x240 fps=30
[dshow @ 1] pixel_format=yuyv422 min s=640x480 fps=29.9701 max s=640x480 fps=29.9701
[dshow @ 1] vcodec=mjpeg min s=1280x720 fps=5 max s=1920x1080 fps=30`
	camera, err := parseDShowFormats("USB Camera", listing)
	if err != nil {
		t.Fatal(err)
	}
	if camera.Width != 1280 || camera.Height != 720 || camera.Format != "mjpeg" || camera.FrameRate != "25" {
		t.Fatalf("selected %#v", camera)
	}
	args := ffmpegArgs(camera)
	for _, pair := range [][]string{{"-f", "dshow"}, {"-vcodec", "mjpeg"}, {"-i", "video=USB Camera"}, {"-c:v", "copy"}} {
		if !containsArgs(args, pair) {
			t.Fatalf("missing %v in %v", pair, args)
		}
	}
	camera, err = parseDShowFormats("Caméra intégrée", `[dshow @ 1] pixel_format=yuyv422 min s=640x480 fps=29.9701 max s=640x480 fps=29.9701`)
	if err != nil {
		t.Fatal(err)
	}
	if camera.FPS != 30 || camera.FrameRate != "29.9701" || camera.Format != "yuyv422" {
		t.Fatalf("raw profile: %#v", camera)
	}
	args = ffmpegArgs(camera)
	for _, pair := range [][]string{{"-pixel_format", "yuyv422"}, {"-framerate", "29.9701"}, {"-c:v", "mjpeg"}} {
		if !containsArgs(args, pair) {
			t.Fatalf("missing %v in %v", pair, args)
		}
	}
	if _, err := parseDShowFormats("missing", "Could not find video device"); err == nil {
		t.Fatal("accepted an invalid format listing")
	}
}

func containsArgs(args, sequence []string) bool {
	for i := 0; i+len(sequence) <= len(args); i++ {
		if slices.Equal(args[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func TestPlatformStatePath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	cfg := Config{Mode: "emitter", Name: "camera", Secret: "secret", DiscoveryPort: 7412}
	if err := cfg.Defaults(); err != nil {
		t.Fatal(err)
	}
	dir := "xdg"
	if runtime.GOOS == "windows" {
		dir = "local"
	}
	if want := filepath.Join(root, dir, "patvs", "emitter.json"); cfg.StatePath != want {
		t.Fatalf("state path %q, want %q", cfg.StatePath, want)
	}
	if cfg.SnapshotDir != filepath.Join(filepath.Dir(cfg.StatePath), "snapshots") {
		t.Fatalf("snapshot path: %q", cfg.SnapshotDir)
	}
}

func TestRelativeStatePath(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	cfg := Config{Mode: "emitter", Name: "camera", Secret: "secret", DiscoveryPort: 7412,
		StatePath: filepath.Join("state with spaces", "emitter.json")}
	if err := cfg.Defaults(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "state with spaces", "emitter.json")
	if cfg.StatePath != want {
		t.Fatalf("state path %q, want %q", cfg.StatePath, want)
	}
	if cfg.SnapshotDir != filepath.Join(filepath.Dir(want), "snapshots") {
		t.Fatalf("snapshot path: %q", cfg.SnapshotDir)
	}
}

func TestBroadcastSocket(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := setBroadcast(conn); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotPortableFilename(t *testing.T) {
	r := &receiver{cfg: Config{SnapshotDir: t.TempDir()}}
	path, err := r.saveSnapshot("camera", []byte{0xff, 0xd8, 0xff, 0xd9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
