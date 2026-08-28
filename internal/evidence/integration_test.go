package evidence

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
	stash "github.com/Anastylosis/stash-go"
)

// Integration test in this file talks to a real Stash instance and only runs
// when SFX_INT_STASH_URL is set in the environment. Without that env var it
// is skipped, so `go test ./...` stays hermetic. SFX_INT_STASH_SEEDED=1 turns
// a missing fixture into a failure instead of a skip — see
// internal/mirror/integration_test.go for the full explanation; this copies
// the same tiny gate since the two aren't in the same package.
func integrationClient(t *testing.T) *stash.Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return stash.NewClient(url)
}

func requireSeeded(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if ok {
		return
	}
	if os.Getenv("SFX_INT_STASH_SEEDED") == "1" {
		t.Fatalf("SFX_INT_STASH_SEEDED=1 but "+format, args...)
	}
	t.Skipf(format, args...)
}

// TestIntegrationStashIDEvidence syncs the seeded fixture library, then runs
// the stash_id generator under the default policy. The fixture's
// phash-duplicate pair shares a fabricated stashdb.org stash_id and both
// clips are 2s, so this should yield exactly one certain group.
func TestIntegrationStashIDEvidence(t *testing.T) {
	client := integrationClient(t)
	l := newTestLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	summary, err := Run(ctx, l, []Generator{StashIDGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	groups, err := l.Groups(ctx, ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	requireSeeded(t, len(groups) == 1,
		"candidate groups = %d, want 1 (the phash-duplicate pair shares a stash_id)", len(groups))

	g := groups[0]
	if g.Verdict != Certain {
		t.Errorf("Verdict = %q, want certain (both fixture clips are 2s)", g.Verdict)
	}
	if len(g.SceneIDs) != 2 {
		t.Errorf("SceneIDs = %v, want 2 scene ids", g.SceneIDs)
	}

	var sharedFound bool
	for _, e := range g.Evidence {
		if e.Kind == "shared_stash_id" {
			sharedFound = true
			if !strings.Contains(e.Detail, "https://stashdb.org/graphql") {
				t.Errorf("shared_stash_id Detail = %q, want it to name the endpoint", e.Detail)
			}
		}
	}
	if !sharedFound {
		t.Error("no shared_stash_id evidence on the group")
	}

	if g.Reclaimable <= 0 {
		t.Errorf("Reclaimable = %d, want > 0", g.Reclaimable)
	}
	if summary.Certain < 1 {
		t.Errorf("summary.Certain = %d, want >= 1", summary.Certain)
	}
}

// TestIntegrationPhashEvidence exercises the one generator that reads the
// live server rather than the ledger. distance 0 keeps the response to
// byte-equivalent perceptual hashes, which is what the fixture library's
// duplicate pair (an mp4 and its remux) is; anything wrong with the
// findDuplicateScenes call, its unpaginated stream decode, or the mapping
// of the returned scenes back onto ledger rows shows up as no group at all.
func TestIntegrationPhashEvidence(t *testing.T) {
	client := integrationClient(t)
	l := newTestLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	gen := PhashGenerator{Client: client, Distance: 0, DurationDiff: 1.0}
	if _, err := Run(ctx, l, []Generator{gen}, Policy{Tolerance: DefaultTolerance}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	groups, err := l.Groups(ctx, ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	requireSeeded(t, len(groups) >= 1,
		"phash at distance 0 found no group; the fixture library holds an mp4 and its remux")

	for _, g := range groups {
		if len(g.SceneIDs) < 2 {
			t.Errorf("group %s has %v; a duplicate group needs at least two scenes", g.Signature, g.SceneIDs)
		}
		var kinds []string
		for _, e := range g.Evidence {
			kinds = append(kinds, e.Kind)
		}
		if !slices.Contains(kinds, "phash_group") {
			t.Errorf("group %s carries no phash_group evidence, only %v", g.Signature, kinds)
		}
		if g.Reclaimable <= 0 {
			t.Errorf("group %s: Reclaimable = %d, want > 0", g.Signature, g.Reclaimable)
		}
	}
}
