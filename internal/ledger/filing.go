package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// FilingScene is one live scene's identity for `file` (organize) and
// `captions` (subs): enough to compute its ideal path from the template or
// match it against a loose subtitle, without a round-trip to Stash.
type FilingScene struct {
	SceneID    string
	Title      string
	Date       string
	StudioName string
	Performers []string
	FileID     string
	Path       string
	Basename   string
	Height     int
	Size       int64
	Duration   float64
}

// FilingFilter narrows FilingInputs.
type FilingFilter struct {
	// Limit caps how many scenes are returned, in scene id order (0 = no limit).
	Limit int
}

// filingPerformerSep separates names in the GROUP_CONCAT below: the ASCII
// unit separator, vanishingly unlikely to appear in a performer's name.
const filingPerformerSep = "\x1f"

// FilingInputs returns every live scene with a live primary file, its
// studio name and its performer names — the read `file` and `captions` both
// need instead of paging Stash directly.
func (l *Ledger) FilingInputs(ctx context.Context, filter FilingFilter) ([]FilingScene, error) {
	query := `
		SELECT s.id, s.title, s.date, s.studio_name, f.id, f.path, f.basename,
		       f.height, f.size, f.duration,
		       (SELECT GROUP_CONCAT(sp.name, char(31)) FROM scene_performer sp WHERE sp.scene_id = s.id)
		FROM scene s
		JOIN file f ON f.scene_id = s.id AND f.is_primary = 1 AND f.gone_run IS NULL
		WHERE s.gone_run IS NULL
		ORDER BY s.id`
	var args []any
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying filing inputs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []FilingScene
	for rows.Next() {
		var fs FilingScene
		var performers sql.NullString
		if err := rows.Scan(&fs.SceneID, &fs.Title, &fs.Date, &fs.StudioName,
			&fs.FileID, &fs.Path, &fs.Basename, &fs.Height, &fs.Size, &fs.Duration, &performers); err != nil {
			return nil, fmt.Errorf("ledger: scanning filing input row: %w", err)
		}
		if performers.Valid && performers.String != "" {
			fs.Performers = strings.Split(performers.String, filingPerformerSep)
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

// DuplicateLoserSceneIDs returns every live scene that is a non-keeper
// member of a live `quarantined` or `partial` group (REBUILD.md §7 phase
// 5a): these are about to leave the library, via prune or the apply run
// that already quarantined them, so `file` leaves them where they are
// rather than filing a copy that is about to move again or vanish.
func (l *Ledger) DuplicateLoserSceneIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT cs.scene_id
		FROM candidate_scene cs
		JOIN candidate_group cg ON cg.signature = cs.signature
		WHERE cg.gone_run IS NULL
		  AND cg.verdict IN (?, ?)
		  AND (cg.keeper IS NULL OR cg.keeper != cs.scene_id)`,
		VerdictQuarantined, VerdictPartial)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying duplicate loser scenes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ledger: scanning duplicate loser row: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// LiveFilePaths maps every live file's path to its owning scene id — `file`
// (organize)'s on-disk collision check: a target path already claimed by a
// different scene's file is a conflict, not a move.
func (l *Ledger) LiveFilePaths(ctx context.Context) (map[string]string, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT path, scene_id FROM file WHERE gone_run IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying live file paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var path, sceneID string
		if err := rows.Scan(&path, &sceneID); err != nil {
			return nil, fmt.Errorf("ledger: scanning live file path row: %w", err)
		}
		out[path] = sceneID
	}
	return out, rows.Err()
}
