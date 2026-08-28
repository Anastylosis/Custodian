package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfigPath sets the package-level --config flag value for the
// duration of the test and restores it afterward.
func withConfigPath(t *testing.T, path string) {
	t.Helper()
	prev := flagConfigPath
	flagConfigPath = path
	t.Cleanup(func() { flagConfigPath = prev })
}

func runConfigCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newConfigCmd()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestConfigInit_WritesDefaultConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	withConfigPath(t, path)

	out, err := runConfigCmd(t, "init")
	if err != nil {
		t.Fatalf("config init: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Errorf("output = %q, want it to mention %q", out, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file was not created: %v", err)
	}
}

func TestConfigInit_RefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	withConfigPath(t, path)
	if _, err := runConfigCmd(t, "init"); err != nil {
		t.Fatalf("first init: %v", err)
	}

	_, err := runConfigCmd(t, "init")
	if err == nil {
		t.Fatal("expected an error writing over an existing config")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to mention the file already exists", err)
	}
}

func TestConfigPath_PrintsTheConfiguredPath(t *testing.T) {
	withConfigPath(t, "/custom/config.yaml")
	out, err := runConfigCmd(t, "path")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if got := strings.TrimSpace(out); got != "/custom/config.yaml" {
		t.Errorf("output = %q, want /custom/config.yaml", got)
	}
}

func TestConfigShow_PrintsEffectiveConfig(t *testing.T) {
	withConfigPath(t, filepath.Join(t.TempDir(), "nonexistent.yaml"))
	out, err := runConfigCmd(t, "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}
	if !strings.Contains(out, "url:") {
		t.Errorf("output = %q, want it to contain the effective stash url", out)
	}
}

func TestConfigShow_PropagatesLoadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	if _, err := runConfigCmd(t, "show"); err == nil {
		t.Fatal("expected an error loading invalid yaml")
	}
}

func TestConfigValidate_ReportsOK(t *testing.T) {
	withConfigPath(t, filepath.Join(t.TempDir(), "nonexistent.yaml"))
	out, err := runConfigCmd(t, "validate")
	if err != nil {
		t.Fatalf("config validate: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("output = %q, want it to report ok", out)
	}
}

func TestConfigValidate_ReportsRetiredKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	// "scoring" is one of the top-level keys the rebuild retired; see
	// internal/config/retired.go for the full table.
	if err := os.WriteFile(path, []byte("scoring:\n  path_priority: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	out, err := runConfigCmd(t, "validate")
	if err == nil {
		t.Fatal("expected an error for a config with a retired key")
	}
	if !strings.Contains(out, path) {
		t.Errorf("output = %q, want it to mention the config path", out)
	}
	if !strings.Contains(err.Error(), "problem") {
		t.Errorf("error = %v, want it to mention the problem count", err)
	}
}
