package sidecar

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/vfs"
)

// Build constructs a Mover from configuration.
//
// It returns (nil, noop, nil) when sidecar support is switched off, so callers
// can use the result unconditionally — every Mover method tolerates a nil
// receiver. The returned cleanup must always be called; it closes the SSH
// connection when one was opened.
func Build(ctx context.Context, cfg *config.Config) (*Mover, func(), error) {
	noop := func() {}
	if cfg == nil || !cfg.Sidecars.Enabled {
		return nil, noop, nil
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Sidecars.Access))
	if mode == "" {
		mode = "auto"
	}
	if mode == "off" || mode == "none" {
		return nil, noop, nil
	}
	if mode == "auto" {
		if cfg.Sidecars.SSH.Host != "" {
			mode = "ssh"
		} else {
			mode = "local"
		}
	}

	rules := make([]pathmap.Rule, 0, len(cfg.Sidecars.PathMap))
	for _, r := range cfg.Sidecars.PathMap {
		rules = append(rules, pathmap.Rule{StashPrefix: r.Stash, LocalPrefix: r.Local})
	}
	mp, err := pathmap.New(rules)
	if err != nil {
		return nil, noop, fmt.Errorf("sidecars.path_map: %w", err)
	}

	var fs vfs.FS
	cleanup := noop
	switch mode {
	case "local":
		fs = vfs.NewLocal()
	case "ssh":
		sc := cfg.Sidecars.SSH
		conn, err := vfs.NewSSH(ctx, vfs.SSHConfig{
			Host:             sc.Host,
			Port:             sc.Port,
			User:             sc.User,
			KeyFile:          sc.KeyFile,
			Passphrase:       envOrEmpty(sc.PassphraseEnv),
			Password:         envOrEmpty(sc.PasswordEnv),
			KnownHostsFile:   sc.KnownHostsFile,
			AcceptNewHostKey: sc.AcceptNewHostKey,
		})
		if err != nil {
			return nil, noop, fmt.Errorf("sidecars over ssh: %w", err)
		}
		fs = conn
		cleanup = func() { _ = conn.Close() }
	default:
		return nil, noop, fmt.Errorf("sidecars.access: unknown mode %q (want auto, local, ssh or off)", cfg.Sidecars.Access)
	}

	mv, err := NewMover(fs, mp, cfg.Sidecars.Extensions)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return mv, cleanup, nil
}

// envOrEmpty reads a named environment variable, tolerating an empty name.
// Secrets are kept in the environment rather than the config file, matching
// how the Stash API key is handled.
func envOrEmpty(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}
