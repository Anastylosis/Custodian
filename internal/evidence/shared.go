package evidence

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// groupSignature is the signature rule every generator shares: sorted
// primary-file oshashes, "scene:<id>" when a scene's primary file carries
// none, joined by "+". Two generators that find the same scenes always
// produce the same signature, so Run's merge-by-signature step ties their
// evidence together instead of duplicating the group.
func groupSignature(sceneIDs []string, oshash map[string]string) string {
	tokens := make([]string, len(sceneIDs))
	for i, id := range sceneIDs {
		if h := oshash[id]; h != "" {
			tokens[i] = h
		} else {
			tokens[i] = "scene:" + id
		}
	}
	sort.Strings(tokens)
	return strings.Join(tokens, "+")
}

// durationSpreadEvidenceFor is durationSpreadEvidence generalized to any
// generator name.
func durationSpreadEvidenceFor(generator string, a, b float64) Evidence {
	spread := a - b
	if spread < 0 {
		spread = -spread
	}
	return Evidence{
		Generator: generator,
		Kind:      "duration_spread",
		Value:     &spread,
		Detail:    fmt.Sprintf("%gs vs %gs", a, b),
	}
}

// stashIDConflictEvidence checks whether the scenes in a group already carry
// different stash_ids for the same endpoint — a candidate stash-box
// mis-tag (REBUILD.md §4.2). It does NOT detect a shared stash_id: that
// case is the stash_id generator's own group, which Run's merge-by-signature
// step ties to this one automatically.
func stashIDConflictEvidence(ctx context.Context, l *ledger.Ledger, generator string, sceneIDs []string) (Evidence, bool, error) {
	byScene, err := l.SceneStashIDs(ctx, sceneIDs)
	if err != nil {
		return Evidence{}, false, fmt.Errorf("evidence: %s: checking stash_id conflicts: %w", generator, err)
	}

	perEndpoint := map[string]map[string]bool{}
	var endpoints []string
	for _, refs := range byScene {
		for _, ref := range refs {
			if perEndpoint[ref.Endpoint] == nil {
				perEndpoint[ref.Endpoint] = map[string]bool{}
				endpoints = append(endpoints, ref.Endpoint)
			}
			perEndpoint[ref.Endpoint][ref.StashID] = true
		}
	}
	sort.Strings(endpoints)

	for _, endpoint := range endpoints {
		ids := perEndpoint[endpoint]
		if len(ids) < 2 {
			continue
		}
		sorted := make([]string, 0, len(ids))
		for id := range ids {
			sorted = append(sorted, id)
		}
		sort.Strings(sorted)
		return Evidence{
			Generator: generator,
			Kind:      "stash_id_conflict",
			Detail:    fmt.Sprintf("%s %s", endpoint, strings.Join(sorted, " vs ")),
		}, true, nil
	}
	return Evidence{}, false, nil
}

// sumAndLargest totals sizes and returns the largest — the Reclaimable
// upper bound shared by the cross-scene generators (total minus the copy
// that would be kept).
func sumAndLargest(sizes []int64) (total, largest int64) {
	for _, s := range sizes {
		total += s
		if s > largest {
			largest = s
		}
	}
	return total, largest
}

func floatPtr(v float64) *float64 { return &v }
