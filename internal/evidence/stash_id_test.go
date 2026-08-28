package evidence

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// fixtureScene builds a minimal stash.Scene: one file with the given
// duration, size and oshash (omitted when empty), carrying stashIDs.
func fixtureScene(id string, duration float64, size int64, oshash string, stashIDs ...stash.StashID) stash.Scene {
	f := stash.File{ID: "f" + id, Duration: duration, Size: size}
	if oshash != "" {
		f.Fingerprints = []stash.Fingerprint{{Type: "oshash", Value: oshash}}
	}
	return stash.Scene{ID: id, UpdatedAt: "2024-01-01 00:00:00", StashIDs: stashIDs, Files: []stash.File{f}}
}

func seedScenes(t *testing.T, l *ledger.Ledger, scenes []stash.Scene) {
	t.Helper()
	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
}

func TestStashIDGeneratorFindsSharedCluster(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	sid := stash.StashID{Endpoint: "https://sb", ID: "uuid-1"}
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "hashA", sid),
		fixtureScene("2", 102, 900, "hashB", sid),
	})

	groups, err := StashIDGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}

	g := groups[0]
	if g.Signature != "hashA+hashB" {
		t.Errorf("Signature = %q, want hashA+hashB", g.Signature)
	}
	if len(g.SceneIDs) != 2 || g.SceneIDs[0] != "1" || g.SceneIDs[1] != "2" {
		t.Errorf("SceneIDs = %v, want [1 2]", g.SceneIDs)
	}
	if g.Reclaimable != 900 {
		t.Errorf("Reclaimable = %d, want 900 (1000+900-1000)", g.Reclaimable)
	}
	if len(g.Evidence) != 2 {
		t.Fatalf("Evidence = %+v, want 2 entries", g.Evidence)
	}
	shared := g.Evidence[0]
	if shared.Kind != "shared_stash_id" || shared.Detail != "https://sb uuid-1" {
		t.Errorf("Evidence[0] = %+v, want shared_stash_id https://sb uuid-1", shared)
	}
	spread := g.Evidence[1]
	if spread.Kind != "duration_spread" || spread.Detail != "100s vs 102s" {
		t.Errorf("Evidence[1] = %+v, want duration_spread 100s vs 102s", spread)
	}
	if spread.Value == nil || *spread.Value != 2 {
		t.Errorf("Evidence[1].Value = %v, want 2", spread.Value)
	}
}

func TestStashIDGeneratorMissingOshashUsesSceneID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	sid := stash.StashID{Endpoint: "https://sb", ID: "uuid-1"}
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "hashA", sid),
		fixtureScene("2", 102, 900, "", sid), // no oshash on the primary file
	})

	groups, err := StashIDGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	// "hashA" sorts before "scene:2" lexically.
	if got, want := groups[0].Signature, "hashA+scene:2"; got != want {
		t.Errorf("Signature = %q, want %q", got, want)
	}
}

func TestStashIDGeneratorSceneBelongsToTwoClusters(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	endpointA := stash.StashID{Endpoint: "https://a", ID: "x"}
	endpointB := stash.StashID{Endpoint: "https://b", ID: "y"}
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "hash1", endpointA, endpointB),
		fixtureScene("2", 100, 1000, "hash2", endpointA),
		fixtureScene("3", 100, 1000, "hash3", endpointB),
	})

	groups, err := StashIDGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("Find = %d groups, want 2", len(groups))
	}
	for _, g := range groups {
		found := false
		for _, id := range g.SceneIDs {
			if id == "1" {
				found = true
			}
		}
		if !found {
			t.Errorf("group %q does not include scene 1: %v", g.Signature, g.SceneIDs)
		}
	}
}

func TestStashIDGeneratorIgnoresUnclusteredStashID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "hashA", stash.StashID{Endpoint: "https://sb", ID: "solo"}),
	})

	groups, err := StashIDGenerator{}.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (only one scene carries the stash_id)", len(groups))
	}
}
