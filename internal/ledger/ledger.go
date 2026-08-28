// Package ledger is the SQLite mirror of a Stash library: scene and file
// state, run history, operator decisions and the mutation log. Rows are
// never deleted — a gone scene or file is tombstoned, not removed.
package ledger

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	// Registers the "sqlite" driver; Open dials it by name.
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// currentSchemaVersion is the schema version this build knows how to apply.
const currentSchemaVersion = 3

// Ledger wraps a database/sql connection to the ledger's SQLite file.
type Ledger struct {
	db *sql.DB
}

// Open opens (or creates) the ledger at path and applies the embedded
// schema. Idempotent — calling Open twice on the same file is fine.
func Open(path string) (*Ledger, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ledger: opening sqlite at %s: %w", path, err)
	}
	// modernc/sqlite is happiest with one writer; cap to keep behavior obvious.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	l := &Ledger{db: db}
	if err := l.applySchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return l, nil
}

// Close closes the underlying database.
func (l *Ledger) Close() error {
	return l.db.Close()
}

// DB returns the raw *sql.DB. Exposed for tests and ad-hoc queries;
// production code should use the typed methods on Ledger.
func (l *Ledger) DB() *sql.DB {
	return l.db
}

// applySchema runs the embedded schema.sql, which is always the latest
// shape for tables that don't exist yet, then migrates any column an older
// database is missing and stamps schema_version.
func (l *Ledger) applySchema(ctx context.Context) error {
	if _, err := l.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("ledger: applying schema: %w", err)
	}

	var current int
	if err := l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("ledger: reading schema_version: %w", err)
	}

	if current < 3 {
		if err := l.addColumnIfMissing(ctx, "candidate_group", "keeper", "TEXT"); err != nil {
			return err
		}
	}

	if current >= currentSchemaVersion {
		return nil
	}
	if _, err := l.db.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES(?)`, currentSchemaVersion); err != nil {
		return fmt.Errorf("ledger: recording schema_version=%d: %w", currentSchemaVersion, err)
	}
	return nil
}

// addColumnIfMissing ALTERs table to add column with the given type
// declaration, unless PRAGMA table_info already reports it — schema.sql's
// CREATE TABLE IF NOT EXISTS never adds a column to a table that already
// exists, so a new column on an old table needs this instead.
func (l *Ledger) addColumnIfMissing(ctx context.Context, table, column, decl string) error {
	rows, err := l.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return fmt.Errorf("ledger: reading %s columns: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("ledger: scanning %s column info: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("ledger: reading %s column info: %w", table, err)
	}
	_ = rows.Close()
	if found {
		return nil
	}
	if _, err := l.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, decl)); err != nil {
		return fmt.Errorf("ledger: adding %s.%s: %w", table, column, err)
	}
	return nil
}

// NewestUpdatedAt returns the newest scene.updated_at stored, or "" if the
// ledger holds no scenes yet.
func (l *Ledger) NewestUpdatedAt(ctx context.Context) (string, error) {
	var newest sql.NullString
	err := l.db.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM scene`).Scan(&newest)
	if err != nil {
		return "", fmt.Errorf("ledger: reading newest updated_at: %w", err)
	}
	return newest.String, nil
}
