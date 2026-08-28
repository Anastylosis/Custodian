package evidence

import (
	"context"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func titledScene(id, title, date, studioID string, duration float64, size int64, oshash string) stash.Scene {
	sc := stash.Scene{
		ID: id, Title: title, Date: date, UpdatedAt: "2024-01-01 00:00:00",
		Files: []stash.File{{
			ID: "f" + id, Duration: duration, Size: size,
			Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: oshash}},
		}},
	}
	if studioID != "" {
		sc.Studio = &stash.Studio{ID: studioID, Name: studioID}
	}
	return sc
}

func TestTitleDateGeneratorGroupsSameTitleAndDate(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 105, 900, "h2"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
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
	found := false
	for _, e := range g.Evidence {
		if e.Kind == "title_date" {
			found = true
		}
	}
	if !found {
		t.Error("no title_date evidence")
	}
}

func TestTitleDateGeneratorRequiresBothTitleAndDate(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "", "2024-01-01", "", 100, 900, "h2"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (no title)", len(groups))
	}
}

func TestTitleDateGeneratorDifferentStudiosNotGrouped(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "studioA", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "studioB", 100, 900, "h2"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (different studios)", len(groups))
	}
}

func TestTitleDateGeneratorMissingStudioIsAWildcard(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "studioA", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 100, 900, "h2"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("Find = %d groups, want 1 (a scene with no studio doesn't veto the match)", len(groups))
	}
}

// A collision beyond textmatch.CollisionSpread (120s) is two different
// scenes that happen to share a title and date, not a group at all.
func TestTitleDateGeneratorCollisionBeyondSpreadNotGrouped(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 100+121, 900, "h2"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (durations differ by more than 120s)", len(groups))
	}
}

func TestTitleDateGeneratorCollisionWithinSpreadIsGrouped(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 100+119, 900, "h2"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("Find = %d groups, want 1 (119s spread is within CollisionSpread)", len(groups))
	}
}

func TestTitleDateGeneratorSplitsIntoComponents(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	// Three scenes share a title+date; 1 and 2 are close in duration, 3 is
	// far from both, so 3 must form its own (dropped, since it's alone)
	// component rather than joining 1/2.
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 105, 900, "h2"),
		titledScene("3", "A Great Scene", "2024-01-01", "", 1000, 500, "h3"),
	})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	if len(groups[0].SceneIDs) != 2 {
		t.Errorf("SceneIDs = %v, want just scenes 1 and 2", groups[0].SceneIDs)
	}
}

func TestTitleDateGeneratorSkipsScenesAlreadySharingAStashID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	sid := stash.StashID{Endpoint: "https://sb", ID: "uuid-1"}
	a := titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1")
	a.StashIDs = []stash.StashID{sid}
	b := titledScene("2", "A Great Scene", "2024-01-01", "", 105, 900, "h2")
	b.StashIDs = []stash.StashID{sid}
	seedScenes(t, l, []stash.Scene{a, b})

	groups, err := TitleDateGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (already linked by the shared stash_id)", len(groups))
	}
}

func TestTitleDateGeneratorProbableWhenDurationsAgree(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 100.5, 900, "h2"),
	})

	summary, err := Run(ctx, l, []Generator{TitleDateGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Probable != 1 {
		t.Errorf("Probable = %d, want 1", summary.Probable)
	}
}

func TestTitleDateGeneratorConflictedWhenDurationsDisagreeWithinCollisionSpread(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	// 60s spread: within CollisionSpread (120s, still a "group"), but well
	// beyond DefaultTolerance (min(2%, 120s) of ~100s), so conflicted.
	seedScenes(t, l, []stash.Scene{
		titledScene("1", "A Great Scene", "2024-01-01", "", 100, 1000, "h1"),
		titledScene("2", "A Great Scene", "2024-01-01", "", 160, 900, "h2"),
	})

	summary, err := Run(ctx, l, []Generator{TitleDateGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Conflicted != 1 {
		t.Errorf("Conflicted = %d, want 1", summary.Conflicted)
	}
}
