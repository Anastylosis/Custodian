package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Run is one sync's history and audit record.
type Run struct {
	ID           int64
	StartedAt    string
	FinishedAt   string
	Kind         string // "full" | "incremental"
	Since        string
	ScenesSeen   int
	FilesSeen    int
	ScenesGone   int
	StashVersion string
	Notes        string
}

// RunSummary is what FinishRun records once a sync completes.
type RunSummary struct {
	ScenesSeen   int
	FilesSeen    int
	ScenesGone   int
	StashVersion string
	Notes        string
}

// BeginRun records the start of a sync and returns its run id.
func (l *Ledger) BeginRun(ctx context.Context, kind, since string) (int64, error) {
	res, err := l.db.ExecContext(ctx,
		`INSERT INTO run(started_at, kind, since) VALUES(?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339), kind, nullIfEmpty(since))
	if err != nil {
		return 0, fmt.Errorf("ledger: beginning run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("ledger: reading new run id: %w", err)
	}
	return id, nil
}

// FinishRun records a sync's outcome against its run id.
func (l *Ledger) FinishRun(ctx context.Context, runID int64, summary RunSummary) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE run SET finished_at = ?, scenes_seen = ?, files_seen = ?,
		   scenes_gone = ?, stash_version = ?, notes = ?
		 WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), summary.ScenesSeen, summary.FilesSeen,
		summary.ScenesGone, summary.StashVersion, summary.Notes, runID)
	if err != nil {
		return fmt.Errorf("ledger: finishing run %d: %w", runID, err)
	}
	return nil
}

// LastRun returns the most recently started run, or nil if none has run yet.
func (l *Ledger) LastRun(ctx context.Context) (*Run, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT id, started_at, finished_at, kind, since, scenes_seen, files_seen,
		        scenes_gone, stash_version, notes
		 FROM run ORDER BY id DESC LIMIT 1`)

	var r Run
	var finishedAt, since sql.NullString
	err := row.Scan(&r.ID, &r.StartedAt, &finishedAt, &r.Kind, &since, &r.ScenesSeen,
		&r.FilesSeen, &r.ScenesGone, &r.StashVersion, &r.Notes)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("ledger: reading last run: %w", err)
	}
	r.FinishedAt = finishedAt.String
	r.Since = since.String
	return &r, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
