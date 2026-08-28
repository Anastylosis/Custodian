package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/reclaim"
)

func TestParseAge(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"", reclaim.DefaultAge},
		{"30d", 30 * 24 * time.Hour},
		{"6w", 6 * 7 * 24 * time.Hour},
		{"720h", 720 * time.Hour},
		{"1.5d", 36 * time.Hour},
	} {
		got, err := parseAge(tc.in)
		if err != nil {
			t.Fatalf("parseAge(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("parseAge(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"soon", "xd", "30 days"} {
		if _, err := parseAge(bad); err == nil {
			t.Errorf("parseAge(%q) should fail", bad)
		}
	}
}

func TestFormatMutationsEmpty(t *testing.T) {
	if got := formatMutations(nil); !strings.Contains(got, "mutation log is empty") {
		t.Errorf("formatMutations(nil) = %q", got)
	}
}

func TestFormatMutations(t *testing.T) {
	ok, failed := true, false
	got := formatMutations([]ledger.Mutation{
		{At: "2026-08-28T10:00:00Z", Kind: "move", Target: "file f11",
			From: "/data/tmp/a.mp4", To: "/data/_quarantine/a.mp4", OK: &ok},
		{At: "2026-08-28T09:59:00Z", Kind: "tag", Target: "scene 11", To: "_custodian_quarantine", OK: &failed, Note: "boom"},
		{At: "2026-08-28T09:58:00Z", Kind: "backup", Target: "/backups"},
	})
	for _, want := range []string{
		"move     ok      file f11",
		"/data/tmp/a.mp4 → /data/_quarantine/a.mp4",
		"tag      FAILED  scene 11",
		"— → _custodian_quarantine",
		"boom",
		"backup   PENDING /backups",
		"3 entries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatMutations missing %q:\n%s", want, got)
		}
	}
}

func TestPickTakesTheFirstNonEmpty(t *testing.T) {
	if got := pick("", "b", "c"); got != "b" {
		t.Errorf("pick = %q", got)
	}
	if got := pick("", ""); got != "" {
		t.Errorf("pick = %q", got)
	}
}
