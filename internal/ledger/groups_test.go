package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	stash "github.com/Anastylosis/stash-go"
)

func float64Ptr(v float64) *float64 { return &v }

func sampleGroups() []Group {
	return []Group{
		{
			Signature: "hash1+hash2",
			SceneIDs:  []string{"1", "2"},
			Evidence: []Evidence{
				{Generator: "stash_id", Kind: "shared_stash_id", Detail: "https://sb uuid-1"},
				{Generator: "stash_id", Kind: "duration_spread", Detail: "100s vs 102s", Value: float64Ptr(2)},
			},
			Verdict:     VerdictCertain,
			Reason:      "durations agree",
			Reclaimable: 90,
		},
		{
			Signature: "hash3+hash4+hash5",
			SceneIDs:  []string{"3", "4", "5"},
			Evidence: []Evidence{
				{Generator: "stash_id", Kind: "shared_stash_id", Detail: "https://sb uuid-2"},
			},
			Verdict:     VerdictConflicted,
			Reason:      "245s vs 1137s",
			Reclaimable: 500,
		},
	}
}

func TestReplaceGroupsRoundTrip(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Groups returned %d groups, want 2", len(got))
	}

	g := got[0]
	if g.Signature != "hash1+hash2" {
		t.Errorf("Signature = %q, want hash1+hash2", g.Signature)
	}
	if g.Verdict != VerdictCertain || g.Reason != "durations agree" || g.Reclaimable != 90 {
		t.Errorf("group = %+v, want certain/durations agree/90", g)
	}
	if len(g.SceneIDs) != 2 || g.SceneIDs[0] != "1" || g.SceneIDs[1] != "2" {
		t.Errorf("SceneIDs = %v, want [1 2]", g.SceneIDs)
	}
	if len(g.Evidence) != 2 {
		t.Fatalf("Evidence = %v, want 2 entries", g.Evidence)
	}
	if g.Evidence[0].Kind != "shared_stash_id" || g.Evidence[0].Value != nil {
		t.Errorf("Evidence[0] = %+v, want shared_stash_id with nil Value", g.Evidence[0])
	}
	if g.Evidence[1].Value == nil || *g.Evidence[1].Value != 2 {
		t.Errorf("Evidence[1].Value = %v, want 2", g.Evidence[1].Value)
	}
}

func TestGroupsFilterByVerdict(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	certain, err := l.Groups(ctx, GroupFilter{Verdict: VerdictCertain})
	if err != nil {
		t.Fatalf("Groups(certain): %v", err)
	}
	if len(certain) != 1 || certain[0].Signature != "hash1+hash2" {
		t.Errorf("Groups(certain) = %+v, want just hash1+hash2", certain)
	}

	conflicted, err := l.Groups(ctx, GroupFilter{Verdict: VerdictConflicted})
	if err != nil {
		t.Fatalf("Groups(conflicted): %v", err)
	}
	if len(conflicted) != 1 || conflicted[0].Signature != "hash3+hash4+hash5" {
		t.Errorf("Groups(conflicted) = %+v, want just hash3+hash4+hash5", conflicted)
	}
}

func TestReplaceGroupsSetsGoneRunOnASecondRun(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run1, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups 1: %v", err)
	}

	// Second run only sees the first group — the second group's evidence
	// vanished (e.g. the shared stash_id no longer clusters).
	run2, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run2, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups 2: %v", err)
	}

	live, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups (live): %v", err)
	}
	if len(live) != 1 || live[0].Signature != "hash1+hash2" {
		t.Errorf("live Groups = %+v, want just hash1+hash2", live)
	}

	all, err := l.Groups(ctx, GroupFilter{IncludeGone: true})
	if err != nil {
		t.Fatalf("Groups (include gone): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Groups(IncludeGone) = %d, want 2", len(all))
	}

	var goneRun sql.NullInt64
	if err := l.DB().QueryRowContext(ctx,
		`SELECT gone_run FROM candidate_group WHERE signature = ?`, "hash3+hash4+hash5").
		Scan(&goneRun); err != nil {
		t.Fatalf("reading gone_run: %v", err)
	}
	if !goneRun.Valid || goneRun.Int64 != run2 {
		t.Errorf("gone_run = %v, want %d", goneRun, run2)
	}
}

