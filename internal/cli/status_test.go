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
		"duplicate groups:  0 (certain 0, conflicted 0, dismissed 0, partial 0, mistag 0, probable 0, rejected 0)\n" +
		"partial:           0 (0 B)\n" +
		"mistag:            0\n" +
		"probable:          0 (0 B) — review only\n" +
		"rejected:          0 (0 B)\n" +
		"multifile:         0 scenes\n" +
		"reclaimable:       0 B\n" +
		"quarantined:       0 groups (0 B)\n" +
		"reclaimed:         0 groups (0 B)\n" +
		"last sync:         never — run `custodian sync`\n" +
		"last scan:         never — run `custodian scan`\n"
	if got != want {
		t.Errorf("formatStats(zero) =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatStatsWithLastRun(t *testing.T) {
	stats := ledger.Stats{
		ScenesLive: 61218, ScenesGone: 5, FilesLive: 61220,
		ScenesWithMultipleFiles: 59, ScenesWithStashID: 28946, ScenesWithoutStashID: 32272,
		BytesLive: 1 << 40, FilesWithoutPhash: 1400,
		GroupsCertain: 843, GroupsConflicted: 410, GroupsDismissed: 2,
		GroupsPartial: 5, PartialBytes: 6 << 30, GroupsMistag: 7,
		GroupsProbable: 9, ProbableBytes: 7 << 30, GroupsRejected: 1, RejectedBytes: 8 << 30,
		GroupsMultifile:   4,
		ReclaimableBytes:  1<<40 + 1<<39, // 1.5 TiB
		GroupsQuarantined: 12, QuarantinedBytes: 2 << 30,
		GroupsReclaimed: 3, ReclaimedBytes: 4 << 30,
		LastSync: &ledger.Run{StartedAt: "2026-08-28T00:00:00Z", Kind: "incremental", ScenesSeen: 42},
		LastScan: &ledger.Run{StartedAt: "2026-08-28T00:01:00Z", Notes: "certain 843, conflicted 410, dismissed 2"},
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
		"duplicate groups:  1277 (certain 843, conflicted 410, dismissed 2, partial 5, mistag 7, probable 9, rejected 1)",
		"partial:           5 (6.00 GiB)",
		"mistag:            7",
		"probable:          9 (7.00 GiB) — review only",
		"rejected:          1 (8.00 GiB)",
		"multifile:         4 scenes",
		"reclaimable:       1.50 TiB",
		"quarantined:       12 groups (2.00 GiB)",
		"reclaimed:         3 groups (4.00 GiB)",
		"last sync:         2026-08-28T00:00:00Z (incremental), 42 changed",
		"last scan:         2026-08-28T00:01:00Z, certain 843, conflicted 410, dismissed 2",
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
