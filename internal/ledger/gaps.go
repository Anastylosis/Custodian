package ledger

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// PhashGapFile is one live file with no phash: enough to hand its scene id
// to Stash's generate task and to print its path in a dry run.
type PhashGapFile struct {
	SceneID string `json:"scene_id"`
	Path    string `json:"path"`
}

// DirTally is one directory's share of the phash gap: how many of its live
// files carry no phash, out of how many live there at all, and those
// files' total bytes.
type DirTally struct {
	Dir   string `json:"dir"`
	Gap   int    `json:"gap"`
	Live  int    `json:"live"`
	Bytes int64  `json:"bytes"`
}

// PhashGap is the ledger's phash-gap tally (REBUILD.md §7 phase 0): live
// files that are healthy but were never sent through Stash's generate task.
type PhashGap struct {
	Count       int        `json:"count"`
	Bytes       int64      `json:"bytes"`
	WithStashID int        `json:"with_stash_id"`
	Dirs        []DirTally `json:"dirs"`
}

// phashRow is one live file, as both PhashGap and PhashGapScenes need it.
type phashRow struct {
	path       string
	dir        string
	size       int64
	noPhash    bool
	sceneID    string
	hasStashID bool
}

// phashRows loads every live file once, so PhashGap's per-directory tally
// and PhashGapScenes' scene list never disagree about what "live" means.
func (l *Ledger) phashRows(ctx context.Context) ([]phashRow, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT f.path, f.basename, f.size, f.phash IS NULL, f.scene_id,
		       EXISTS(SELECT 1 FROM scene_stash_id ssi WHERE ssi.scene_id = f.scene_id)
		FROM file f
		WHERE f.gone_run IS NULL
		ORDER BY f.path`)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying live files for the phash gap: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []phashRow
	for rows.Next() {
		var r phashRow
		var basename string
		if err := rows.Scan(&r.path, &basename, &r.size, &r.noPhash, &r.sceneID, &r.hasStashID); err != nil {
			return nil, fmt.Errorf("ledger: scanning live file row: %w", err)
		}
		r.dir = dirOf(r.path, basename)
		out = append(out, r)
	}
	return out, rows.Err()
}

// dirOf returns path's containing directory by trimming basename off its
// end, rather than searching for a separator: Stash always reports paths as
// POSIX (pathmap's doc comment), so this is exact wherever basename matches.
func dirOf(path, basename string) string {
	if basename == "" || !strings.HasSuffix(path, basename) {
		return path
	}
	return strings.TrimSuffix(strings.TrimSuffix(path, basename), "/")
}

// PhashGap computes the aggregate phash gap across the live library: how
// many files, how many bytes, how many belong to a scene that already
// carries a stash_id, and how the gap concentrates by directory.
func (l *Ledger) PhashGap(ctx context.Context) (PhashGap, error) {
	rows, err := l.phashRows(ctx)
	if err != nil {
		return PhashGap{}, err
	}

	var gap PhashGap
	byDir := map[string]*DirTally{}
	for _, r := range rows {
		t, ok := byDir[r.dir]
		if !ok {
			t = &DirTally{Dir: r.dir}
			byDir[r.dir] = t
		}
		t.Live++
		if !r.noPhash {
			continue
		}
		gap.Count++
		gap.Bytes += r.size
		t.Gap++
		t.Bytes += r.size
		if r.hasStashID {
			gap.WithStashID++
		}
	}

	for _, t := range byDir {
		if t.Gap > 0 {
			gap.Dirs = append(gap.Dirs, *t)
		}
	}
	sort.Slice(gap.Dirs, func(i, j int) bool {
		if gap.Dirs[i].Gap != gap.Dirs[j].Gap {
			return gap.Dirs[i].Gap > gap.Dirs[j].Gap
		}
		return gap.Dirs[i].Dir < gap.Dirs[j].Dir
	})
	return gap, nil
}

// PhashGapScenes returns the scene id and path of every live file with no
// phash, in path order — what `doctor --generate-phashes` hands to
// MetadataGenerate and what its dry run prints.
func (l *Ledger) PhashGapScenes(ctx context.Context) ([]PhashGapFile, error) {
	rows, err := l.phashRows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PhashGapFile, 0, len(rows))
	for _, r := range rows {
		if r.noPhash {
			out = append(out, PhashGapFile{SceneID: r.sceneID, Path: r.path})
		}
	}
	return out, nil
}

// QuarantineGap is live files sitting inside a directory that no logged
// 'move' mutation put there — occupants custodian never quarantined.
type QuarantineGap struct {
	Count int      `json:"count"`
	Bytes int64    `json:"bytes"`
	Paths []string `json:"paths"`
}

// QuarantineGap finds live files under dir whose current path is not the
// to_value of any logged 'move' mutation. Returns the zero value when dir
// is empty — nothing configured to check against.
func (l *Ledger) QuarantineGap(ctx context.Context, dir string) (QuarantineGap, error) {
	dir = strings.TrimRight(dir, "/")
	if dir == "" {
		return QuarantineGap{}, nil
	}

	rows, err := l.db.QueryContext(ctx, `SELECT path, size FROM file WHERE gone_run IS NULL ORDER BY path`)
	if err != nil {
		return QuarantineGap{}, fmt.Errorf("ledger: querying live files for the quarantine gap: %w", err)
	}
	type liveFile struct {
		path string
		size int64
	}
	var under []liveFile
	for rows.Next() {
		var f liveFile
		if err := rows.Scan(&f.path, &f.size); err != nil {
			_ = rows.Close()
			return QuarantineGap{}, fmt.Errorf("ledger: scanning live file row: %w", err)
		}
		if underDir(f.path, dir) {
			under = append(under, f)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return QuarantineGap{}, fmt.Errorf("ledger: reading live file rows: %w", err)
	}
	_ = rows.Close()

	own, err := l.moveDestinations(ctx)
	if err != nil {
		return QuarantineGap{}, err
	}

	var gap QuarantineGap
	for _, f := range under {
		if own[f.path] {
			continue
		}
		gap.Count++
		gap.Bytes += f.size
		gap.Paths = append(gap.Paths, f.path)
	}
	return gap, nil
}

// underDir reports whether path is dir itself or sits inside it. Paths here
// are always Stash's own POSIX notation, so a plain prefix check is exact.
func underDir(path, dir string) bool {
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}

// moveDestinations is the set of paths a logged 'move' mutation has ever
// sent a file to — what QuarantineGap treats as "custodian put this here".
func (l *Ledger) moveDestinations(ctx context.Context) (map[string]bool, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT DISTINCT to_value FROM mutation_log WHERE kind = 'move' AND to_value != ''`)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying move destinations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var to string
		if err := rows.Scan(&to); err != nil {
			return nil, fmt.Errorf("ledger: scanning move destination row: %w", err)
		}
		out[to] = true
	}
	return out, rows.Err()
}

