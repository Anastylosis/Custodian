package ledger

import (
	"context"
	"database/sql"
	"fmt"
)

// Decision is an operator override, keyed by a group's sorted file oshashes
// (falling back to a shared stash_id). Nothing reads these yet.
type Decision struct {
	Key       string
	Kind      string
	Value     string
	Keeper    string
	DecidedAt string
	Notes     string
}

// PutDecision inserts or replaces the decision stored under d.Key.
func (l *Ledger) PutDecision(ctx context.Context, d Decision) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO decision(key, kind, value, keeper, decided_at, notes)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   kind = excluded.kind, value = excluded.value, keeper = excluded.keeper,
		   decided_at = excluded.decided_at, notes = excluded.notes`,
		d.Key, d.Kind, d.Value, nullIfEmpty(d.Keeper), d.DecidedAt, d.Notes)
	if err != nil {
		return fmt.Errorf("ledger: putting decision %s: %w", d.Key, err)
	}
	return nil
}

// GetDecision returns the decision stored under key, and false if there is
// none.
func (l *Ledger) GetDecision(ctx context.Context, key string) (Decision, bool, error) {
	var d Decision
	var keeper sql.NullString
	err := l.db.QueryRowContext(ctx,
		`SELECT key, kind, value, keeper, decided_at, notes FROM decision WHERE key = ?`, key).
		Scan(&d.Key, &d.Kind, &d.Value, &keeper, &d.DecidedAt, &d.Notes)
	switch {
	case err == sql.ErrNoRows:
		return Decision{}, false, nil
	case err != nil:
		return Decision{}, false, fmt.Errorf("ledger: getting decision %s: %w", key, err)
	}
	d.Keeper = keeper.String
	return d, true, nil
}

// Mutation is one write Custodian made to Stash or the filesystem: what,
// when, from and to.
type Mutation struct {
	RunID  int64
	At     string
	Kind   string
	Target string
	From   string
	To     string
	Note   string
}

// AppendMutation records one entry in the mutation log. Apply phases will
// call this before and after every destructive step; nothing calls it yet.
func (l *Ledger) AppendMutation(ctx context.Context, m Mutation) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO mutation_log(run_id, at, kind, target, from_value, to_value, note)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		m.RunID, m.At, m.Kind, m.Target, m.From, m.To, m.Note)
	if err != nil {
		return fmt.Errorf("ledger: appending mutation: %w", err)
	}
	return nil
}
