package ledger

import (
	"context"
	"fmt"
)

// ScenesWithoutStashID returns the ids of live scenes carrying no stash_id
// for any endpoint, ordered by id and capped at limit (0 = no limit) — the
// population `identify --via stash-box` pre-screens.
func (l *Ledger) ScenesWithoutStashID(ctx context.Context, limit int) ([]string, error) {
	query := `SELECT id FROM scene
		WHERE gone_run IS NULL AND id NOT IN (SELECT scene_id FROM scene_stash_id)
		ORDER BY id`
	return l.queryIDs(ctx, query, limit, "scenes without a stash_id")
}

// ScenesWithURLNoStashID returns the ids of live scenes carrying at least one
// URL and no stash_id for any endpoint, ordered by id and capped at limit
// (0 = no limit) — the population `identify --via url` considers.
func (l *Ledger) ScenesWithURLNoStashID(ctx context.Context, limit int) ([]string, error) {
	query := `SELECT DISTINCT s.id FROM scene s
		JOIN scene_url u ON u.scene_id = s.id
		WHERE s.gone_run IS NULL AND s.id NOT IN (SELECT scene_id FROM scene_stash_id)
		ORDER BY s.id`
	return l.queryIDs(ctx, query, limit, "scenes with a url and no stash_id")
}

// queryIDs runs a query selecting a single id column, appending a LIMIT
// clause when limit > 0.
func (l *Ledger) queryIDs(ctx context.Context, query string, limit int, what string) ([]string, error) {
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ledger: scanning %s: %w", what, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
