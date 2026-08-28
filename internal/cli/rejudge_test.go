package cli

import (
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/evidence"
)

func TestFormatRejudge(t *testing.T) {
	sum := evidence.RejudgeSummary{
		ToCertain: 3, ToPartial: 2, ToMistag: 1,
		Duration: 12 * time.Millisecond,
		Examples: []evidence.RejudgeExample{
			{Signature: "hash1+hash2", From: "conflicted", To: "certain", Reason: "matches canonical duration"},
			{Signature: "hash3+hash4", From: "conflicted", To: "mistag", Reason: "sources disagree"},
		},
	}
	got := formatRejudge(sum)
	want := "rejudge\n" +
		"  -> certain:  3\n" +
		"  -> partial:  2\n" +
		"  -> mistag:   1\n" +
		"  duration:    12ms\n" +
		"examples:\n" +
		"  hash1+hash2: conflicted -> certain (matches canonical duration)\n" +
		"  hash3+hash4: conflicted -> mistag (sources disagree)\n"
	if got != want {
		t.Errorf("formatRejudge =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatRejudgeZeroOmitsExamples(t *testing.T) {
	got := formatRejudge(evidence.RejudgeSummary{})
	want := "rejudge\n" +
		"  -> certain:  0\n" +
		"  -> partial:  0\n" +
		"  -> mistag:   0\n" +
		"  duration:    0s\n"
	if got != want {
		t.Errorf("formatRejudge(zero) =\n%q\nwant\n%q", got, want)
	}
}
