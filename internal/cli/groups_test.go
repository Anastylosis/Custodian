package cli

import (
	"context"
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

// A signature is long and `groups` prints only a prefix of it, with an
// ellipsis. Requiring the full string would mean nobody could act on what
// they just read, so a unique prefix — with or without the ellipsis — has to
// resolve, and an ambiguous one has to refuse rather than pick.
func TestResolveSignatureAcceptsAPrefixAndRefusesAnAmbiguousOne(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 3_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 2_000_000_000, 1280, 720, 9_000_000, "h264")
	insertSceneFile(t, l, "3", "/data/c.mp4", "c.mp4", 1_000_000_000, 1280, 720, 5_000_000, "h264")
	insertSceneFile(t, l, "4", "/data/d.mp4", "d.mp4", 1_000_000_000, 1280, 720, 5_000_000, "h264")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "abcdef111111", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain},
		{Signature: "abcdef222222", SceneIDs: []string{"3", "4"}, Verdict: ledger.VerdictCertain},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	for _, arg := range []string{"abcdef111111", "abcdef1", "abcdef1…"} {
		g, err := resolveSignature(ctx, l, arg)
		if err != nil {
			t.Errorf("resolveSignature(%q): %v", arg, err)
			continue
		}
		if g.Signature != "abcdef111111" {
			t.Errorf("resolveSignature(%q) = %q, want abcdef111111", arg, g.Signature)
		}
	}

	if _, err := resolveSignature(ctx, l, "abcdef"); err == nil {
		t.Error("a prefix matching two groups must refuse rather than pick one")
	}
	if _, err := resolveSignature(ctx, l, "nope"); err == nil {
		t.Error("an unknown signature must be an error")
	}
}

// The point of keying a decision on the group's signature is that it outlives
// the group row, so `scan` can re-apply it. Restoring has to clear it for
// real, or the next scan silently dismisses the group again.
func TestDismissThenRestoreLeavesNoDecisionBehind(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	if err := l.PutDecision(ctx, ledger.Decision{
		Key: "sig1", Kind: "not_duplicate", Notes: "different scenes",
	}); err != nil {
		t.Fatalf("PutDecision: %v", err)
	}
	d, ok, err := l.GetDecision(ctx, "sig1")
	if err != nil || !ok {
		t.Fatalf("GetDecision: %v, found %v", err, ok)
	}
	if d.Notes != "different scenes" {
		t.Errorf("Notes = %q, want the recorded reason", d.Notes)
	}

	if err := l.DeleteDecision(ctx, "sig1"); err != nil {
		t.Fatalf("DeleteDecision: %v", err)
	}
	if _, ok, err := l.GetDecision(ctx, "sig1"); err != nil || ok {
		t.Errorf("decision still present after restore: found %v, err %v", ok, err)
	}
	// Deleting one that was never there is the caller's intent already met.
	if err := l.DeleteDecision(ctx, "sig1"); err != nil {
		t.Errorf("DeleteDecision on a missing key should be a no-op, got %v", err)
	}
}
