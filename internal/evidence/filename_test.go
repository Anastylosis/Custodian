package evidence

import (
	"context"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func structuredScene(id, basename string, duration float64, size int64, oshash string) stash.Scene {
	return stash.Scene{
		ID: id, UpdatedAt: "2024-01-01 00:00:00",
		Files: []stash.File{{
			ID: "f" + id, Basename: basename, Path: "/lib/" + basename, Duration: duration, Size: size,
			Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: oshash}},
		}},
	}
}

func TestFilenameGeneratorGroupsMatchingBasenames(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		structuredScene("1", "2024-01-01_Alice_Bob-Some.Title_1080p.mp4", 100, 1000, "h1"),
		structuredScene("2", "2024-01-01_Alice_Bob-Some.Title_720p.mp4", 100.5, 900, "h2"),
	})

	groups, err := FilenameGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	g := groups[0]
	if g.Signature != "h1+h2" {
		t.Errorf("Signature = %q, want h1+h2", g.Signature)
	}
	if len(g.SceneIDs) != 2 {
		t.Errorf("SceneIDs = %v, want 2", g.SceneIDs)
	}
	found := false
	for _, e := range g.Evidence {
		if e.Kind == "filename_match" {
			found = true
		}
	}
	if !found {
		t.Error("no filename_match evidence")
	}
	if g.Reclaimable != 900 {
		t.Errorf("Reclaimable = %d, want 900", g.Reclaimable)
	}
}

func TestFilenameGeneratorIgnoresUnparsableBasenames(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "h1"),
		fixtureScene("2", 100, 900, "h2"),
	})

	groups, err := FilenameGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (neither basename is structured)", len(groups))
	}
}

// A different performer set on an otherwise-identical filename must not
// group — the performer set is part of the match key.
func TestFilenameGeneratorPerformerSetMismatchNotGrouped(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		structuredScene("1", "2024-01-01_Alice-Some.Title_1080p.mp4", 100, 1000, "h1"),
		structuredScene("2", "2024-01-01_Carol-Some.Title_720p.mp4", 100.5, 900, "h2"),
	})

	groups, err := FilenameGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (different performers)", len(groups))
	}
}

func TestFilenameGeneratorPerformerOrderDoesNotMatter(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		structuredScene("1", "2024-01-01_Alice_Bob-Some.Title_1080p.mp4", 100, 1000, "h1"),
		structuredScene("2", "2024-01-01_Bob_Alice-Some.Title_720p.mp4", 100.5, 900, "h2"),
	})

	groups, err := FilenameGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("Find = %d groups, want 1 (performer order shouldn't matter)", len(groups))
	}
}

func TestFilenameGeneratorRejectedOnConflictingStashID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	a := structuredScene("1", "2024-01-01_Alice-Some.Title_1080p.mp4", 100, 1000, "h1")
	a.StashIDs = []stash.StashID{{Endpoint: "https://sb", ID: "aaa"}}
	b := structuredScene("2", "2024-01-01_Alice-Some.Title_720p.mp4", 100.5, 900, "h2")
	b.StashIDs = []stash.StashID{{Endpoint: "https://sb", ID: "bbb"}}
	seedScenes(t, l, []stash.Scene{a, b})

	groups, err := FilenameGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	g := groups[0]
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Rejected {
		t.Errorf("Verdict = %q, want rejected", g.Verdict)
	}
}

func TestFilenameGeneratorProbableWhenDurationsAgree(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		structuredScene("1", "2024-01-01_Alice-Some.Title_1080p.mp4", 100, 1000, "h1"),
		structuredScene("2", "2024-01-01_Alice-Some.Title_720p.mp4", 100.5, 900, "h2"),
	})

	summary, err := Run(ctx, l, []Generator{FilenameGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Probable != 1 {
		t.Errorf("Probable = %d, want 1", summary.Probable)
	}
}