func TestReplaceGroupsReplacesEvidenceAndScenesWholesale(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run1, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups 1: %v", err)
	}

	// Same signature, different verdict/scenes/evidence — must fully replace,
	// not accumulate.
	changed := sampleGroups()[0]
	changed.Verdict = VerdictDismissed
	changed.Reason = "operator marked not_duplicate"
	changed.SceneIDs = []string{"1", "2", "6"}
	changed.Evidence = []Evidence{{Generator: "stash_id", Kind: "shared_stash_id", Detail: "https://sb uuid-1"}}

	run2, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run2, []Group{changed}); err != nil {
		t.Fatalf("ReplaceGroups 2: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Groups = %d, want 1", len(got))
	}
	g := got[0]
	if g.Verdict != VerdictDismissed || g.Reason != "operator marked not_duplicate" {
		t.Errorf("group = %+v, want dismissed/operator marked not_duplicate", g)
	}
	if len(g.SceneIDs) != 3 {
		t.Errorf("SceneIDs = %v, want 3 scenes", g.SceneIDs)
	}
	if len(g.Evidence) != 1 {
		t.Errorf("Evidence = %v, want 1 entry", g.Evidence)
	}
}

func TestStatsGroupFields(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.GroupsCertain != 1 {
		t.Errorf("GroupsCertain = %d, want 1", stats.GroupsCertain)
	}
	if stats.GroupsConflicted != 1 {
		t.Errorf("GroupsConflicted = %d, want 1", stats.GroupsConflicted)
	}
	if stats.GroupsDismissed != 0 {
		t.Errorf("GroupsDismissed = %d, want 0", stats.GroupsDismissed)
	}
	if stats.ReclaimableBytes != 590 {
		t.Errorf("ReclaimableBytes = %d, want 590", stats.ReclaimableBytes)
	}
}

func TestStatsCountsMultifileGroupsByEvidenceKind(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	groups := []Group{
		{
			Signature: "scene:9", SceneIDs: []string{"9"}, Verdict: VerdictCertain,
			Evidence: []Evidence{{Generator: "multifile", Kind: "multifile", Detail: "a; b", Value: float64Ptr(2)}},
		},
		{Signature: "hash1+hash2", SceneIDs: []string{"1", "2"}, Verdict: VerdictCertain,
			Evidence: []Evidence{{Generator: "stash_id", Kind: "shared_stash_id"}}},
	}
	if err := l.ReplaceGroups(ctx, runID, groups); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.GroupsMultifile != 1 {
		t.Errorf("GroupsMultifile = %d, want 1", stats.GroupsMultifile)
	}
}

