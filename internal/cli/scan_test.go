package cli

import (
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/evidence"
)

func TestFormatSummary(t *testing.T) {
	summary := evidence.Summary{
		Certain: 843, CertainBytes: 967307411456, // ~0.88 TiB
		Conflicted: 410, ConflictedBytes: 461611606016, // ~0.42 TiB
		Dismissed: 2, DismissedBytes: 0,
		Duration: 340 * time.Millisecond,
	}
	got := formatSummary(summary)
	want := "scan\n" +
		"  certain:     843 (900.88 GiB)\n" +
		"  conflicted:  410 (429.91 GiB)\n" +
		"  dismissed:   2 (0 B)\n" +
		"  duration:    340ms\n"
	if got != want {
		t.Errorf("formatSummary =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatSummaryZero(t *testing.T) {
	got := formatSummary(evidence.Summary{})
	want := "scan\n" +
		"  certain:     0 (0 B)\n" +
		"  conflicted:  0 (0 B)\n" +
		"  dismissed:   0 (0 B)\n" +
		"  duration:    0s\n"
	if got != want {
		t.Errorf("formatSummary(zero) =\n%q\nwant\n%q", got, want)
	}
}
