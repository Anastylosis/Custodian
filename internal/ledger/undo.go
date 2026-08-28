package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// MutationsByRun returns the mutation log rows written under runID, oldest
// first — the order `reclaim.Undo` needs to replay what one apply run did,
// as opposed to Mutations' newest-first form for `custodian log`.
func (l *Ledger) MutationsByRun(ctx context.Context, runID int64) ([]Mutation, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, run_id, at, kind, target, from_value, to_value, note, ok
		   FROM mutation_log WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying mutation_log for run %d: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Mutation
	for rows.Next() {
		var m Mutation
		var rid sql.NullInt64
		var ok sql.NullBool
		if err := rows.Scan(&m.ID, &rid, &m.At, &m.Kind, &m.Target, &m.From, &m.To, &m.Note, &ok); err != nil {
			return nil, fmt.Errorf("ledger: scanning mutation_log row: %w", err)
		}
		m.RunID = rid.Int64
		if ok.Valid {
			v := ok.Bool
			m.OK = &v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GroupBySignature returns the one candidate_group with this signature,
// gone or not — `custodian undo <signature>` needs to find a quarantined
// group regardless of whether a later scan tombstoned it.
func (l *Ledger) GroupBySignature(ctx context.Context, signature string) (Group, bool, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT g.signature, g.verdict, g.reason, g.reclaimable, g.keeper, g.applied_run, r.started_at
		   FROM candidate_group g LEFT JOIN run r ON r.id = g.applied_run
		  WHERE g.signature = ?`, signature)
	g, err := scanGroupCommon(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, false, nil
	}
	if err != nil {
		return Group{}, false, fmt.Errorf("ledger: reading group %s: %w", signature, err)
	}
	if err := l.fillGroup(ctx, &g); err != nil {
		return Group{}, false, err
	}
	return g, true, nil
}

// GroupsByAppliedRun returns every candidate_group whose applied_run is
// runID — how `custodian undo --run N` finds everything one apply call
// quarantined. A group later pruned under a different run no longer carries
// this applied_run (SetOutcome overwrites it), so it will not reappear here.
func (l *Ledger) GroupsByAppliedRun(ctx context.Context, runID int64) ([]Group, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT g.signature, g.verdict, g.reason, g.reclaimable, g.keeper, g.applied_run, r.started_at
		   FROM candidate_group g LEFT JOIN run r ON r.id = g.applied_run
		  WHERE g.applied_run = ? ORDER BY g.signature`, runID)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying groups applied by run %d: %w", runID, err)
	}
	var groups []Group
	for rows.Next() {
		g, err := scanGroupCommon(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("ledger: scanning candidate_group row: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("ledger: reading candidate_group rows: %w", err)
	}
	_ = rows.Close()

	for i := range groups {
		if err := l.fillGroup(ctx, &groups[i]); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

// groupScanner is the shape *sql.Row and *sql.Rows share for the
// signature/verdict/.../applied_run projection GroupBySignature and
// GroupsByAppliedRun both read.
type groupScanner interface {
	Scan(dest ...any) error
}

func scanGroupCommon(row groupScanner) (Group, error) {
	var g Group
	var keeper, appliedAt sql.NullString
	var appliedRun sql.NullInt64
	if err := row.Scan(&g.Signature, &g.Verdict, &g.Reason, &g.Reclaimable, &keeper, &appliedRun, &appliedAt); err != nil {
		return Group{}, err
	}
	g.Keeper = keeper.String
	g.AppliedRun = appliedRun.Int64
	g.AppliedAt = appliedAt.String
	return g, nil
}

// fillGroup populates the scenes, evidence and keeper steps a Group carries
// alongside its candidate_group row, reusing the same per-signature queries
// Groups uses.
func (l *Ledger) fillGroup(ctx context.Context, g *Group) error {
	sceneIDs, err := l.groupSceneIDs(ctx, g.Signature)
	if err != nil {
		return err
	}
	g.SceneIDs = sceneIDs

	ev, err := l.groupEvidence(ctx, g.Signature)
	if err != nil {
		return err
	}
	g.Evidence = ev

	steps, err := l.groupSteps(ctx, g.Signature)
	if err != nil {
		return err
	}
	g.Steps = steps
	return nil
}
