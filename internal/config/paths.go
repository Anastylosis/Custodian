package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultConfigPath returns the path to use when --config is not specified.
// Resolution order:
//  1. ./config.yaml (if it exists in the current directory — backward compat)
//  2. $XDG_CONFIG_HOME/custodian/config.yaml (Linux/Mac standard)
//  3. ./config.yaml (fallback for first-time users)
func DefaultConfigPath() string {
	// Current directory takes precedence (portable / development use).
	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}

	// XDG standard location.
	if dir, err := os.UserConfigDir(); err == nil {
		xdg := filepath.Join(dir, "custodian", "config.yaml")
		if _, err := os.Stat(xdg); err == nil {
			return xdg
		}
	}

	// Nothing found — fall back to current dir (config init will create it
	// in the XDG location).
	return "config.yaml"
}

// LedgerName is the ledger file's basename, wherever it lives.
const LedgerName = "custodian.ledger.sqlite"

// DefaultLedgerPath returns the ledger's path when --ledger is not given.
// Resolution order:
//  1. ./custodian.ledger.sqlite (if it exists — a portable / per-library setup)
//  2. $XDG_DATA_HOME/custodian/custodian.ledger.sqlite
//
// There is one database and one flag for it. Earlier builds derived the
// ledger's name from a --db path that pointed at a store which no longer
// exists; the name it derived is the one returned here, so an existing ledger
// is found where it already sits.
func DefaultLedgerPath() string {
	if _, err := os.Stat(LedgerName); err == nil {
		return LedgerName
	}
	if dir := dataHome(); dir != "" {
		return filepath.Join(dir, "custodian", LedgerName)
	}
	return LedgerName
}

// DefaultConfigInitPath returns the path `config init` should write to.
// Prefers the XDG location; falls back to current directory.
func DefaultConfigInitPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "custodian", "config.yaml")
	}
	return "config.yaml"
}

// DataHome exports dataHome for other internal packages (internal/reclaim's
// backup directory) that need the same platform-appropriate data root
// without re-deriving it.
func DataHome() string { return dataHome() }

// dataHome returns the platform-appropriate directory for user data:
//
//	Linux/BSD: $XDG_DATA_HOME, else ~/.local/share
//	macOS:     ~/Library/Application Support  (same root as config)
//	Windows:   %AppData%                      (same root as config)
//
// Only the freedesktop platforms separate data from config, so elsewhere this
// shares the config root and relies on the "custodian" subdirectory to keep
// the two apart.
//
// The platform is decided by runtime.GOOS rather than inferred. An earlier
// version compared os.UserConfigDir() against ~/.config and treated a
// mismatch as "not Linux" — which is exactly what setting XDG_CONFIG_HOME
// produces, so the database landed in the *config* directory instead of
// ~/.local/share. Nothing failed; the file simply appeared somewhere the user
// had not asked for.
func dataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}

	switch runtime.GOOS {
	case "windows", "darwin":
		// No separate data root on these platforms.
		if dir, err := os.UserConfigDir(); err == nil {
			return dir
		}
		return ""
	default:
		// freedesktop: data belongs in ~/.local/share, never ~/.config.
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".local", "share")
	}
}

// EnsureDir creates dir and all parents if they don't exist.
// Used by config init and store Open to create XDG directories.
func EnsureDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o755)
}