func TestStatsFailsWhenCandidateEvidenceTableIsMissingForMultifileCount(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_evidence`); err != nil {
		t.Fatalf("dropping candidate_evidence: %v", err)
	}
	if _, err := l.Stats(ctx); err == nil {
		t.Error("Stats should fail when candidate_evidence is missing")
	}
}

func TestStatsGroupsIgnoresGoneGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run1, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups 1: %v", err)
	}
	run2, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run2, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups 2: %v", err)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.GroupsCertain != 1 || stats.GroupsConflicted != 0 {
		t.Errorf("stats = %+v, want certain=1 conflicted=0 after the gone group drops out", stats)
	}
	if stats.ReclaimableBytes != 90 {
		t.Errorf("ReclaimableBytes = %d, want 90", stats.ReclaimableBytes)
	}
}

func TestSharedStashIDs(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		{
			ID: "1", UpdatedAt: "2024-01-01",
			StashIDs: []stash.StashID{{Endpoint: "https://sb", ID: "uuid-1"}},
			Files: []stash.File{{ID: "f1", Duration: 100, Size: 10,
				Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "h1"}}}},
		},
		{
			ID: "2", UpdatedAt: "2024-01-01",
			StashIDs: []stash.StashID{{Endpoint: "https://sb", ID: "uuid-1"}},
			Files: []stash.File{{ID: "f2", Duration: 102, Size: 20,
				Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "h2"}}}},
		},
		{
			// Not clustered: the only scene with this stash_id.
			ID: "3", UpdatedAt: "2024-01-01",
			StashIDs: []stash.StashID{{Endpoint: "https://sb", ID: "uuid-solo"}},
			Files:    []stash.File{{ID: "f3", Duration: 50, Size: 30}},
		},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	clusters, err := l.SharedStashIDs(ctx)
	if err != nil {
		t.Fatalf("SharedStashIDs: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("SharedStashIDs = %d clusters, want 1", len(clusters))
	}
	c := clusters[0]
	if c.Endpoint != "https://sb" || c.StashID != "uuid-1" {
		t.Errorf("cluster = %+v, want https://sb/uuid-1", c)
	}
	if len(c.Scenes) != 2 {
		t.Fatalf("cluster scenes = %d, want 2", len(c.Scenes))
	}
	if c.Scenes[0].SceneID != "1" || c.Scenes[0].Oshash != "h1" || c.Scenes[0].Duration != 100 {
		t.Errorf("cluster.Scenes[0] = %+v", c.Scenes[0])
	}
	if c.Scenes[1].SceneID != "2" || c.Scenes[1].Oshash != "h2" || c.Scenes[1].Duration != 102 {
		t.Errorf("cluster.Scenes[1] = %+v", c.Scenes[1])
	}
}

func TestPrimaryFiles(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	files, err := l.PrimaryFiles(ctx, []string{"1", "2", "no-such-scene"})
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("PrimaryFiles = %d entries, want 2", len(files))
	}
	if files["1"].Basename != "a.mp4" || files["1"].Size != 100 || files["1"].Oshash != "hash1" {
		t.Errorf("PrimaryFiles[1] = %+v", files["1"])
	}
	if files["2"].Basename != "b.mp4" || files["2"].Size != 50 || files["2"].Oshash != "" {
		t.Errorf("PrimaryFiles[2] = %+v", files["2"])
	}
}

func TestSceneStashIDs(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.SceneStashIDs(ctx, []string{"1", "2", "no-such-scene"})
	if err != nil {
		t.Fatalf("SceneStashIDs: %v", err)
	}
	if len(got["1"]) != 1 || got["1"][0].Endpoint != "https://sb" || got["1"][0].StashID != "uuid-1" {
		t.Errorf("SceneStashIDs[1] = %+v", got["1"])
	}
	if len(got["2"]) != 0 {
		t.Errorf("SceneStashIDs[2] = %+v, want none (scene 2 carries no stash_id)", got["2"])
	}
}

func TestSceneStashIDsFailsWhenTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE scene_stash_id`); err != nil {
		t.Fatalf("dropping scene_stash_id: %v", err)
	}
	if _, err := l.SceneStashIDs(ctx, []string{"1"}); err == nil {
		t.Error("SceneStashIDs should fail when scene_stash_id is missing")
	}
}

