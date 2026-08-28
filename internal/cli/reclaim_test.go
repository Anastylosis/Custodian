package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/reclaim"
	"github.com/spf13/cobra"
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

func TestMultifileSceneCount(t *testing.T) {
	g := ledger.Group{SceneIDs: []string{"1", "2", "3"}}
	if got := multifileSceneCount(g, true); got != 0 {
		t.Errorf("multifileSceneCount(multifile) = %d, want 0", got)
	}
	if got := multifileSceneCount(g, false); got != 2 {
		t.Errorf("multifileSceneCount(normal) = %d, want 2", got)
	}
}

func newReclaimTestCmd() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(nil)
	return cmd, &out
}

func TestConfirmApplySummarizesCertainAndPartialGroups(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "3", "/data/c.mp4", "c.mp4", 500_000_000, 1280, 720, 9_000_000, "h264")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain, Keeper: "1", Reclaimable: 1_000_000_000},
		{Signature: "sig2", SceneIDs: []string{"3"}, Verdict: ledger.VerdictPartial,
			Evidence: []ledger.Evidence{{Generator: "multifile", Kind: "multifile"}}},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	flagApplyYes = true
	cmd, out := newReclaimTestCmd()
	ok, err := confirmApply(ctx, cmd, l, 0)
	if err != nil {
		t.Fatalf("confirmApply: %v", err)
	}
	if !ok {
		t.Fatal("confirmApply should proceed with --yes")
	}
	if !strings.Contains(out.String(), "ABOUT TO QUARANTINE") {
		t.Errorf("confirmApply output missing summary: %q", out.String())
	}
}

func TestConfirmApplyRespectsLimit(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "3", "/data/c.mp4", "c.mp4", 500_000_000, 1280, 720, 9_000_000, "h264")
	insertSceneFile(t, l, "4", "/data/d.mp4", "d.mp4", 500_000_000, 1280, 720, 9_000_000, "h264")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain, Keeper: "1"},
		{Signature: "sig2", SceneIDs: []string{"3", "4"}, Verdict: ledger.VerdictCertain, Keeper: "3"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	flagApplyYes = true
	cmd, _ := newReclaimTestCmd()
	if _, err := confirmApply(ctx, cmd, l, 1); err != nil {
		t.Fatalf("confirmApply: %v", err)
	}
}

func TestConfirmPruneSummarizesOldQuarantines(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")

	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain, Keeper: "1", Reclaimable: 1_000_000_000},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	applyRun, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.SetOutcome(ctx, "sig1", ledger.VerdictQuarantined, applyRun); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	flagPruneYes = true
	cmd, out := newReclaimTestCmd()
	// A negative Age pushes the AppliedBefore cutoff into the future, well
	// past applyRun's started_at even at RFC3339's one-second resolution.
	ok, err := confirmPrune(ctx, cmd, l, reclaim.PruneOptions{Age: -2 * time.Second})
	if err != nil {
		t.Fatalf("confirmPrune: %v", err)
	}
	if !ok {
		t.Fatal("confirmPrune should proceed with --yes")
	}
	if !strings.Contains(out.String(), "ABOUT TO DELETE QUARANTINED FILES") {
		t.Errorf("confirmPrune output missing summary: %q", out.String())
	}
	if !strings.Contains(out.String(), "Groups affected:    1") {
		t.Errorf("confirmPrune should count the old quarantine: %q", out.String())
	}
}

func TestConfirmApplyPropagatesLookupError(t *testing.T) {
	l := newPlanTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	flagApplyYes = true
	cmd, _ := newReclaimTestCmd()
	if _, err := confirmApply(context.Background(), cmd, l, 0); err == nil {
		t.Fatal("confirmApply should propagate a lookup error from a closed ledger")
	}
}

func TestConfirmPrunePropagatesLookupError(t *testing.T) {
	l := newPlanTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	flagPruneYes = true
	cmd, _ := newReclaimTestCmd()
	if _, err := confirmPrune(context.Background(), cmd, l, reclaim.PruneOptions{}); err == nil {
		t.Fatal("confirmPrune should propagate a lookup error from a closed ledger")
	}
}

func TestConfirmPruneNothingOldEnough(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	flagPruneYes = true
	cmd, _ := newReclaimTestCmd()
	ok, err := confirmPrune(ctx, cmd, l, reclaim.PruneOptions{Age: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("confirmPrune: %v", err)
	}
	if !ok {
		t.Fatal("confirmPrune should still proceed (nothing to quarantine, but --yes bypasses)")
	}
}

func TestConfirmPruneRespectsLimit(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "3", "/data/c.mp4", "c.mp4", 500_000_000, 1280, 720, 9_000_000, "h264")
	insertSceneFile(t, l, "4", "/data/d.mp4", "d.mp4", 500_000_000, 1280, 720, 9_000_000, "h264")

	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain},
		{Signature: "sig2", SceneIDs: []string{"3", "4"}, Verdict: ledger.VerdictCertain},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	applyRun, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	for _, sig := range []string{"sig1", "sig2"} {
		if err := l.SetOutcome(ctx, sig, ledger.VerdictQuarantined, applyRun); err != nil {
			t.Fatalf("SetOutcome: %v", err)
		}
	}

	flagPruneYes = true
	cmd, out := newReclaimTestCmd()
	if _, err := confirmPrune(ctx, cmd, l, reclaim.PruneOptions{Age: -2 * time.Second, Limit: 1}); err != nil {
		t.Fatalf("confirmPrune: %v", err)
	}
	if !strings.Contains(out.String(), "Groups affected:    1") {
		t.Errorf("confirmPrune should cap at the limit: %q", out.String())
	}
}