// ScenesWithoutPrimaryFile returns the ids of live scenes holding no live
// primary file, in scene id order.
func (l *Ledger) ScenesWithoutPrimaryFile(ctx context.Context) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT s.id FROM scene s
		WHERE s.gone_run IS NULL
		  AND s.id NOT IN (SELECT scene_id FROM file WHERE is_primary = 1 AND gone_run IS NULL)
		ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying scenes without a primary file: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ledger: scanning scene id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// TombstonedGroupRef is one live candidate_group's reference to a member
// scene that is gone from Stash.
type TombstonedGroupRef struct {
	SceneID   string `json:"scene_id"`
	Signature string `json:"signature"`
	Verdict   string `json:"verdict"`
}

// TombstonedInLiveGroups returns every live candidate_group member whose
// scene has been tombstoned: it disappeared from Stash after `scan` last
// saw it, and a rescan hasn't rebuilt the group without it yet.
func (l *Ledger) TombstonedInLiveGroups(ctx context.Context) ([]TombstonedGroupRef, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT cs.scene_id, cs.signature, cg.verdict
		FROM candidate_scene cs
		JOIN candidate_group cg ON cg.signature = cs.signature AND cg.gone_run IS NULL
		JOIN scene s ON s.id = cs.scene_id
		WHERE s.gone_run IS NOT NULL
		ORDER BY cs.signature, cs.scene_id`)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying tombstoned scenes in live groups: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []TombstonedGroupRef
	for rows.Next() {
		var ref TombstonedGroupRef
		if err := rows.Scan(&ref.SceneID, &ref.Signature, &ref.Verdict); err != nil {
			return nil, fmt.Errorf("ledger: scanning tombstoned group row: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
