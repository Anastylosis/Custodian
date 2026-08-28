package ledger

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestPutCanonicalRoundTrip(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	c := Canonical{SceneID: "1", Source: "stash-box", Origin: "https://sb", Duration: 1137, Title: "Real Title", MatchedBy: "stash_id", FetchedRun: 3}
	if err := l.PutCanonical(ctx, c); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}

	got, err := l.Canonicals(ctx, []string{"1"})
	if err != nil {
		t.Fatalf("Canonicals: %v", err)
	}
	if len(got["1"]) != 1 || !reflect.DeepEqual(got["1"][0], c) {
		t.Fatalf("Canonicals[1] = %+v, want [%+v]", got["1"], c)
	}
}

func TestPutCanonicalUpsertsOnConflict(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	if err := l.PutCanonical(ctx, Canonical{SceneID: "1", Source: "fss", Origin: "/data/fss.db", Duration: 500, FetchedRun: 1}); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}
	if err := l.PutCanonical(ctx, Canonical{SceneID: "1", Source: "fss", Origin: "/data/fss.db", Duration: 501, Title: "Updated", FetchedRun: 2}); err != nil {
		t.Fatalf("PutCanonical (update): %v", err)
	}

	got, err := l.Canonicals(ctx, []string{"1"})
	if err != nil {
		t.Fatalf("Canonicals: %v", err)
	}
	if len(got["1"]) != 1 {
		t.Fatalf("Canonicals[1] = %+v, want exactly 1 row (same source+origin upserts)", got["1"])
	}
	if got["1"][0].Duration != 501 || got["1"][0].Title != "Updated" {
		t.Errorf("row = %+v, want the updated values", got["1"][0])
	}
}

func TestCanonicalsKeepsSourcesSeparate(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	if err := l.PutCanonical(ctx, Canonical{SceneID: "1", Source: "stash-box", Origin: "https://sb", Duration: 1137, FetchedRun: 1}); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}
	if err := l.PutCanonical(ctx, Canonical{SceneID: "1", Source: "fss", Origin: "/data/fss.db", Duration: 600, FetchedRun: 1}); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}

	got, err := l.Canonicals(ctx, []string{"1"})
	if err != nil {
		t.Fatalf("Canonicals: %v", err)
	}
	if len(got["1"]) != 2 {
		t.Fatalf("Canonicals[1] = %+v, want 2 rows (one per source)", got["1"])
	}
}

func TestCanonicalsEmptyInputReturnsEmptyMap(t *testing.T) {
	l := newTestLedger(t)
	got, err := l.Canonicals(context.Background(), nil)
	if err != nil {
		t.Fatalf("Canonicals: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Canonicals(nil) = %+v, want empty", got)
	}
}

