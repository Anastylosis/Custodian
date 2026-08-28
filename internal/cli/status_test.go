package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
)

func TestFormatStatsNeverSynced(t *testing.T) {
	got := formatStats(ledger.Stats{})
	want := "scenes:            0 (0 gone)\n" +
		"files:             0\n" +
		"scenes >1 file:    0\n" +
		"with stash_id:     0\n" +
		"without stash_id:  0\n" +
		"files no phash:    0\n" +
		"bytes on disk:     0 B\n" +
		"last sync:         never — run `custodian sync`\n"
	if got != want {
		t.Errorf("formatStats(zero) =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatStatsWithLastRun(t *testing.T) {
	stats := ledger.Stats{
		ScenesLive: 61218, ScenesGone: 5, FilesLive: 61220,
		ScenesWithMultipleFiles: 59, ScenesWithStashID: 28946, ScenesWithoutStashID: 32272,
		BytesLive: 1 << 40, FilesWithoutPhash: 1400,
		LastRun: &ledger.Run{StartedAt: "2026-08-28T00:00:00Z", Kind: "incremental", ScenesSeen: 42},
	}
	got := formatStats(stats)
	for _, want := range []string{
		"scenes:            61218 (5 gone)",
		"files:             61220",
		"scenes >1 file:    59",
		"with stash_id:     28946",
		"without stash_id:  32272",
		"files no phash:    1400",
		"bytes on disk:     1.00 TiB",
		"last sync:         2026-08-28T00:00:00Z (incremental), 42 changed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatStats output missing %q, got:\n%s", want, got)
		}
	}
}

func TestFormatReport(t *testing.T) {
	report := mirror.Report{
		Kind: "incremental", Since: "2026-08-01 00:00:00",
		Pages: 3, Scenes: 250, Files: 251, Gone: 0,
		Duration: 90 * time.Second,
	}
	got := formatReport(report)
	want := "sync (incremental)\n" +
		"  since:    2026-08-01 00:00:00\n" +
		"  pages:    3\n" +
		"  scenes:   250\n" +
		"  files:    251\n" +
		"  gone:     0\n" +
		"  duration: 1m30s\n"
	if got != want {
		t.Errorf("formatReport =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatReportOmitsSinceWhenFull(t *testing.T) {
	got := formatReport(mirror.Report{Kind: "full"})
	if strings.Contains(got, "since:") {
		t.Errorf("formatReport(full, no since) should omit the since line, got:\n%s", got)
	}
}