func TestSceneStashIDsEmptyInput(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	got, err := l.SceneStashIDs(ctx, nil)
	if err != nil {
		t.Fatalf("SceneStashIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("SceneStashIDs(nil) = %v, want empty", got)
	}
}

func TestExistingSignatures(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	before, err := l.ExistingSignatures(ctx)
	if err != nil {
		t.Fatalf("ExistingSignatures (empty ledger): %v", err)
	}
	if len(before) != 0 {
		t.Errorf("ExistingSignatures on an empty ledger = %v, want empty", before)
	}

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	after, err := l.ExistingSignatures(ctx)
	if err != nil {
		t.Fatalf("ExistingSignatures: %v", err)
	}
	if !after["hash1+hash2"] || !after["hash3+hash4+hash5"] {
		t.Errorf("ExistingSignatures = %v, want both sample signatures", after)
	}
}

func TestExistingSignaturesFailsWhenTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_group`); err != nil {
		t.Fatalf("dropping candidate_group: %v", err)
	}
	if _, err := l.ExistingSignatures(ctx); err == nil {
		t.Error("ExistingSignatures should fail when candidate_group is missing")
	}
}

func TestExistingSignaturesIncludesGoneGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run1, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups 1: %v", err)
	}
	run2, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	// The second group's signature vanishes from this run's live set, but
	// ExistingSignatures should still report it — a generator finding it
	// again later is "known", not "new".
	if err := l.ReplaceGroups(ctx, run2, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups 2: %v", err)
	}

	got, err := l.ExistingSignatures(ctx)
	if err != nil {
		t.Fatalf("ExistingSignatures: %v", err)
	}
	if !got["hash3+hash4+hash5"] {
		t.Error("ExistingSignatures should still report a gone group's signature")
	}
}

func TestReplaceGroupsPersistsGeneratorSuppliedSteps(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	g := Group{
		Signature: "scene:9", SceneIDs: []string{"9"}, Verdict: VerdictCertain,
		Steps: []Step{{Rule: "file_choice", Outcome: "filed", Detail: "f1"}},
	}
	if err := l.ReplaceGroups(ctx, runID, []Group{g}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Groups = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0].Steps, g.Steps) {
		t.Errorf("Steps = %+v, want %+v", got[0].Steps, g.Steps)
	}
}

func TestReplaceGroupsLeavesStepsAloneWhenAGroupCarriesNone(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"},
		[]Step{{Rule: "tier", Outcome: "1 wins"}}); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	// A second scan run for the same signature that supplies no Steps
	// (the ordinary case — plan, not the generator, owns Steps) must not
	// wipe out what SetChoice wrote.
	run2, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.ReplaceGroups(ctx, run2, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups 2: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got[0].Steps) != 1 || got[0].Steps[0].Rule != "tier" {
		t.Errorf("Steps = %+v, want the tier step from SetChoice preserved", got[0].Steps)
	}
}

func TestPrimaryFilesEmptyInput(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	files, err := l.PrimaryFiles(ctx, nil)
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("PrimaryFiles(nil) = %v, want empty", files)
	}
}

func TestSetChoiceRoundTrip(t *testing.T) {
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
	group := Group{Signature: "hash1+hash2", SceneIDs: []string{"1", "2"}, Verdict: VerdictCertain, Reason: "durations agree"}
	if err := l.ReplaceGroups(ctx, scanRun, []Group{group}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	steps := []Step{
		{Rule: "tier", Detail: "1080p vs 720p", Outcome: "1 wins"},
	}
	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"}, steps); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Groups = %d, want 1", len(got))
	}
	g := got[0]
	if g.Keeper != "1" {
		t.Errorf("Keeper = %q, want 1", g.Keeper)
	}
	// twoScenes: scene 1's primary file (f1) is 100 bytes, scene 2's (f3) is
	// 50 — the loser's size is what SetChoice recomputes reclaimable from.
	if g.Reclaimable != 50 {
		t.Errorf("Reclaimable = %d, want 50 (loser scene 2's primary file size)", g.Reclaimable)
	}
	if !reflect.DeepEqual(g.Steps, steps) {
		t.Errorf("Steps = %+v, want %+v", g.Steps, steps)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ReclaimableBytes != 50 {
		t.Errorf("Stats.ReclaimableBytes = %d, want 50 (SetChoice's recompute reflected in status)", stats.ReclaimableBytes)
	}
}

func TestSetChoiceReplacesStepsWholesale(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"},
		[]Step{{Rule: "tier", Outcome: "tie"}, {Rule: "bitrate", Outcome: "1 wins"}}); err != nil {
		t.Fatalf("SetChoice (first): %v", err)
	}
	if err := l.SetChoice(ctx, "hash1+hash2", "2", []string{"1"},
		[]Step{{Rule: "size", Outcome: "2 wins"}}); err != nil {
		t.Fatalf("SetChoice (second): %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	g := got[0]
	if g.Keeper != "2" {
		t.Errorf("Keeper = %q, want 2 (second SetChoice wins)", g.Keeper)
	}
	want := []Step{{Rule: "size", Outcome: "2 wins"}}
	if !reflect.DeepEqual(g.Steps, want) {
		t.Errorf("Steps = %+v, want %+v (old steps fully replaced, not accumulated)", g.Steps, want)
	}
}

func TestGroupsReturnsStepsInOrder(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	steps := []Step{
		{Rule: "tier", Detail: "1080p vs 1080p", Outcome: "tie"},
		{Rule: "bitrate", Detail: "14.5 vs 14.4 Mbps", Outcome: "tie under 10%"},
		{Rule: "codec", Detail: "hevc vs h264", Outcome: "1 wins"},
	}
	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"}, steps); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if !reflect.DeepEqual(got[0].Steps, steps) {
		t.Errorf("Steps = %+v, want %+v in the same order", got[0].Steps, steps)
	}
}

func TestGroupsKeeperEmptyBeforePlanRuns(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	for _, g := range got {
		if g.Keeper != "" {
			t.Errorf("group %s Keeper = %q, want empty before plan runs", g.Signature, g.Keeper)
		}
		if len(g.Steps) != 0 {
			t.Errorf("group %s Steps = %v, want none before plan runs", g.Signature, g.Steps)
		}
	}
}

func TestReplaceGroupsFailsWhenCandidateSceneTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_scene`); err != nil {
		t.Fatalf("dropping candidate_scene: %v", err)
	}
	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err == nil {
		t.Error("ReplaceGroups should fail when candidate_scene is missing")
	}
}