func TestScenesNeedingCanonicalFiltersAlreadyFetchedAndOtherVerdicts(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun (scan): %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, []Group{
		{Signature: "sig-conflicted", SceneIDs: []string{"1", "2"}, Verdict: VerdictConflicted, Reason: "x"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	// Scene 1 already has a canonical row; scene 2 does not.
	if err := l.PutCanonical(ctx, Canonical{SceneID: "1", Source: "stash-box", Origin: "https://sb", Duration: 100, FetchedRun: scanRun}); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}

	got, err := l.ScenesNeedingCanonical(ctx, VerdictConflicted)
	if err != nil {
		t.Fatalf("ScenesNeedingCanonical: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"2"}) {
		t.Errorf("ScenesNeedingCanonical(conflicted) = %v, want [2]", got)
	}

	// A verdict with no live groups returns nothing.
	got, err = l.ScenesNeedingCanonical(ctx, VerdictCertain)
	if err != nil {
		t.Fatalf("ScenesNeedingCanonical: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ScenesNeedingCanonical(certain) = %v, want none", got)
	}
}

func TestScenesNeedingCanonicalIgnoresGoneGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun (scan): %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, []Group{
		{Signature: "sig-1", SceneIDs: []string{"1", "2"}, Verdict: VerdictConflicted, Reason: "x"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	// A second scan run that sees nothing tombstones sig-1.
	nextRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun (scan 2): %v", err)
	}
	if err := l.ReplaceGroups(ctx, nextRun, nil); err != nil {
		t.Fatalf("ReplaceGroups (empty): %v", err)
	}

	got, err := l.ScenesNeedingCanonical(ctx, VerdictConflicted)
	if err != nil {
		t.Fatalf("ScenesNeedingCanonical: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ScenesNeedingCanonical = %v, want none (the group is gone)", got)
	}
}

func TestSceneRefsPopulatesIdentityAndStashIDs(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	refs, err := l.SceneRefs(ctx, []string{"1", "2"})
	if err != nil {
		t.Fatalf("SceneRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("SceneRefs returned %d refs, want 2", len(refs))
	}
	byID := map[string]SceneRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}

	one := byID["1"]
	if one.Title != "First" || one.Date != "2024-01-01" || one.Studio != "Studio One" {
		t.Errorf("scene 1 ref = %+v", one)
	}
	if len(one.StashIDs) != 1 || one.StashIDs[0].Endpoint != "https://sb" || one.StashIDs[0].StashID != "uuid-1" {
		t.Errorf("scene 1 StashIDs = %+v", one.StashIDs)
	}
	// twoScenes' scene 1 has two files; f1 (size 100) is primary (index 0).
	if one.Duration != 0 {
		t.Errorf("scene 1 Duration = %v, want 0 (twoScenes sets no duration)", one.Duration)
	}

	two := byID["2"]
	if len(two.StashIDs) != 0 {
		t.Errorf("scene 2 StashIDs = %+v, want none", two.StashIDs)
	}
}

func TestSceneRefsEmptyInputReturnsNil(t *testing.T) {
	l := newTestLedger(t)
	got, err := l.SceneRefs(context.Background(), nil)
	if err != nil {
		t.Fatalf("SceneRefs: %v", err)
	}
	if got != nil {
		t.Errorf("SceneRefs(nil) = %+v, want nil", got)
	}
}

func TestSceneRefsSkipsUnknownSceneIDs(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	refs, err := l.SceneRefs(ctx, []string{"1", "does-not-exist"})
	if err != nil {
		t.Fatalf("SceneRefs: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != "1" {
		t.Fatalf("SceneRefs = %+v, want just scene 1", refs)
	}
}

func TestUpdateVerdictReplacesVerdictReasonAndCanonicalEvidenceOnly(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	g := Group{
		Signature: "sig-1", SceneIDs: []string{"1", "2"},
		Evidence: []Evidence{
			{Generator: "stash_id", Kind: "shared_stash_id", Detail: "https://sb uuid-1"},
		},
		Verdict: VerdictConflicted, Reason: "durations disagree",
	}
	if err := l.ReplaceGroups(ctx, runID, []Group{g}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	newEvidence := []Evidence{
		{Generator: "canonical_duration", Kind: "canonical_duration", Detail: "1: fss: 600s actual vs 601s canonical (match)"},
	}
	if err := l.UpdateVerdict(ctx, "sig-1", VerdictPartial, "scene 1 matches canonical; other(s) shorter", newEvidence); err != nil {
		t.Fatalf("UpdateVerdict: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Groups = %d, want 1", len(got))
	}
	gg := got[0]
	if gg.Verdict != VerdictPartial {
		t.Errorf("Verdict = %q, want partial", gg.Verdict)
	}
	if gg.Reason != "scene 1 matches canonical; other(s) shorter" {
		t.Errorf("Reason = %q", gg.Reason)
	}
	// Scene membership is untouched.
	if !reflect.DeepEqual(gg.SceneIDs, []string{"1", "2"}) {
		t.Errorf("SceneIDs = %v, want [1 2] (unchanged)", gg.SceneIDs)
	}
	// The original stash_id evidence survives; the canonical_duration row
	// was added.
	var kinds []string
	for _, e := range gg.Evidence {
		kinds = append(kinds, e.Generator+":"+e.Kind)
	}
	sort.Strings(kinds)
	want := []string{"canonical_duration:canonical_duration", "stash_id:shared_stash_id"}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("evidence generators = %v, want %v", kinds, want)
	}
}

func TestUpdateVerdictReplacesCanonicalEvidenceWholesaleOnRepeatedCalls(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []Group{
		{Signature: "sig-1", SceneIDs: []string{"1"}, Verdict: VerdictConflicted, Reason: "x"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	first := []Evidence{{Generator: "canonical_duration", Kind: "canonical_duration", Detail: "first"}}
	if err := l.UpdateVerdict(ctx, "sig-1", VerdictMistag, "r1", first); err != nil {
		t.Fatalf("UpdateVerdict (1): %v", err)
	}
	second := []Evidence{{Generator: "canonical_duration", Kind: "canonical_duration", Detail: "second"}}
	if err := l.UpdateVerdict(ctx, "sig-1", VerdictMistag, "r2", second); err != nil {
		t.Fatalf("UpdateVerdict (2): %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got[0].Evidence) != 1 || got[0].Evidence[0].Detail != "second" {
		t.Fatalf("Evidence = %+v, want just the second call's row", got[0].Evidence)
	}
}
