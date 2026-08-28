package organize

import (
	"path/filepath"
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
)

func TestFiledUnderBaseDir(t *testing.T) {
	cfg := &config.OrganizeConfig{BaseDir: filepath.FromSlash("/data")}
	cases := []struct {
		path string
		want bool
	}{
		{filepath.FromSlash("/data/Some Performer/2024-01-01_x_1080p.mp4"), true},
		{filepath.FromSlash("/data"), true},
		{filepath.FromSlash("/data/"), true},
		{filepath.FromSlash("/incoming/x.mp4"), false},
		{filepath.FromSlash("/data-other/x.mp4"), false}, // prefix, not a path component
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
