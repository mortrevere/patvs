package patvs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// RunBoth runs a receiver and an emitter until cancellation or either role stops.
func RunBoth(ctx context.Context, cfg Config) error {
	configs, err := bothConfigs(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 2)
	for _, role := range configs {
		go func() {
			var err error
			if role.Mode == "receiver" {
				err = RunReceiver(ctx, role)
			} else {
				err = RunEmitter(ctx, role)
			}
			if err != nil {
				err = fmt.Errorf("%s: %w", role.Mode, err)
			}
			done <- err
		}()
	}
	err = <-done
	cancel()
	return errors.Join(err, <-done)
}

func bothConfigs(cfg Config) ([2]Config, error) {
	configs := [2]Config{cfg, cfg}
	for i, mode := range []string{"receiver", "emitter"} {
		role := &configs[i]
		role.Mode = mode
		if cfg.StatePath != "" {
			ext := filepath.Ext(cfg.StatePath)
			stem := strings.TrimSuffix(cfg.StatePath, ext)
			if ext == "" {
				ext = ".json"
			}
			role.StatePath = stem + "-" + mode + ext
		}
		if err := role.Defaults(); err != nil {
			return configs, fmt.Errorf("%s: %w", mode, err)
		}
	}
	return configs, nil
}
