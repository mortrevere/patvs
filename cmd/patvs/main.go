package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"patvs/internal/patvs"
)

var (
	version    = "dev"
	builtSeeds = ""
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("patvs stopped", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("a mode is required")
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return nil
	}

	mode := args[0]
	if mode != "emitter" && mode != "receiver" && mode != "controller" {
		usage()
		return fmt.Errorf("unknown mode %q", mode)
	}

	flags := flag.NewFlagSet(mode, flag.ContinueOnError)
	secret := os.Getenv("PATVS_SECRET")
	if secret == "" {
		secret = "patvs"
	}
	cfg := patvs.Config{Mode: mode, Secret: secret, APIAddr: ":7411", DiscoveryPort: 7412, StreamAddr: "127.0.0.1:7413"}
	flags.StringVar(&cfg.Name, "name", "", "human-readable device name (default: hostname)")
	flags.StringVar(&cfg.Secret, "secret", cfg.Secret, "shared installation secret")
	flags.StringVar(&cfg.StatePath, "state", "", "state file path")
	flags.StringVar(&cfg.APIAddr, "listen", cfg.APIAddr, "receiver API listen address")
	flags.IntVar(&cfg.DiscoveryPort, "discovery-port", cfg.DiscoveryPort, "UDP discovery port")
	flags.StringVar(&cfg.StreamAddr, "stream-listen", cfg.StreamAddr, "receiver loopback stream address")
	seedText := strings.TrimSpace(strings.Join([]string{builtSeeds, os.Getenv("PATVS_SEEDS")}, ","))
	flags.StringVar(&seedText, "seeds", seedText, "comma-separated receiver addresses")
	flags.BoolVar(&cfg.JSON, "json", false, "print machine-readable JSON")
	camera := os.Getenv("PATVS_CAMERA")
	if camera == "" {
		camera = "auto"
	}
	flags.StringVar(&cfg.Camera, "camera", camera, "camera device (V4L2 path or Windows DirectShow name), lavfi expression, or auto")
	flags.StringVar(&cfg.SnapshotDir, "snapshot-dir", "", "receiver snapshot directory")
	player := os.Getenv("PATVS_PLAYER")
	if player == "" {
		player = "vlc"
	}
	flags.StringVar(&cfg.Player, "player", player, "VLC executable path")
	if mode == "receiver" {
		flags.StringVar(&cfg.DisplayAspectRatio, "display-aspect-ratio", os.Getenv("PATVS_DISPLAY_ASPECT_RATIO"), "stretch playback to this display ratio, e.g. 4:3 or 16:9 (default: preserve source ratio)")
	}
	flags.BoolVar(&cfg.Debug, "debug", false, "enable debug logs")
	if mode == "emitter" || mode == "receiver" {
		flags.BoolVar(&cfg.Reset, "reset", false, "forget remembered peers and their settings on startup (keep device identity)")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg.Args = flags.Args()
	cfg.Seeds = splitSeeds(seedText)
	if err := cfg.Defaults(); err != nil {
		return err
	}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})).With("mode", mode, "pid", os.Getpid()))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch mode {
	case "emitter":
		return patvs.RunEmitter(ctx, cfg)
	case "receiver":
		return patvs.RunReceiver(ctx, cfg)
	default:
		return patvs.RunController(ctx, cfg)
	}
}

func splitSeeds(value string) []string {
	var seeds []string
	for _, seed := range strings.Split(value, ",") {
		if seed = strings.TrimSpace(seed); seed != "" {
			seeds = append(seeds, seed)
		}
	}
	return seeds
}

func usage() {
	fmt.Fprintln(os.Stderr, `patvs - Portable All Terrain Video Streaming

Usage:
  patvs emitter [options]
  patvs receiver [options]
  patvs controller [options] [command]
  patvs version

Run a mode with -h for its common options.`)
}
