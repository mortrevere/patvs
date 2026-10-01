package main

import (
	"path/filepath"
	"testing"
)

func TestParseConfigModes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	for _, test := range []struct {
		name string
		args []string
		mode string
	}{
		{"default", nil, "both"},
		{"options only", []string{"--camera", "lavfi:color=c=red", "--display-aspect-ratio", "4:3", "--reset"}, "both"},
		{"emitter", []string{"emitter"}, "emitter"},
		{"receiver", []string{"receiver"}, "receiver"},
		{"controller", []string{"controller", "receivers"}, "controller"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := parseConfig(test.args)
			if err != nil || cfg.Mode != test.mode {
				t.Fatalf("mode %q, error %v", cfg.Mode, err)
			}
			if cfg.Mode == "both" {
				if cfg.StatePath != "" {
					t.Fatalf("combined state was defaulted too early: %q", cfg.StatePath)
				}
			} else if cfg.StatePath != filepath.Join(root, "patvs", cfg.Mode+".json") {
				t.Fatalf("state path %q", cfg.StatePath)
			}
			if test.name == "options only" && (cfg.Camera != "lavfi:color=c=red" || cfg.DisplayAspectRatio != "4:3" || !cfg.Reset) {
				t.Fatalf("combined options lost: %#v", cfg)
			}
			if cfg.Mode == "controller" && (len(cfg.Args) != 1 || cfg.Args[0] != "receivers") {
				t.Fatalf("controller arguments lost: %v", cfg.Args)
			}
		})
	}
	for _, mode := range []string{"receiver", "emitter"} {
		path := filepath.Join(root, "custom.json")
		cfg, err := parseConfig([]string{mode, "--state", path})
		if err != nil || cfg.StatePath != path {
			t.Fatalf("single-role state path changed: %#v, %v", cfg, err)
		}
	}
	if _, err := parseConfig([]string{"unknown"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestInformationalCommands(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"version"}, {"receiver", "-h"}, {"emitter", "-h"}, {"controller", "-h"}} {
		if err := run(args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
