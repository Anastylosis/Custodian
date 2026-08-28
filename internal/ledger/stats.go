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
	GroupsCertain           int
	GroupsConflicted        int
	GroupsDismissed         int
	GroupsPartial           int
	GroupsMistag            int
	GroupsQuarantined       int
	GroupsReclaimed         int
	ReclaimableBytes        int64
	PartialBytes            int64
	QuarantinedBytes        int64
	ReclaimedBytes          int64
	LastSync                *Run // most recent full/incremental run
	LastScan                *Run // most recent scan run
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

	if err := l.statsGroups(ctx, &st); err != nil {
		return Stats{}, err
	}

	lastSync, err := l.LastRunOfKind(ctx, "full", "incremental")
	if err != nil {
		return Stats{}, err
	}
	st.LastSync = lastSync

	lastScan, err := l.LastRunOfKind(ctx, "scan")
	if err != nil {
		return Stats{}, err
	}
	st.LastScan = lastScan

	return st, nil
}

// statsGroups fills the duplicate-group counts by verdict and the total
// reclaimable bytes across live groups.
func (l *Ledger) statsGroups(ctx context.Context, st *Stats) error {
	rows, err := l.db.QueryContext(ctx,
		`SELECT verdict, COUNT(*), COALESCE(SUM(reclaimable), 0)
		 FROM candidate_group WHERE gone_run IS NULL GROUP BY verdict`)
	if err != nil {
		return fmt.Errorf("ledger: counting candidate_group by verdict: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var verdict string
		var count int
		var reclaimable int64
		if err := rows.Scan(&verdict, &count, &reclaimable); err != nil {
			return fmt.Errorf("ledger: scanning candidate_group verdict row: %w", err)
		}
		switch verdict {
		case VerdictCertain:
			st.GroupsCertain = count
			st.ReclaimableBytes += reclaimable
		case VerdictConflicted:
			st.GroupsConflicted = count
			st.ReclaimableBytes += reclaimable
		case VerdictDismissed:
			st.GroupsDismissed = count
			st.ReclaimableBytes += reclaimable
		case VerdictPartial:
			// A `partial` group's keeper (the matching copy) is chosen by
			// `plan` exactly like a `certain` group's, so its losers'
			// bytes count toward the same reclaimable total.
			st.GroupsPartial = count
			st.PartialBytes = reclaimable
			st.ReclaimableBytes += reclaimable
		case VerdictMistag:
			// No move happens for a mistag group — apply only tags — so
			// its bytes are not reclaimable and are not added to the total.
			st.GroupsMistag = count
		case VerdictQuarantined:
			// Not counted as reclaimable: the bytes are still on disk, but
			// they are spoken for — `prune` is what frees them.
			st.GroupsQuarantined = count
			st.QuarantinedBytes = reclaimable
		case VerdictReclaimed:
			st.GroupsReclaimed = count
			st.ReclaimedBytes = reclaimable
		}
	}
	return rows.Err()
}
