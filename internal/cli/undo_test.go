package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/reclaim"
	"github.com/spf13/cobra"
)

func TestUndoPreviewGroupsBySignature(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictQuarantined, Reclaimable: 1_000_000_000},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	groups, err := undoPreviewGroups(ctx, l, reclaim.UndoOptions{Signature: "sig1"})
	if err != nil {
		t.Fatalf("undoPreviewGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Signature != "sig1" {
		t.Fatalf("groups = %+v, want [sig1]", groups)
	}
}

func TestUndoPreviewGroupsBySignatureNotFound(t *testing.T) {
	l := newPlanTestLedger(t)
	groups, err := undoPreviewGroups(context.Background(), l, reclaim.UndoOptions{Signature: "nope"})
	if err != nil {
		t.Fatalf("undoPreviewGroups: %v", err)
	}
	if groups != nil {
		t.Errorf("groups = %+v, want nil for an unknown signature", groups)
	}
}

func TestUndoPreviewGroupsByRun(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")

	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain, Reclaimable: 1_000_000_000},
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

	groups, err := undoPreviewGroups(ctx, l, reclaim.UndoOptions{RunID: applyRun})
	if err != nil {
		t.Fatalf("undoPreviewGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Signature != "sig1" {
		t.Fatalf("groups = %+v, want [sig1]", groups)
	}
}

func TestConfirmUndoSummarizesGroups(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "1", "/data/a.mp4", "a.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "2", "/data/b.mp4", "b.mp4", 1_000_000_000, 1920, 1080, 14_000_000, "hevc")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictQuarantined, Reclaimable: 1_000_000_000},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	flagUndoYes = true
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(nil)

	ok, err := confirmUndo(ctx, cmd, l, reclaim.UndoOptions{Signature: "sig1"})
	if err != nil {
		t.Fatalf("confirmUndo: %v", err)
	}
	if !ok {
		t.Fatal("confirmUndo should proceed with --yes")
	}
	if !strings.Contains(out.String(), "ABOUT TO UNDO THE QUARANTINE OF") {
		t.Errorf("confirmUndo output missing summary: %q", out.String())
	}
}

func TestConfirmUndoNothingToUndo(t *testing.T) {
	l := newPlanTestLedger(t)
	flagUndoYes = true
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(nil)

	// An unknown signature is not an error from undoPreviewGroups (it
	// returns an empty slice), so confirmUndo still succeeds with a
	// zero-group summary.
	ok, err := confirmUndo(context.Background(), cmd, l, reclaim.UndoOptions{Signature: "nope"})
	if err != nil {
		t.Fatalf("confirmUndo: %v", err)
	}
	if !ok {
		t.Fatal("confirmUndo should proceed with --yes even with nothing to undo")
	}
}

func TestConfirmUndoPropagatesLookupError(t *testing.T) {
	l := newPlanTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	flagUndoYes = true
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(nil)

	if _, err := confirmUndo(context.Background(), cmd, l, reclaim.UndoOptions{Signature: "sig1"}); err == nil {
		t.Fatal("confirmUndo should propagate a lookup error from a closed ledger")
	}
}
