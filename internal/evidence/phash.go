package evidence

import (
	"context"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// PhashGenerator emits groups from Stash's own findDuplicateScenes — the one
// generator that reads the live server rather than only the ledger.
// Read-only: it never writes anything to Stash.
type PhashGenerator struct {
	Client       *stash.Client
	Distance     int
	DurationDiff float64
}

// Name identifies this generator in evidence rows and logs.
func (PhashGenerator) Name() string { return "phash" }

// Find calls findDuplicateScenes and maps each returned scene back to the
// ledger's own record of it, for a size/duration/oshash consistent with
// every other generator. A scene findDuplicateScenes names but the ledger
// no longer carries live is dropped; a cluster left with fewer than two
// live scenes is dropped entirely.
func (g PhashGenerator) Find(ctx context.Context, l *ledger.Ledger) ([]Group, error) {
	if g.Client == nil {
		return nil, nil
	}

	found, err := g.Client.FindDuplicateScenes(ctx, g.Distance, g.DurationDiff)
	if err != nil {
		return nil, fmt.Errorf("evidence: phash: %w", err)
	}

	var groups []Group
	for _, cluster := range found {
		if len(cluster) < 2 {
			continue
		}
		sceneIDs := make([]string, len(cluster))
		for i, sc := range cluster {
			sceneIDs[i] = sc.ID
		}

		primaries, err := l.PrimaryFiles(ctx, sceneIDs)
		if err != nil {
			return nil, fmt.Errorf("evidence: phash: %w", err)
		}
		live := make([]string, 0, len(sceneIDs))
		for _, id := range sceneIDs {
			if _, ok := primaries[id]; ok {
				live = append(live, id)
			}
		}
		if len(live) < 2 {
			continue
		}

		grp, err := phashGroup(ctx, l, g, live, primaries)
		if err != nil {
			return nil, err
		}
		groups = append(groups, grp)
	}
	return groups, nil
}

func phashGroup(ctx context.Context, l *ledger.Ledger, g PhashGenerator, sceneIDs []string, primaries map[string]ledger.PrimaryFile) (Group, error) {
	oshash := make(map[string]string, len(sceneIDs))
	sizes := make([]int64, len(sceneIDs))
	for i, id := range sceneIDs {
		oshash[id] = primaries[id].Oshash
		sizes[i] = primaries[id].Size
	}
	total, largest := sumAndLargest(sizes)

	evs := []Evidence{{
		Generator: "phash",
		Kind:      "phash_group",
		Detail:    fmt.Sprintf("distance %d, duration_diff %gs", g.Distance, g.DurationDiff),
	}}
	for i := 0; i < len(sceneIDs); i++ {
		for j := i + 1; j < len(sceneIDs); j++ {
			evs = append(evs, durationSpreadEvidenceFor("phash", primaries[sceneIDs[i]].Duration, primaries[sceneIDs[j]].Duration))
		}
	}

	conflict, ok, err := stashIDConflictEvidence(ctx, l, "phash", sceneIDs)
	if err != nil {
		return Group{}, err
	}
	if ok {
		evs = append(evs, conflict)
	}

	return Group{
		Signature:   groupSignature(sceneIDs, oshash),
		SceneIDs:    sceneIDs,
		Evidence:    evs,
		Reclaimable: total - largest,
	}, nil
}
