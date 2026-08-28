package evidence

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Anastylosis/Custodian/internal/keeper"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// MultifileGenerator emits one group per live scene holding more than one
// live file — Stash attaches a re-detected file to an existing scene by
// oshash/md5, so the files are already byte-equivalent (REBUILD.md §1.5,
// "Known Stash behaviours"; workflow B). Its groups are single-scene: not a
// cross-scene duplicate, just cleanup within one scene.
//
// Applying the file_choice step (setting the primary file and deleting the
// others) is phase 5's job; this generator only records the choice.
type MultifileGenerator struct{}

// Name identifies this generator in evidence rows and logs.
func (MultifileGenerator) Name() string { return "multifile" }

// Find groups every live scene's files against each other, one group per
// scene.
func (MultifileGenerator) Find(ctx context.Context, l *ledger.Ledger) ([]Group, error) {
	bySceneID, err := l.MultiFileScenes(ctx)
	if err != nil {
		return nil, fmt.Errorf("evidence: multifile: %w", err)
	}
	if len(bySceneID) == 0 {
		return nil, nil
	}

	sceneIDs := make([]string, 0, len(bySceneID))
	for id := range bySceneID {
		sceneIDs = append(sceneIDs, id)
	}
	sort.Strings(sceneIDs)

	primaries, err := l.PrimaryFiles(ctx, sceneIDs)
	if err != nil {
		return nil, fmt.Errorf("evidence: multifile: %w", err)
	}

	groups := make([]Group, 0, len(sceneIDs))
	for _, id := range sceneIDs {
		groups = append(groups, multifileGroup(id, bySceneID[id], primaries[id]))
	}
	return groups, nil
}

// multifileGroup builds one scene's group: evidence naming its files,
// Reclaimable as the sum of every file but the winner, and a file_choice
// Step recording which file survives.
func multifileGroup(sceneID string, files []ledger.SceneFile, primary ledger.PrimaryFile) Group {
	winner, reason := chooseMultifileWinner(files)

	paths := make([]string, len(files))
	sizes := make([]int64, len(files))
	for i, f := range files {
		paths[i] = f.Path
		sizes[i] = f.Size
	}
	total, _ := sumAndLargest(sizes)
	reclaimable := total - winner.Size

	return Group{
		Signature: groupSignature([]string{sceneID}, map[string]string{sceneID: primary.Oshash}),
		SceneIDs:  []string{sceneID},
		Evidence: []Evidence{{
			Generator: "multifile",
			Kind:      "multifile",
			Detail:    strings.Join(paths, "; "),
			Value:     floatPtr(float64(len(files))),
		}},
		Steps: []Step{{
			Rule:    "file_choice",
			Outcome: reason,
			Detail:  winner.ID,
		}},
		Reclaimable: reclaimable,
	}
}

// chooseMultifileWinner picks the file to keep: filed (non-staging) beats
// staging; among files tied on that, a structured basename
// (stash.ParseFilename ok) beats one that doesn't parse; the remaining tie
// falls to the lowest file id, for a deterministic pick.
func chooseMultifileWinner(files []ledger.SceneFile) (ledger.SceneFile, string) {
	filed := keeper.DefaultRules().Filed
	best := files[0]
	for _, f := range files[1:] {
		if betterMultifileFile(f, best, filed) {
			best = f
		}
	}
	return best, multifileWinReason(best, filed)
}

// betterMultifileFile reports whether a should be preferred over b.
func betterMultifileFile(a, b ledger.SceneFile, filed func(string) bool) bool {
	fa, fb := filed(a.Path), filed(b.Path)
	if fa != fb {
		return fa
	}
	_, pa := stash.ParseFilename(a.Basename)
	_, pb := stash.ParseFilename(b.Basename)
	if pa != pb {
		return pa
	}
	return a.ID < b.ID
}

func multifileWinReason(f ledger.SceneFile, filed func(string) bool) string {
	if filed(f.Path) {
		return "filed"
	}
	if _, ok := stash.ParseFilename(f.Basename); ok {
		return "structured basename"
	}
	return "tie: lowest file id"
}
