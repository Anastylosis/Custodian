package cli

import (
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

func TestShortSignature(t *testing.T) {
	cases := map[string]string{
		"hashA+hashB":               "hashA+hashB",
		"208a7a741b36ca2f+cac1a1b8": "208a7a741b36…",
	}
	for in, want := range cases {
		if got := shortSignature(in); got != want {
			t.Errorf("shortSignature(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatGroups(t *testing.T) {
	groups := []ledger.Group{
		{
			Signature:   "hashA+hashB",
			SceneIDs:    []string{"1", "2"},
			Verdict:     ledger.VerdictCertain,
			Reason:      "durations agree",
			Reclaimable: 1 << 30, // 1 GiB
		},
	}
	files := map[string]ledger.PrimaryFile{
		"1": {Duration: 1741, Size: 1 << 31, Basename: "scene.mp4"},
		"2": {Duration: 1743, Size: 1 << 30, Basename: "scene.dup.mp4"},
	}

	got := formatGroups(groups, files)
	want := "group hashA+hashB (certain)\n" +
		"  reason: durations agree\n" +
		"  scenes:\n" +
		"    1 · 29m1s · 2.00 GiB · scene.mp4\n" +
		"    2 · 29m3s · 1.00 GiB · scene.dup.mp4\n" +
		"  reclaimable: 1.00 GiB\n\n"
	if got != want {
		t.Errorf("formatGroups =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatGroupsMissingPrimaryFile(t *testing.T) {
	groups := []ledger.Group{
		{Signature: "scene:99", SceneIDs: []string{"99"}, Verdict: ledger.VerdictConflicted, Reason: "245s vs 1137s"},
	}
	got := formatGroups(groups, map[string]ledger.PrimaryFile{})
	want := "group scene:99 (conflicted)\n" +
		"  reason: 245s vs 1137s\n" +
		"  scenes:\n" +
		"    99 · 0s · 0 B · \n" +
		"  reclaimable: 0 B\n\n"
	if got != want {
		t.Errorf("formatGroups (missing file) =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatGroupsEmpty(t *testing.T) {
	if got := formatGroups(nil, nil); got != "" {
		t.Errorf("formatGroups(nil) = %q, want empty", got)
	}
}
