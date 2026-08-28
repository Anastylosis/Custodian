package evidence

import (
	"context"
	"fmt"
	"sort"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/textmatch"
)

// TitleDateGenerator emits groups from scenes sharing a title key and date
// (and, when both carry one, the same studio) that don't already share a
// stash_id — the metadata-only signal for the uncatalogued half of the
// library, where phash may be the only other option.
type TitleDateGenerator struct{}

// Name identifies this generator in evidence rows and logs.
func (TitleDateGenerator) Name() string { return "title_date" }

// Find buckets live scenes by (textmatch.Key(title), date), splits each
// bucket into connected components where every pair agrees on studio
// (or either lacks one) and on duration within textmatch.CollisionSpread —
// a title collision beyond that spread is two different scenes, not a
// group at all — and drops any component whose scenes already all share a
// stash_id (the stash_id generator's own signal).
func (TitleDateGenerator) Find(ctx context.Context, l *ledger.Ledger) ([]Group, error) {
	scenes, err := l.LiveSceneSignals(ctx)
	if err != nil {
		return nil, fmt.Errorf("evidence: title_date: %w", err)
	}

	buckets := map[string][]ledger.SceneSignals{}
	var keys []string
	for _, sc := range scenes {
		if sc.Title == "" || sc.Date == "" {
			continue
		}
		key := textmatch.Key(sc.Title) + "|" + sc.Date
		if _, ok := buckets[key]; !ok {
			keys = append(keys, key)
		}
		buckets[key] = append(buckets[key], sc)
	}
	sort.Strings(keys)

	var groups []Group
	for _, key := range keys {
		bucket := buckets[key]
		if len(bucket) < 2 {
			continue
		}
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].SceneID < bucket[j].SceneID })

		for _, comp := range titleDateComponents(bucket) {
			sceneIDs := sceneIDsOf(comp)
			already, err := allShareAStashID(ctx, l, sceneIDs)
			if err != nil {
				return nil, err
			}
			if already {
				continue
			}
			groups = append(groups, titleDateGroup(comp))
		}
	}
	return groups, nil
}

// titleDateComponents splits a same-title-key/date bucket into connected
// components where every pair agrees on studio and on duration within
// textmatch.CollisionSpread. Components of one scene are dropped.
func titleDateComponents(bucket []ledger.SceneSignals) [][]ledger.SceneSignals {
	n := len(bucket)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if studioCompatible(bucket[i], bucket[j]) &&
				textmatch.DurationsAgree(bucket[i].Duration, bucket[j].Duration, textmatch.CollisionSpread) {
				union(i, j)
			}
		}
	}

	byRoot := map[int][]ledger.SceneSignals{}
	for i, sc := range bucket {
		r := find(i)
		byRoot[r] = append(byRoot[r], sc)
	}

	var roots []int
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Ints(roots)

	var out [][]ledger.SceneSignals
	for _, r := range roots {
		if len(byRoot[r]) >= 2 {
			out = append(out, byRoot[r])
		}
	}
	return out
}

// studioCompatible reports whether a and b can belong to the same
// title_date group on studio grounds: equal, or either lacks one.
func studioCompatible(a, b ledger.SceneSignals) bool {
	return a.StudioID == "" || b.StudioID == "" || a.StudioID == b.StudioID
}

func sceneIDsOf(scenes []ledger.SceneSignals) []string {
	ids := make([]string, len(scenes))
	for i, sc := range scenes {
		ids[i] = sc.SceneID
	}
	return ids
}

// allShareAStashID reports whether every scene in sceneIDs already carries
// a common (endpoint, stash_id) — the stash_id generator's own cluster, in
// which case title_date's grouping is redundant.
func allShareAStashID(ctx context.Context, l *ledger.Ledger, sceneIDs []string) (bool, error) {
	byScene, err := l.SceneStashIDs(ctx, sceneIDs)
	if err != nil {
		return false, fmt.Errorf("evidence: title_date: checking existing stash_id links: %w", err)
	}
	if len(sceneIDs) < 2 {
		return false, nil
	}

	common := map[string]bool{}
	for _, ref := range byScene[sceneIDs[0]] {
		common[ref.Endpoint+"|"+ref.StashID] = true
	}
	for _, id := range sceneIDs[1:] {
		next := map[string]bool{}
		for _, ref := range byScene[id] {
			token := ref.Endpoint + "|" + ref.StashID
			if common[token] {
				next[token] = true
			}
		}
		common = next
		if len(common) == 0 {
			return false, nil
		}
	}
	return len(common) > 0, nil
}

func titleDateGroup(scenes []ledger.SceneSignals) Group {
	sceneIDs := make([]string, len(scenes))
	oshash := make(map[string]string, len(scenes))
	sizes := make([]int64, len(scenes))
	for i, sc := range scenes {
		sceneIDs[i] = sc.SceneID
		oshash[sc.SceneID] = sc.Oshash
		sizes[i] = sc.Size
	}
	total, largest := sumAndLargest(sizes)

	evs := []Evidence{{
		Generator: "title_date",
		Kind:      "title_date",
		Detail:    fmt.Sprintf("%s / %s", textmatch.Key(scenes[0].Title), scenes[0].Date),
	}}
	for i := 0; i < len(scenes); i++ {
		for j := i + 1; j < len(scenes); j++ {
			evs = append(evs, durationSpreadEvidenceFor("title_date", scenes[i].Duration, scenes[j].Duration))
		}
	}

	return Group{
		Signature:   groupSignature(sceneIDs, oshash),
		SceneIDs:    sceneIDs,
		Evidence:    evs,
		Reclaimable: total - largest,
	}
}
