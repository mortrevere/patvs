package patvs

import (
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Mode          string
	Name          string
	Secret        string
	StatePath     string
	APIAddr       string
	DiscoveryPort int
	StreamAddr    string
	Seeds         []string
	JSON          bool
	Camera        string
	SnapshotDir   string
	Player        string
	Debug         bool
	Reset         bool
	Args          []string
}

func (c *Config) Defaults() error {
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
	stateRoot := os.Getenv("XDG_STATE_HOME")
	if stateRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("find home directory: %w", err)
		}
		stateRoot = filepath.Join(home, ".local", "state")
	}
	if c.StatePath == "" {
		c.StatePath = filepath.Join(stateRoot, "patvs", c.Mode+".json")
	}
	if c.SnapshotDir == "" {
		c.SnapshotDir = filepath.Join(filepath.Dir(c.StatePath), "snapshots")
	}
	return nil
}
