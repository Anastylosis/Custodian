package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// XDG_DATA_HOME is the explicit answer and wins everywhere.
func TestDataHomeRespectsXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/explicit/data")
	if got := dataHome(); got != "/explicit/data" {
		t.Errorf("dataHome() = %q, want /explicit/data", got)
	}
}

// The regression this file exists for.
//
// dataHome() used to detect Linux by comparing os.UserConfigDir() against
// ~/.config. Setting XDG_CONFIG_HOME makes those differ, so it concluded
// "not Linux" and returned the *config* directory — the database then landed
// next to config.yaml instead of in ~/.local/share. Nothing errored; the file
// just appeared somewhere the user had not asked for.
func TestDataHomeIgnoresXDGConfigHome(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("data and config deliberately share a root on this platform")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "somewhere-else"))

	got := dataHome()
	want := filepath.Join(home, ".local", "share")
	if got != want {
		t.Errorf("dataHome() = %q, want %q\nXDG_CONFIG_HOME must not influence the data directory", got, want)
	}
	if strings.Contains(got, "somewhere-else") {
		t.Error("data directory followed XDG_CONFIG_HOME — the config/data split is broken")
	}
}

func TestDataHomeDefaultsToLocalShare(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("freedesktop layout only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if got, want := dataHome(), filepath.Join(home, ".local", "share"); got != want {
		t.Errorf("dataHome() = %q, want %q", got, want)
	}
}

// Data and config must not be the same directory on freedesktop platforms —
// that separation is the whole point of the function.
func TestDataAndConfigRootsDifferOnFreedesktop(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("these platforms intentionally share a root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("UserConfigDir: %v", err)
	}
	if dataHome() == cfg {
		t.Errorf("data and config both resolve to %q", cfg)
	}
}

// The init paths are what `config init` and a new store actually write to, so
// they must sit under the roots above rather than re-deriving them.
func TestInitPathsUseTheResolvedRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))

	if got, want := DefaultDBInitPath(), filepath.Join(home, "data", "custodian", "custodian.sqlite"); got != want {
		t.Errorf("DefaultDBInitPath() = %q, want %q", got, want)
	}
	// Config init follows os.UserConfigDir, which honours XDG_CONFIG_HOME on
	// freedesktop platforms and uses the platform-native root elsewhere.
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		if got, want := DefaultConfigInitPath(), filepath.Join(home, "config", "custodian", "config.yaml"); got != want {
			t.Errorf("DefaultConfigInitPath() = %q, want %q", got, want)
		}
	}
}

// A file in the working directory keeps winning — people run this from a
// checkout and expect the local file to be used.
func TestLocalFilesTakePrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, name := range []string{"config.yaml", "custodian.sqlite"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	if got := DefaultConfigPath(); got != "config.yaml" {
		t.Errorf("DefaultConfigPath() = %q, want config.yaml", got)
	}
	if got := DefaultDBPath(); got != "custodian.sqlite" {
		t.Errorf("DefaultDBPath() = %q, want custodian.sqlite", got)
	}
}

// A database written by an older build — which put it under the config root
// when XDG_CONFIG_HOME was set — must still be found. Starting an empty one
// beside it would strand scan history that cannot be reproduced.
func TestDefaultDBPathAdoptsAMisplacedLegacyDatabase(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("the roots coincide here, so there is no legacy location")
	}
	home := t.TempDir()
	t.Chdir(t.TempDir()) // no ./custodian.sqlite in sight
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	cfgRoot := filepath.Join(home, "cfg")
	t.Setenv("XDG_CONFIG_HOME", cfgRoot)

	legacy := filepath.Join(cfgRoot, "custodian", "custodian.sqlite")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := DefaultDBPath(); got != legacy {
		t.Errorf("DefaultDBPath() = %q, want the legacy file at %q", got, legacy)
	}
}

// With nothing on disk anywhere, a fresh install must not be lured into the
// legacy path — new databases belong under the data root.
func TestDefaultDBInitPathIgnoresLegacyLocation(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("the roots coincide here")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))

	want := filepath.Join(home, ".local", "share", "custodian", "custodian.sqlite")
	if got := DefaultDBInitPath(); got != want {
		t.Errorf("DefaultDBInitPath() = %q, want %q", got, want)
	}
}