func TestReplaceGroupsFailsWhenCandidateEvidenceTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_evidence`); err != nil {
		t.Fatalf("dropping candidate_evidence: %v", err)
	}
	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err == nil {
		t.Error("ReplaceGroups should fail when candidate_evidence is missing")
	}
}

func TestSetChoiceFailsWhenCandidateGroupTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_step`); err != nil {
		t.Fatalf("dropping candidate_step: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_group`); err != nil {
		t.Fatalf("dropping candidate_group: %v", err)
	}
	if err := l.SetChoice(ctx, "sig", "1", nil, nil); err == nil {
		t.Error("SetChoice should fail when candidate_group is missing")
	}
}

func TestSetChoiceFailsWhenCandidateStepTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_step`); err != nil {
		t.Fatalf("dropping candidate_step: %v", err)
	}
	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"}, nil); err == nil {
		t.Error("SetChoice should fail when candidate_step is missing")
	}
}

func TestSumPrimaryFileSizesFailsWhenFileTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE file`); err != nil {
		t.Fatalf("dropping file: %v", err)
	}
	if err := l.SetChoice(ctx, "sig", "1", []string{"2"}, nil); err == nil {
		t.Error("SetChoice should fail when file is missing (sumPrimaryFileSizes)")
	}
}

func TestReplaceGroupsFailsWhenCandidateGroupTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_evidence`); err != nil {
		t.Fatalf("dropping candidate_evidence: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_scene`); err != nil {
		t.Fatalf("dropping candidate_scene: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_group`); err != nil {
		t.Fatalf("dropping candidate_group: %v", err)
	}

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err == nil {
		t.Error("ReplaceGroups should fail when candidate_group is missing")
	}
}

// A BEFORE trigger that RAISEs lets a statement prepare cleanly against a
// table that still exists, but fails at exec time — the only way to reach
// upsertGroup's per-statement error branches without a table missing at
// prepare time (which fails one statement earlier, in prepareGroupStatements).
func blockWith(t *testing.T, l *Ledger, event, table string) {
	t.Helper()
	trigger := fmt.Sprintf(`CREATE TRIGGER blk_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'blocked'); END`,
		event, table, event, table)
	if _, err := l.DB().Exec(trigger); err != nil {
		t.Fatalf("creating blocking trigger on %s %s: %v", event, table, err)
	}
}

