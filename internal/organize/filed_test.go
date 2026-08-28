package organize

import (
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
)

func TestFiledUnderBaseDir(t *testing.T) {
	// Stash paths are always "/"-separated, so these are plain literals —
	// filepath.FromSlash would turn them into "\"-paths on Windows and mask
	// a regression in Filed.
	cfg := &config.OrganizeConfig{BaseDir: "/data"}
	cases := []struct {
		path string
		want bool
	}{
		{"/data/Some Performer/2024-01-01_x_1080p.mp4", true},
		{"/data", true},
		{"/data/", true},
		{"/incoming/x.mp4", false},
		{"/data-other/x.mp4", false}, // prefix, not a path component
		{"", false},
	}
	for _, c := range cases {
		if got := Filed(c.path, cfg); got != c.want {
			t.Errorf("Filed(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestFiledNilConfigOrEmptyBaseDir(t *testing.T) {
	if Filed("/data/x.mp4", nil) {
		t.Error("Filed with nil cfg should be false")
	}
	if Filed("/data/x.mp4", &config.OrganizeConfig{}) {
		t.Error("Filed with empty base_dir should be false")
	}
}
