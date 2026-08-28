package ledger

import (
	"context"
	"fmt"
)

// Stats is the headline "where does the library stand" summary — the
// numbers `custodian status` prints.
type Stats struct {
	ScenesLive              int
	ScenesGone              int
	FilesLive               int
	ScenesWithMultipleFiles int
	ScenesWithStashID       int
	ScenesWithoutStashID    int
	BytesLive               int64
	FilesWithoutPhash       int
	LastRun                 *Run
}

// Stats computes the current library summary from the ledger.
func (l *Ledger) Stats(ctx context.Context) (Stats, error) {
	var st Stats

	if err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scene WHERE gone_run IS NULL`).Scan(&st.ScenesLive); err != nil {
		return Stats{}, fmt.Errorf("ledger: counting live scenes: %w", err)
	}
	if err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scene WHERE gone_run IS NOT NULL`).Scan(&st.ScenesGone); err != nil {
		return Stats{}, fmt.Errorf("ledger: counting gone scenes: %w", err)
	}
	if err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file WHERE gone_run IS NULL`).Scan(&st.FilesLive); err != nil {
		return Stats{}, fmt.Errorf("ledger: counting live files: %w", err)
	}
	if err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (
		   SELECT scene_id FROM file WHERE gone_run IS NULL
		   GROUP BY scene_id HAVING COUNT(*) > 1
		 )`).Scan(&st.ScenesWithMultipleFiles); err != nil {
		return Stats{}, fmt.Errorf("ledger: counting multi-file scenes: %w", err)
	}
	if err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT scene_id) FROM scene_stash_id
		 WHERE scene_id IN (SELECT id FROM scene WHERE gone_run IS NULL)`).
		Scan(&st.ScenesWithStashID); err != nil {
		return Stats{}, fmt.Errorf("ledger: counting scenes with stash_id: %w", err)
	}
	st.ScenesWithoutStashID = st.ScenesLive - st.ScenesWithStashID

	if err := l.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(size), 0) FROM file WHERE gone_run IS NULL`).
		Scan(&st.BytesLive); err != nil {
		return Stats{}, fmt.Errorf("ledger: summing live bytes: %w", err)
	}
	if err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM file WHERE gone_run IS NULL AND phash IS NULL`).
		Scan(&st.FilesWithoutPhash); err != nil {
		return Stats{}, fmt.Errorf("ledger: counting files without phash: %w", err)
	}

	lastRun, err := l.LastRun(ctx)
	if err != nil {
		return Stats{}, err
	}
	st.LastRun = lastRun

	return st, nil
}
