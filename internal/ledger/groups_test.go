package ledger

import (
	"context"
	"database/sql"
	"testing"

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
	if files["1"].Basename != "a.mp4" || files["1"].Size != 100 {
		t.Errorf("PrimaryFiles[1] = %+v", files["1"])
	}
	if files["2"].Basename != "b.mp4" || files["2"].Size != 50 {
		t.Errorf("PrimaryFiles[2] = %+v", files["2"])
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
