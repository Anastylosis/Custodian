package evidence

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// StashIDGenerator emits groups from stash-box entries shared by more than
// one live scene — near-proof of identity (REBUILD.md §4.2).
type StashIDGenerator struct{}

// Name identifies this generator in evidence rows and logs.
func (StashIDGenerator) Name() string { return "stash_id" }

// Find groups live scenes sharing one (endpoint, stash_id) pair.
func (StashIDGenerator) Find(ctx context.Context, l *ledger.Ledger) ([]Group, error) {
	clusters, err := l.SharedStashIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("evidence: stash_id: %w", err)
	}

	groups := make([]Group, 0, len(clusters))
	for _, c := range clusters {
		groups = append(groups, stashIDGroup(c))
	}
	return groups, nil
}

// stashIDGroup builds a candidate group from one cluster: the signature is
// the cluster's scenes' primary-file oshashes, sorted and joined by "+" (a
// scene without one contributes "scene:<id>" instead), stable across Stash
// scene-id churn. Reclaimable is every scene's primary-file size minus the
// largest — phase 2 does not choose a keeper, so this is an upper bound.
func stashIDGroup(c ledger.StashIDCluster) Group {
	sceneIDs := make([]string, len(c.Scenes))
	tokens := make([]string, len(c.Scenes))
	var total, largest int64
	for i, sc := range c.Scenes {
		sceneIDs[i] = sc.SceneID
		if sc.Oshash != "" {
			tokens[i] = sc.Oshash
		} else {
			tokens[i] = "scene:" + sc.SceneID
		}
		total += sc.Size
		if sc.Size > largest {
			largest = sc.Size
		}
	}
	sort.Strings(tokens)

	evs := []Evidence{{
		Generator: "stash_id",
		Kind:      "shared_stash_id",
		Detail:    fmt.Sprintf("%s %s", c.Endpoint, c.StashID),
	}}
	for i := 0; i < len(c.Scenes); i++ {
		for j := i + 1; j < len(c.Scenes); j++ {
			evs = append(evs, durationSpreadEvidence(c.Scenes[i].Duration, c.Scenes[j].Duration))
		}
	}

	return Group{
		Signature:   strings.Join(tokens, "+"),
		SceneIDs:    sceneIDs,
		Evidence:    evs,
		Reclaimable: total - largest,
	}
}

func durationSpreadEvidence(a, b float64) Evidence {
	spread := a - b
	if spread < 0 {
		spread = -spread
	}
	return Evidence{
		Generator: "stash_id",
		Kind:      "duration_spread",
		Value:     &spread,
		Detail:    fmt.Sprintf("%gs vs %gs", a, b),
	}
}
