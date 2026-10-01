package patvs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Config struct {
	Mode               string
	Name               string
	Secret             string
	StatePath          string
	APIAddr            string
	DiscoveryPort      int
	StreamAddr         string
	Seeds              []string
	JSON               bool
	Camera             string
	SnapshotDir        string
	Player             string
	DisplayAspectRatio string
	Debug              bool
	Reset              bool
	Args               []string
}

func (c *Config) Defaults() error {
	if c.DisplayAspectRatio != "" {
		parts := strings.Split(c.DisplayAspectRatio, ":")
		if len(parts) != 2 {
			return fmt.Errorf("display aspect ratio must be positive width:height, such as 4:3 or 16:9")
		}
		for _, part := range parts {
			value, err := strconv.ParseUint(part, 10, 32)
			if err != nil || value == 0 {
				return fmt.Errorf("invalid display aspect ratio %q: use positive width:height", c.DisplayAspectRatio)
			}
		}
	}
	if c.Secret == "" {
		return fmt.Errorf("secret cannot be empty")
	}
	if c.DiscoveryPort < 1 || c.DiscoveryPort > 65535 {
		return fmt.Errorf("invalid discovery port %d", c.DiscoveryPort)
	}
	if c.Name == "" {
		name, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("read hostname: %w", err)
		}
		c.Name = name
	}
	if c.StatePath == "" {
		stateRoot := os.Getenv("XDG_STATE_HOME")
		if runtime.GOOS == "windows" {
			stateRoot = os.Getenv("LOCALAPPDATA")
		}
		if stateRoot == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find home directory: %w", err)
			}
			stateRoot = filepath.Join(home, ".local", "state")
			if runtime.GOOS == "windows" {
				stateRoot = filepath.Join(home, "AppData", "Local")
			}
		}
		c.StatePath = filepath.Join(stateRoot, "patvs", c.Mode+".json")
	}
	if c.SnapshotDir == "" {
		c.SnapshotDir = filepath.Join(filepath.Dir(c.StatePath), "snapshots")
	}
	return nil
}
