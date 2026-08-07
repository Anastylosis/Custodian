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

// DefaultDBPath returns the path to use when --db is not specified.
// Resolution order:
//  1. ./custodian.sqlite (if it exists — backward compat)
//  2. $XDG_DATA_HOME/custodian/custodian.sqlite
//  3. ./custodian.sqlite (fallback)
func DefaultDBPath() string {
	if _, err := os.Stat("custodian.sqlite"); err == nil {
		return "custodian.sqlite"
	}

	if dir := dataHome(); dir != "" {
		xdg := filepath.Join(dir, "custodian", "custodian.sqlite")
		if _, err := os.Stat(xdg); err == nil {
			return xdg
		}
	}

	// Anyone who ran an older build with XDG_CONFIG_HOME set has a database
	// under the *config* root, because dataHome() used to fall through to it.
	// Adopt that file rather than silently starting an empty database beside
	// it — the scan history and decisions in there are not reproducible.
	if legacy := legacyDBPath(); legacy != "" {
		if _, err := os.Stat(legacy); err == nil {
			return legacy
		}
	}

	return "custodian.sqlite"
}

// legacyDBPath is where a database landed while dataHome() was misdetecting
// the platform. Empty when that location coincides with the correct one, so
// this only ever names a genuinely different path.
func legacyDBPath() string {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	if cfg == dataHome() {
		return ""
	}
	return filepath.Join(cfg, "custodian", "custodian.sqlite")
}

// DefaultConfigInitPath returns the path `config init` should write to.
// Prefers the XDG location; falls back to current directory.
func DefaultConfigInitPath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "custodian", "config.yaml")
	}
	return "config.yaml"
}

// DefaultDBInitPath returns the path the store should use when creating
// a new database. Prefers XDG; falls back to current directory.
func DefaultDBInitPath() string {
	if dir := dataHome(); dir != "" {
		return filepath.Join(dir, "custodian", "custodian.sqlite")
	}
	return "custodian.sqlite"
}

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