func TestReplaceGroupsFailsWhenAnInsertIsBlockedAtExecTime(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{"group", "candidate_group"},
		{"scene", "candidate_scene"},
		{"evidence", "candidate_evidence"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			l := newTestLedger(t)
			blockWith(t, l, "INSERT", c.table)

			runID, err := l.BeginRun(ctx, "scan", "")
			if err != nil {
				t.Fatalf("BeginRun: %v", err)
			}
			if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err == nil {
				t.Errorf("ReplaceGroups should fail when %s INSERT is blocked", c.table)
			}
		})
	}
}

func TestReplaceGroupsFailsWhenADeleteIsBlockedAtExecTime(t *testing.T) {
	cases := []struct {
		name  string
		table string
	}{
		{"scene", "candidate_scene"},
		{"evidence", "candidate_evidence"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			l := newTestLedger(t)

			run1, err := l.BeginRun(ctx, "scan", "")
			if err != nil {
				t.Fatalf("BeginRun 1: %v", err)
			}
			if err := l.ReplaceGroups(ctx, run1, sampleGroups()[:1]); err != nil {
				t.Fatalf("ReplaceGroups 1: %v", err)
			}

			blockWith(t, l, "DELETE", c.table)

			run2, err := l.BeginRun(ctx, "scan", "")
			if err != nil {
				t.Fatalf("BeginRun 2: %v", err)
			}
			if err := l.ReplaceGroups(ctx, run2, sampleGroups()[:1]); err == nil {
				t.Errorf("ReplaceGroups should fail when %s DELETE is blocked", c.table)
			}
		})
	}
}

func TestSetOutcomeRoundTrip(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun (scan): %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	applyRun, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun (apply): %v", err)
	}
	if err := l.SetOutcome(ctx, "hash1+hash2", VerdictQuarantined, applyRun); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{Verdict: VerdictQuarantined})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Groups(quarantined) = %d, want 1", len(got))
	}
	g := got[0]
	if g.Verdict != VerdictQuarantined {
		t.Errorf("Verdict = %q, want quarantined", g.Verdict)
	}
	if g.AppliedRun != applyRun {
		t.Errorf("AppliedRun = %d, want %d", g.AppliedRun, applyRun)
	}
	if g.AppliedAt == "" {
		t.Error("AppliedAt is empty, want the apply run's started_at")
	}
}

func TestSetOutcomeWithZeroRunIDStoresNullAppliedRun(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	if err := l.SetOutcome(ctx, "hash1+hash2", VerdictDismissed, 0); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	var appliedRun sql.NullInt64
	if err := l.DB().QueryRowContext(ctx,
		`SELECT applied_run FROM candidate_group WHERE signature = ?`, "hash1+hash2").
		Scan(&appliedRun); err != nil {
		t.Fatalf("reading applied_run: %v", err)
	}
	if appliedRun.Valid {
		t.Errorf("applied_run = %v, want NULL for a zero runID", appliedRun)
	}
}

