package ledger

import (
	"context"
	"sort"
	"testing"
)

func seedFilingScenes(t *testing.T, l *Ledger) int64 {
	t.Helper()
	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	return runID
}

func TestFilingInputsReturnsLiveScenesWithPrimaryFile(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedFilingScenes(t, l)

	got, err := l.FilingInputs(ctx, FilingFilter{})
	if err != nil {
		t.Fatalf("FilingInputs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}

	one := got[0]
	if one.SceneID != "1" || one.Title != "First" || one.Date != "2024-01-01" || one.StudioName != "Studio One" {
		t.Errorf("scene 1 = %+v", one)
	}
	if one.FileID != "f1" || one.Path != "/lib/a.mp4" || one.Basename != "a.mp4" || one.Height != 1080 || one.Size != 100 {
		t.Errorf("scene 1 primary file = %+v", one)
	}
	perf := append([]string{}, one.Performers...)
	sort.Strings(perf)
	if len(perf) != 2 || perf[0] != "Alice" || perf[1] != "Bob" {
		t.Errorf("scene 1 performers = %v, want [Alice Bob]", perf)
	}

	two := got[1]
	if two.SceneID != "2" || two.StudioName != "" || len(two.Performers) != 0 {
		t.Errorf("scene 2 = %+v, want no studio and no performers", two)
	}
	if two.FileID != "f3" || two.Path != "/lib/b.mp4" {
		t.Errorf("scene 2 primary file = %+v", two)
	}
}

func TestFilingInputsRespectsLimit(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedFilingScenes(t, l)

	got, err := l.FilingInputs(ctx, FilingFilter{Limit: 1})
	if err != nil {
		t.Fatalf("FilingInputs: %v", err)
	}
	if len(got) != 1 || got[0].SceneID != "1" {
		t.Fatalf("got %+v, want just scene 1", got)
	}
}

func TestFilingInputsSkipsTombstonedScenes(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedFilingScenes(t, l)

	// A second full run that only re-sees scene 1 tombstones scene 2.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := twoScenes()
	if err := l.UpsertScenes(ctx, run2, scenes[:1]); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	got, err := l.FilingInputs(ctx, FilingFilter{})
	if err != nil {
		t.Fatalf("FilingInputs: %v", err)
	}
	if len(got) != 1 || got[0].SceneID != "1" {
		t.Fatalf("got %+v, want just live scene 1", got)
	}
}

func TestDuplicateLoserSceneIDsExcludesKeeperAndOtherVerdicts(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedFilingScenes(t, l)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	groups := []Group{
		{Signature: "partial-sig", SceneIDs: []string{"1", "2"}, Verdict: VerdictPartial, Reason: "clip"},
	}
	if err := l.ReplaceGroups(ctx, runID, groups); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.SetChoice(ctx, "partial-sig", "1", []string{"2"}, nil); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	losers, err := l.DuplicateLoserSceneIDs(ctx)
	if err != nil {
		t.Fatalf("DuplicateLoserSceneIDs: %v", err)
	}
	if !losers["2"] {
		t.Errorf("losers = %v, want scene 2 present", losers)
	}
	if losers["1"] {
		t.Errorf("losers = %v, keeper scene 1 must not be a loser", losers)
	}
}

func TestDuplicateLoserSceneIDsIgnoresCertainVerdict(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedFilingScenes(t, l)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	groups := []Group{
		{Signature: "certain-sig", SceneIDs: []string{"1", "2"}, Verdict: VerdictCertain, Reason: "shared stash_id"},
	}
	if err := l.ReplaceGroups(ctx, runID, groups); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.SetChoice(ctx, "certain-sig", "1", []string{"2"}, nil); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	losers, err := l.DuplicateLoserSceneIDs(ctx)
	if err != nil {
		t.Fatalf("DuplicateLoserSceneIDs: %v", err)
	}
	if len(losers) != 0 {
		t.Errorf("losers = %v, want none — a `certain` group is not yet about to leave", losers)
	}
}

func TestLiveFilePathsCoversEveryFileNotJustPrimary(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedFilingScenes(t, l)

	paths, err := l.LiveFilePaths(ctx)
	if err != nil {
		t.Fatalf("LiveFilePaths: %v", err)
	}
	want := map[string]string{
		"/lib/a.mp4":     "1",
		"/lib/a.dup.mp4": "1",
		"/lib/b.mp4":     "2",
	}
	for path, sceneID := range want {
		if paths[path] != sceneID {
			t.Errorf("paths[%q] = %q, want %q", path, paths[path], sceneID)
		}
	}
	if len(paths) != len(want) {
		t.Errorf("len(paths) = %d, want %d (%v)", len(paths), len(want), paths)
	}
}
