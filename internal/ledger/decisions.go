package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"time"
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
// when, from and to. OK is nil while the write is in flight, and true or
// false once the server has answered.
type Mutation struct {
	ID     int64
	RunID  int64
	At     string
	Kind   string
	Target string
	From   string
	To     string
	Note   string
	OK     *bool
}

// AppendMutation records one attempted write and returns its row id. Call it
// BEFORE sending the write, so a crash mid-way still leaves the attempt on
// record, then stamp the outcome with MarkMutation.
func (l *Ledger) AppendMutation(ctx context.Context, m Mutation) (int64, error) {
	at := m.At
	if at == "" {
		at = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := l.db.ExecContext(ctx,
		`INSERT INTO mutation_log(run_id, at, kind, target, from_value, to_value, note, ok)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		nullIfZero(m.RunID), at, m.Kind, m.Target, m.From, m.To, m.Note, nullableBool(m.OK))
	if err != nil {
		return 0, fmt.Errorf("ledger: appending mutation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("ledger: reading new mutation id: %w", err)
	}
	return id, nil
}

// MarkMutation stamps the outcome of the write appended under id. A non-empty
// note is appended to whatever the row already carried, so the reason a write
// failed sits beside what it was trying to do.
func (l *Ledger) MarkMutation(ctx context.Context, id int64, ok bool, note string) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE mutation_log
		    SET ok = ?,
		        note = CASE WHEN ? = '' THEN note
		                    WHEN note = '' THEN ?
		                    ELSE note || ' — ' || ? END
		  WHERE id = ?`,
		boolToInt(ok), note, note, note, id)
	if err != nil {
		return fmt.Errorf("ledger: marking mutation %d: %w", id, err)
	}
	return nil
}

// Mutations returns the mutation log newest first, capped at limit (0 = all).
func (l *Ledger) Mutations(ctx context.Context, limit int) ([]Mutation, error) {
	query := `SELECT id, run_id, at, kind, target, from_value, to_value, note, ok
	          FROM mutation_log ORDER BY id DESC`
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying mutation_log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Mutation
	for rows.Next() {
		var m Mutation
		var runID sql.NullInt64
		var ok sql.NullBool
		if err := rows.Scan(&m.ID, &runID, &m.At, &m.Kind, &m.Target,
			&m.From, &m.To, &m.Note, &ok); err != nil {
			return nil, fmt.Errorf("ledger: scanning mutation_log row: %w", err)
		}
		m.RunID = runID.Int64
		if ok.Valid {
			v := ok.Bool
			m.OK = &v
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullableBool(v *bool) any {
	if v == nil {
		return nil
	}
	return boolToInt(*v)
}

func nullIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
