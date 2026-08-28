package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/vfs"
)

// This file holds the filesystem plumbing the commands that touch the library
// share: `captions`, which reads a folder of subtitles and moves them next to
// their videos, and `doctor`, which verifies that plumbing before anything
// relies on it.

// localDirCandidate reports whether the given path is a directory on this
// machine, expanding a leading ~.
func localDirCandidate(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p, true
		}
		return abs, true
	}
	return "", false
}

// openLibraryFS builds the filesystem backend from the sidecars config,
// ignoring sidecars.enabled: reading the library is a report, and stays
// available even when automatic sidecar moving is switched off.
func openLibraryFS(ctx context.Context, cfg *config.Config) (vfs.FS, func(), error) {
	mode := cfg.Sidecars.Access
	if mode == "" || mode == "auto" {
		if cfg.Sidecars.SSH.Host != "" {
			mode = "ssh"
		} else {
			mode = "local"
		}
	}
	switch mode {
	case "ssh":
		sc := cfg.Sidecars.SSH
		conn, err := vfs.NewSSH(ctx, vfs.SSHConfig{
			Host: sc.Host, Port: sc.Port, User: sc.User,
			KeyFile:          sc.KeyFile,
			Passphrase:       osGetenv(sc.PassphraseEnv),
			Password:         osGetenv(sc.PasswordEnv),
			KnownHostsFile:   sc.KnownHostsFile,
			AcceptNewHostKey: sc.AcceptNewHostKey,
		})
		if err != nil {
			return nil, func() {}, err
		}
		return conn, func() { _ = conn.Close() }, nil
	default:
		return vfs.NewLocal(), func() {}, nil
	}
}

func osGetenv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

// pathMapper builds the Stash-path → local-path translator from config.
func pathMapper(cfg *config.Config) (*pathmap.Mapper, error) {
	rules := make([]pathmap.Rule, 0, len(cfg.Sidecars.PathMap))
	for _, r := range cfg.Sidecars.PathMap {
		rules = append(rules, pathmap.Rule{StashPrefix: r.Stash, LocalPrefix: r.Local})
	}
	return pathmap.New(rules)
}

// trimExt drops a filename's extension without touching its directory.
func trimExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}