func TestSetOutcomeFailsWhenCandidateGroupTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_group`); err != nil {
		t.Fatalf("dropping candidate_group: %v", err)
	}
	if err := l.SetOutcome(ctx, "sig", VerdictQuarantined, 1); err == nil {
		t.Error("SetOutcome should fail when candidate_group is missing")
	}
}

func TestGroupsFilterAppliedBeforeExcludesAJustAppliedGroup(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun (scan): %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	// Insert the applying run directly so its started_at is a known,
	// controllable instant rather than whatever time.Now() happens to be.
	appliedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	res, err := l.DB().ExecContext(ctx, `INSERT INTO run(started_at, kind) VALUES(?, 'apply')`,
		appliedAt.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("inserting apply run: %v", err)
	}
	applyRun, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("reading apply run id: %v", err)
	}
	if err := l.SetOutcome(ctx, "hash1+hash2", VerdictQuarantined, applyRun); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	// AppliedBefore exactly the run's own started_at excludes it: the run
	// did not start strictly before itself.
	atBoundary, err := l.Groups(ctx, GroupFilter{AppliedBefore: appliedAt})
	if err != nil {
		t.Fatalf("Groups (AppliedBefore=appliedAt): %v", err)
	}
	if len(atBoundary) != 0 {
		t.Errorf("Groups(AppliedBefore=appliedAt) = %+v, want none (just applied)", atBoundary)
	}

	// A moment after the apply run started, the group counts as old enough.
	after, err := l.Groups(ctx, GroupFilter{AppliedBefore: appliedAt.Add(time.Second)})
	if err != nil {
		t.Fatalf("Groups (AppliedBefore=after): %v", err)
	}
	if len(after) != 1 || after[0].Signature != "hash1+hash2" {
		t.Errorf("Groups(AppliedBefore=after) = %+v, want just hash1+hash2", after)
	}
}

func TestGroupsFilterAppliedBeforeExcludesAnUnappliedGroup(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	got, err := l.Groups(ctx, GroupFilter{AppliedBefore: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Groups(AppliedBefore) = %+v, want none (group was never applied)", got)
	}
}

func TestGroupsFailsWhenCandidateSceneTableIsMissingOnRead(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_scene`); err != nil {
		t.Fatalf("dropping candidate_scene: %v", err)
	}
	if _, err := l.Groups(ctx, GroupFilter{}); err == nil {
		t.Error("Groups should fail when candidate_scene is missing")
	}
}

func TestGroupsFailsWhenCandidateStepTableIsMissingOnRead(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_step`); err != nil {
		t.Fatalf("dropping candidate_step: %v", err)
	}
	if _, err := l.Groups(ctx, GroupFilter{}); err == nil {
		t.Error("Groups should fail when candidate_step is missing")
	}
}

func TestGroupsFailsWhenCandidateEvidenceTableIsMissingOnRead(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()[:1]); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_evidence`); err != nil {
		t.Fatalf("dropping candidate_evidence: %v", err)
	}
	if _, err := l.Groups(ctx, GroupFilter{}); err == nil {
		t.Error("Groups should fail when candidate_evidence is missing")
	}
}

func TestStatsQuarantinedAndReclaimedBytesExcludedFromReclaimable(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	groups := []Group{
		{Signature: "certain1", SceneIDs: []string{"1"}, Verdict: VerdictCertain, Reclaimable: 50},
		{Signature: "dismissed1", SceneIDs: []string{"4"}, Verdict: VerdictDismissed, Reclaimable: 25},
		{Signature: "quarantined1", SceneIDs: []string{"2"}, Verdict: VerdictQuarantined, Reclaimable: 100},
		{Signature: "reclaimed1", SceneIDs: []string{"3"}, Verdict: VerdictReclaimed, Reclaimable: 200},
	}
	if err := l.ReplaceGroups(ctx, runID, groups); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.GroupsDismissed != 1 {
		t.Errorf("GroupsDismissed = %d, want 1", stats.GroupsDismissed)
	}
	if stats.GroupsQuarantined != 1 || stats.QuarantinedBytes != 100 {
		t.Errorf("quarantined = %d groups, %d bytes, want 1, 100", stats.GroupsQuarantined, stats.QuarantinedBytes)
	}
	if stats.GroupsReclaimed != 1 || stats.ReclaimedBytes != 200 {
		t.Errorf("reclaimed = %d groups, %d bytes, want 1, 200", stats.GroupsReclaimed, stats.ReclaimedBytes)
	}
	// The certain and dismissed groups' bytes count as reclaimable —
	// quarantined and reclaimed groups are spoken for or already freed, not
	// pending.
	if stats.ReclaimableBytes != 75 {
		t.Errorf("ReclaimableBytes = %d, want 75 (quarantined/reclaimed excluded)", stats.ReclaimableBytes)
	}
}
