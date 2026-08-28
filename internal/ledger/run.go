package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

// LastRun returns the most recently started run of any kind, or nil if
// none has run yet.
func (l *Ledger) LastRun(ctx context.Context) (*Run, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT id, started_at, finished_at, kind, since, scenes_seen, files_seen,
		        scenes_gone, stash_version, notes
		 FROM run ORDER BY id DESC LIMIT 1`)
	return scanRun(row, "reading last run")
}

// LastRunOfKind returns the most recently started run whose kind is one of
// kinds — e.g. LastRunOfKind(ctx, "full", "incremental") for the last sync,
// as opposed to LastRun, which would also match a "scan" run. Nil if none
// has run yet.
func (l *Ledger) LastRunOfKind(ctx context.Context, kinds ...string) (*Run, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(kinds))
	args := make([]any, len(kinds))
	for i, k := range kinds {
		placeholders[i] = "?"
		args[i] = k
	}
	row := l.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id, started_at, finished_at, kind, since, scenes_seen, files_seen,
		        scenes_gone, stash_version, notes
		 FROM run WHERE kind IN (%s) ORDER BY id DESC LIMIT 1`, strings.Join(placeholders, ",")),
		args...)
	return scanRun(row, "reading last run of kind")
}

func scanRun(row *sql.Row, errContext string) (*Run, error) {
	var r Run
	var finishedAt, since sql.NullString
	err := row.Scan(&r.ID, &r.StartedAt, &finishedAt, &r.Kind, &since, &r.ScenesSeen,
		&r.FilesSeen, &r.ScenesGone, &r.StashVersion, &r.Notes)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("ledger: %s: %w", errContext, err)
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
