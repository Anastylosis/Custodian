package evidence

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/textmatch"
	stash "github.com/Anastylosis/stash-go"
)

// FilenameGenerator emits groups from scenes whose primary-file basenames
// parse to the same structured date, title key and performer set —
// duplicates whose only evidence is the filename convention itself.
type FilenameGenerator struct{}

// Name identifies this generator in evidence rows and logs.
func (FilenameGenerator) Name() string { return "filename" }

// Find buckets every live scene whose basename parses (stash.ParseFilename)
// by (date, textmatch.Key(title), sorted performer names), and emits one
// group per bucket with two or more scenes.
func (FilenameGenerator) Find(ctx context.Context, l *ledger.Ledger) ([]Group, error) {
	scenes, err := l.LiveSceneSignals(ctx)
	if err != nil {
		return nil, fmt.Errorf("evidence: filename: %w", err)
	}

	buckets := map[string][]ledger.SceneSignals{}
	parsed := map[string]stash.ParsedFilename{}
	for _, sc := range scenes {
		p, ok := stash.ParseFilename(sc.Basename)
		if !ok {
			continue
		}
		parsed[sc.SceneID] = p
		key := filenameKey(p)
		buckets[key] = append(buckets[key], sc)
	}

	var keys []string
	for k, bucket := range buckets {
		if len(bucket) >= 2 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	groups := make([]Group, 0, len(keys))
	for _, k := range keys {
		bucket := buckets[k]
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].SceneID < bucket[j].SceneID })
		grp, err := filenameGroup(ctx, l, bucket, parsed[bucket[0].SceneID])
		if err != nil {
			return nil, err
		}
		groups = append(groups, grp)
	}
	return groups, nil
}

// filenameKey is the bucket key: date, title key and sorted lower-cased
// performer names, so ordering and case in the filename don't split an
// otherwise-matching pair.
func filenameKey(p stash.ParsedFilename) string {
	names := make([]string, len(p.Performers))
	for i, n := range p.Performers {
		names[i] = strings.ToLower(strings.TrimSpace(n))
	}
	sort.Strings(names)
	return p.Date + "|" + textmatch.Key(p.Title) + "|" + strings.Join(names, ",")
}

func filenameGroup(ctx context.Context, l *ledger.Ledger, scenes []ledger.SceneSignals, p stash.ParsedFilename) (Group, error) {
	sceneIDs := make([]string, len(scenes))
	oshash := make(map[string]string, len(scenes))
	sizes := make([]int64, len(scenes))
	for i, sc := range scenes {
		sceneIDs[i] = sc.SceneID
		oshash[sc.SceneID] = sc.Oshash
		sizes[i] = sc.Size
	}
	total, largest := sumAndLargest(sizes)

	names := make([]string, len(p.Performers))
	copy(names, p.Performers)
	sort.Strings(names)

	evs := []Evidence{{
		Generator: "filename",
		Kind:      "filename_match",
		Detail:    fmt.Sprintf("%s %q %s", p.Date, textmatch.Key(p.Title), strings.Join(names, ", ")),
	}}
	for i := 0; i < len(scenes); i++ {
		for j := i + 1; j < len(scenes); j++ {
			evs = append(evs, durationSpreadEvidenceFor("filename", scenes[i].Duration, scenes[j].Duration))
		}
	}

	conflict, ok, err := stashIDConflictEvidence(ctx, l, "filename", sceneIDs)
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
