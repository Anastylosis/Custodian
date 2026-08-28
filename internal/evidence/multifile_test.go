package evidence

import (
	"context"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func TestMultifileGeneratorFindsAScenesWithTwoFiles(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		{
			ID: "1", UpdatedAt: "2024-01-01",
			Files: []stash.File{
				{ID: "f1", Basename: "junk1.mp4", Path: "/data/tmp/junk1.mp4", Size: 100,
					Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "h1"}}},
				{ID: "f2", Basename: "2024-01-01_Alice-Some.Title_1080p.mp4", Path: "/lib/alice/2024-01-01_Alice-Some.Title_1080p.mp4", Size: 90},
			},
		},
	})

	groups, err := MultifileGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.SceneIDs) != 1 || g.SceneIDs[0] != "1" {
		t.Errorf("SceneIDs = %v, want [1]", g.SceneIDs)
	}
	if g.Signature != "h1" {
		t.Errorf("Signature = %q, want h1 (the primary file's oshash)", g.Signature)
	}
	if len(g.Evidence) != 1 || g.Evidence[0].Kind != "multifile" {
		t.Fatalf("Evidence = %+v, want one multifile entry", g.Evidence)
	}
	if g.Evidence[0].Value == nil || *g.Evidence[0].Value != 2 {
		t.Errorf("Evidence[0].Value = %v, want 2", g.Evidence[0].Value)
	}
	// f2 is filed (not under /data/tmp) and f1 is staging, so f2 wins.
	if g.Reclaimable != 100 {
		t.Errorf("Reclaimable = %d, want 100 (f1's size, the loser)", g.Reclaimable)
	}
	if len(g.Steps) != 1 || g.Steps[0].Rule != "file_choice" || g.Steps[0].Detail != "f2" || g.Steps[0].Outcome != "filed" {
		t.Errorf("Steps = %+v, want file_choice keeping f2 because it's filed", g.Steps)
	}
}

func TestMultifileGeneratorPrefersStructuredBasenameWhenBothStaging(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		{
			ID: "1", UpdatedAt: "2024-01-01",
			Files: []stash.File{
				{ID: "f1", Basename: "junk.mp4", Path: "/data/tmp/junk.mp4", Size: 100},
				{ID: "f2", Basename: "2024-01-01_Alice-Some.Title_1080p.mp4", Path: "/data/tmp/2024-01-01_Alice-Some.Title_1080p.mp4", Size: 90},
			},
		},
	})

	groups, err := MultifileGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	steps := groups[0].Steps
	if len(steps) != 1 || steps[0].Detail != "f2" || steps[0].Outcome != "structured basename" {
		t.Errorf("Steps = %+v, want file_choice keeping f2 for its structured basename", steps)
	}
}

func TestMultifileGeneratorFallsBackToLowestFileID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		{
			ID: "1", UpdatedAt: "2024-01-01",
			Files: []stash.File{
				{ID: "f2", Basename: "b.mp4", Path: "/data/tmp/b.mp4", Size: 100},
				{ID: "f1", Basename: "a.mp4", Path: "/data/tmp/a.mp4", Size: 90},
			},
		},
	})

	groups, err := MultifileGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	steps := groups[0].Steps
	if len(steps) != 1 || steps[0].Detail != "f1" || steps[0].Outcome != "tie: lowest file id" {
		t.Errorf("Steps = %+v, want file_choice keeping f1 (lowest id)", steps)
	}
}

func TestMultifileGeneratorIgnoresSingleFileScenes(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{fixtureScene("1", 100, 1000, "h1")})

	groups, err := MultifileGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0", len(groups))
	}
}

func TestMultifileGeneratorVerdictIsAlwaysCertain(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		{
			ID: "1", UpdatedAt: "2024-01-01",
			Files: []stash.File{
				{ID: "f1", Basename: "a.mp4", Path: "/lib/a.mp4", Size: 100},
				{ID: "f2", Basename: "a.dup.mp4", Path: "/lib/a.dup.mp4", Size: 90},
			},
		},
	})

	summary, err := Run(ctx, l, []Generator{MultifileGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Certain != 1 {
		t.Errorf("Certain = %d, want 1", summary.Certain)
	}
}
