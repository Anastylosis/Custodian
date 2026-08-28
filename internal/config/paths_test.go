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

	if got, want := DefaultLedgerPath(), filepath.Join(home, "data", "custodian", LedgerName); got != want {
		t.Errorf("DefaultLedgerPath() = %q, want %q", got, want)
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
	for _, name := range []string{"config.yaml", LedgerName} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	if got := DefaultConfigPath(); got != "config.yaml" {
		t.Errorf("DefaultConfigPath() = %q, want config.yaml", got)
	}
	if got := DefaultLedgerPath(); got != LedgerName {
		t.Errorf("DefaultLedgerPath() = %q, want %s", got, LedgerName)
	}
}

// With no config.yaml in the working directory but one at the XDG location,
// the XDG file wins.
func TestDefaultConfigPathFindsXDGFile(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("XDG_CONFIG_HOME is only honoured on freedesktop platforms")
	}
	home := t.TempDir()
	t.Chdir(t.TempDir()) // no ./config.yaml here
	t.Setenv("HOME", home)
	xdgConfig := filepath.Join(home, "xdgcfg")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	want := filepath.Join(xdgConfig, "custodian", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if got := DefaultConfigPath(); got != want {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, want)
	}
}

// With neither a local file nor an XDG one, DefaultConfigPath falls back to
// the current directory so `config init` has somewhere to write.
func TestDefaultConfigPathFallsBackWithNothingOnDisk(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "nowhere"))

	if got := DefaultConfigPath(); got != "config.yaml" {
		t.Errorf("DefaultConfigPath() = %q, want config.yaml", got)
	}
}

// When dataHome() cannot resolve anything (no $HOME), DefaultLedgerPath
// still returns a usable relative path rather than propagating the failure.
func TestDefaultLedgerPathFallsBackWhenDataHomeIsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("dataHome always resolves via UserConfigDir on this platform")
	}
	t.Chdir(t.TempDir()) // no ./custodian.ledger.sqlite here
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	if got := DefaultLedgerPath(); got != LedgerName {
		t.Errorf("DefaultLedgerPath() = %q, want %s", got, LedgerName)
	}
}

// When os.UserConfigDir() itself fails, DefaultConfigInitPath falls back to
// the current directory rather than propagating the error.
func TestDefaultConfigInitPathFallsBackWhenUserConfigDirFails(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("UserConfigDir does not depend on $HOME on this platform")
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if got := DefaultConfigInitPath(); got != "config.yaml" {
		t.Errorf("DefaultConfigInitPath() = %q, want config.yaml", got)
	}
}

// dataHome itself returns "" (rather than erroring) when $HOME is unset,
// which is what lets the callers above fall back cleanly.
func TestDataHomeEmptyWhenHomeIsUnset(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("freedesktop layout only")
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	if got := dataHome(); got != "" {
		t.Errorf("dataHome() = %q, want empty", got)
	}
}

func TestEnsureDirCreatesNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c", "file.txt")
	if err := EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "a", "b", "c"))
	if err != nil {
		t.Fatalf("expected parent directories to exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
}

func TestEnsureDirIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "file.txt")
	if err := EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir (first): %v", err)
	}
	if err := EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir (second, already exists): %v", err)
	}
}

// With nothing in the working directory, the ledger belongs under the data
// root — never beside the config, which is where an older build put it when
// XDG_CONFIG_HOME was set.
func TestDefaultLedgerPathFallsBackToTheDataRoot(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("the roots coincide here, so there is nothing to keep apart")
	}
	home := t.TempDir()
	t.Chdir(t.TempDir()) // no ./custodian.ledger.sqlite in sight
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))

	want := filepath.Join(home, ".local", "share", "custodian", LedgerName)
	if got := DefaultLedgerPath(); got != want {
		t.Errorf("DefaultLedgerPath() = %q, want %q", got, want)
	}
}
