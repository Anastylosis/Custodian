package ledger

import (
	"context"
	"fmt"
	"strings"
)

// Canonical is one source's authoritative duration and title for a scene —
// evidence.Rejudge classifies the scene's own file duration against it.
type Canonical struct {
	SceneID    string
	Source     string // "stash-box" | "fss"
	Origin     string // endpoint URL or catalogue path
	Duration   float64
	Title      string
	MatchedBy  string
	FetchedRun int64
}

// PutCanonical inserts or replaces the canonical row for (scene_id, source,
// origin).
func (l *Ledger) PutCanonical(ctx context.Context, c Canonical) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO canonical(scene_id, source, origin, duration, title, matched_by, fetched_run)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(scene_id, source, origin) DO UPDATE SET
		   duration = excluded.duration, title = excluded.title,
		   matched_by = excluded.matched_by, fetched_run = excluded.fetched_run`,
		c.SceneID, c.Source, c.Origin, c.Duration, c.Title, c.MatchedBy, c.FetchedRun)
	if err != nil {
		return fmt.Errorf("ledger: putting canonical for scene %s: %w", c.SceneID, err)
	}
	return nil
}

// Canonicals returns every canonical row for the given scene ids, keyed by
// scene id. A scene id with no canonical row is simply absent from the map.
func (l *Ledger) Canonicals(ctx context.Context, sceneIDs []string) (map[string][]Canonical, error) {
	out := make(map[string][]Canonical, len(sceneIDs))
	if len(sceneIDs) == 0 {
		return out, nil
	}
	placeholders, args := inClause(sceneIDs)
	rows, err := l.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT scene_id, source, origin, duration, title, matched_by, fetched_run
		 FROM canonical WHERE scene_id IN (%s) ORDER BY scene_id, source, origin`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying canonical: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var c Canonical
		if err := rows.Scan(&c.SceneID, &c.Source, &c.Origin, &c.Duration, &c.Title,
			&c.MatchedBy, &c.FetchedRun); err != nil {
			return nil, fmt.Errorf("ledger: scanning canonical row: %w", err)
		}
		out[c.SceneID] = append(out[c.SceneID], c)
	}
	return out, rows.Err()
}

// ScenesNeedingCanonical returns the ids of scenes belonging to a live
// candidate_group with the given verdict that carry no canonical row yet —
// what `custodian canon` looks up.
func (l *Ledger) ScenesNeedingCanonical(ctx context.Context, verdict string) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT DISTINCT cs.scene_id
		FROM candidate_scene cs
		JOIN candidate_group g ON g.signature = cs.signature AND g.gone_run IS NULL
		WHERE g.verdict = ?
		  AND cs.scene_id NOT IN (SELECT scene_id FROM canonical)
		ORDER BY cs.scene_id`, verdict)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying scenes needing canonical: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ledger: scanning scene id needing canonical: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// StashIDRef is one stash-box link on a SceneRef.
type StashIDRef struct {
	Endpoint string
	StashID  string
}

// SceneRef is the scene identity internal/canonical's sources match
// against: title, date, studio, stash ids, and the scene's own primary
// file duration.
type SceneRef struct {
	ID       string
	Title    string
	Date     string
	Studio   string
	StashIDs []StashIDRef
	Duration float64
}

// SceneRefs builds a SceneRef for each of the given scene ids, from the
// scene, scene_stash_id and primary file tables. A scene id with no
// matching scene row is simply absent from the result; the returned order
// follows the underlying scan, not the input order.
func (l *Ledger) SceneRefs(ctx context.Context, sceneIDs []string) ([]SceneRef, error) {
	if len(sceneIDs) == 0 {
		return nil, nil
	}
	placeholders, args := inClause(sceneIDs)

	rows, err := l.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT s.id, s.title, s.date, s.studio_name, COALESCE(f.duration, 0)
		 FROM scene s
		 LEFT JOIN file f ON f.scene_id = s.id AND f.is_primary = 1 AND f.gone_run IS NULL
		 WHERE s.id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying scene refs: %w", err)
	}
	refs := make(map[string]*SceneRef, len(sceneIDs))
	var order []string
	for rows.Next() {
		var r SceneRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Date, &r.Studio, &r.Duration); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("ledger: scanning scene ref row: %w", err)
		}
		cp := r
		refs[r.ID] = &cp
		order = append(order, r.ID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("ledger: reading scene ref rows: %w", err)
	}
	_ = rows.Close()

	sidRows, err := l.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT scene_id, endpoint, stash_id FROM scene_stash_id WHERE scene_id IN (%s)`, placeholders), args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying scene ref stash_ids: %w", err)
	}
	defer func() { _ = sidRows.Close() }()
	for sidRows.Next() {
		var sceneID, endpoint, stashID string
		if err := sidRows.Scan(&sceneID, &endpoint, &stashID); err != nil {
			return nil, fmt.Errorf("ledger: scanning scene ref stash_id row: %w", err)
		}
		if r, ok := refs[sceneID]; ok {
			r.StashIDs = append(r.StashIDs, StashIDRef{Endpoint: endpoint, StashID: stashID})
		}
	}
	if err := sidRows.Err(); err != nil {
		return nil, fmt.Errorf("ledger: reading scene ref stash_id rows: %w", err)
	}

	out := make([]SceneRef, 0, len(order))
	for _, id := range order {
		out = append(out, *refs[id])
	}
	return out, nil
}

// inClause builds a "?,?,?" placeholder string and the matching args slice
// for an IN (...) clause over ids.
func inClause(ids []string) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
