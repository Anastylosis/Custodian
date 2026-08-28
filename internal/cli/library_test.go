package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
)

func TestLocalDirCandidate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, ok := localDirCandidate(""); ok || got != "" {
		t.Errorf("localDirCandidate(\"\") = %q, %v, want \"\", false", got, ok)
	}
	if got, ok := localDirCandidate(file); ok || got != "" {
		t.Errorf("localDirCandidate(file) = %q, %v, want \"\", false", got, ok)
	}
	if got, ok := localDirCandidate(filepath.Join(dir, "nope")); ok || got != "" {
		t.Errorf("localDirCandidate(missing) = %q, %v, want \"\", false", got, ok)
	}
	got, ok := localDirCandidate(dir)
	if !ok {
		t.Fatalf("localDirCandidate(%q) = _, false, want true", dir)
	}
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("localDirCandidate(%q) = %q, want %q", dir, got, want)
	}
}

func TestLocalDirCandidate_ExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sub := filepath.Join(home, "Videos")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := localDirCandidate("~/Videos")
	if !ok {
		t.Fatal("localDirCandidate(~/Videos) = _, false, want true")
	}
	want, err := filepath.Abs(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("localDirCandidate(~/Videos) = %q, want %q", got, want)
	}
}

func TestOsGetenv(t *testing.T) {
	if got := osGetenv(""); got != "" {
		t.Errorf("osGetenv(\"\") = %q, want empty", got)
	}
	t.Setenv("CUSTODIAN_TEST_VAR", "value")
	if got := osGetenv("CUSTODIAN_TEST_VAR"); got != "value" {
		t.Errorf("osGetenv = %q, want %q", got, "value")
	}
}

func TestPathMapper(t *testing.T) {
	cfg := &config.Config{
		Sidecars: config.SidecarsConfig{
			PathMap: []config.PathMapRule{
				{Stash: "/data", Local: "/mnt/videos"},
			},
		},
	}
	m, err := pathMapper(cfg)
	if err != nil {
		t.Fatalf("pathMapper: %v", err)
	}
	got, err := m.ToLocal("/data/a.mp4")
	if err != nil {
		t.Fatalf("ToLocal: %v", err)
	}
	if want := "/mnt/videos/a.mp4"; got != want {
		t.Errorf("ToLocal = %q, want %q", got, want)
	}
}

func TestPathMapper_InvalidRuleErrors(t *testing.T) {
	cfg := &config.Config{
		Sidecars: config.SidecarsConfig{
			PathMap: []config.PathMapRule{
				{Stash: "relative", Local: "/mnt/videos"},
			},
		},
	}
	if _, err := pathMapper(cfg); err == nil {
		t.Fatal("expected an error for a relative stash prefix")
	}
}

func TestOpenLibraryFS_LocalByDefault(t *testing.T) {
	cfg := &config.Config{}
	fs, cleanup, err := openLibraryFS(context.Background(), cfg)
	if err != nil {
		t.Fatalf("openLibraryFS: %v", err)
	}
	defer cleanup()
	if fs.Describe() != "local filesystem" {
		t.Errorf("Describe = %q, want local filesystem", fs.Describe())
	}
}

func TestOpenLibraryFS_ExplicitLocal(t *testing.T) {
	cfg := &config.Config{Sidecars: config.SidecarsConfig{Access: "local"}}
	fs, cleanup, err := openLibraryFS(context.Background(), cfg)
	if err != nil {
		t.Fatalf("openLibraryFS: %v", err)
	}
	defer cleanup()
	if fs.Describe() != "local filesystem" {
		t.Errorf("Describe = %q, want local filesystem", fs.Describe())
	}
}

func TestOpenLibraryFS_AutoPicksSSHWhenHostConfigured(t *testing.T) {
	cfg := &config.Config{Sidecars: config.SidecarsConfig{
		SSH: config.SidecarSSHConfig{Host: "nas.invalid", User: "me"},
	}}
	// No live SSH: this only asserts that "auto" routes to the ssh backend
	// and surfaces its connection error, rather than silently falling back
	// to local (which would read the wrong filesystem).
	_, _, err := openLibraryFS(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected a connection error dialing an unreachable host")
	}
}

func TestTrimExt(t *testing.T) {
	tests := []struct{ in, want string }{
		{"video.mp4", "video"},
		{"video.en.srt", "video.en"},
		{"noext", "noext"},
		{".hidden", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := trimExt(tc.in); got != tc.want {
			t.Errorf("trimExt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
