package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Verdict values a policy can reach. partial/mistag/probable/rejected arrive
// with later generators.
const (
	VerdictCertain    = "certain"
	VerdictConflicted = "conflicted"
	VerdictDismissed  = "dismissed"

	// VerdictQuarantined is where `apply` leaves a group: the losers are
	// tagged and their files sit in the quarantine directory, still in
	// Stash and still on disk. Nothing has been deleted, so "applied"
	// would name something that did not happen.
	VerdictQuarantined = "quarantined"

	// VerdictReclaimed is where `prune` leaves a group: the losers were
	// merged into the keeper and their files deleted from disk.
	VerdictReclaimed = "reclaimed"
)

// Evidence is one fact a generator found supporting or informing a
// candidate_group's verdict.
type Evidence struct {
	Generator string
	Kind      string
	Detail    string
	Value     *float64
}

// Group is a duplicate-candidate group: the scenes evidence ties together,
// the evidence itself, the verdict a policy reached, and — once `plan` has
// run — the keeper it chose and the steps that decided it.
type Group struct {
	Signature   string
	SceneIDs    []string
	Evidence    []Evidence
	Verdict     string
	Reason      string
	Reclaimable int64
	Keeper      string
	Steps       []Step

	// AppliedRun is the run that quarantined or reclaimed the group, and
	// AppliedAt when that run started. Both are zero until `apply` acts.
	AppliedRun int64
	AppliedAt  string
}

// Step is one rule internal/keeper evaluated while choosing a group's
// keeper: the rule's name, what it decided, and why.
type Step struct {
	Rule    string
	Outcome string
	Detail  string
}

// GroupFilter narrows Groups.
type GroupFilter struct {
	Verdict     string
	IncludeGone bool

	// AppliedBefore keeps only groups whose applying run started before
	// this instant — how `prune` finds quarantines old enough to reclaim.
	// The zero time means "no age filter".
	AppliedBefore time.Time
}

// ReplaceGroups upserts groups seen this run and marks gone_run on any
// candidate_group not seen — wholesale for the run, the way TombstoneUnseen
// treats scenes.
func (l *Ledger) ReplaceGroups(ctx context.Context, runID int64, groups []Group) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: beginning ReplaceGroups transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmts, err := prepareGroupStatements(ctx, tx)
	if err != nil {
		return err
	}
	defer stmts.close()

	for _, g := range groups {
		if err := stmts.upsertGroup(ctx, runID, g); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE candidate_group SET gone_run = ? WHERE seen_run < ? AND gone_run IS NULL`,
		runID, runID); err != nil {
		return fmt.Errorf("ledger: tombstoning gone candidate_groups: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: committing ReplaceGroups: %w", err)
	}
	return nil
}

type groupStatements struct {
	group    *sql.Stmt
	delScene *sql.Stmt
	insScene *sql.Stmt
	delEv    *sql.Stmt
	insEv    *sql.Stmt
}

func prepareGroupStatements(ctx context.Context, tx *sql.Tx) (*groupStatements, error) {
	prep := func(query string) (*sql.Stmt, error) { return tx.PrepareContext(ctx, query) }

	var s groupStatements
	var err error
	for _, step := range []struct {
		dst   **sql.Stmt
		query string
	}{
		{&s.group, `INSERT INTO candidate_group(signature, verdict, reason, reclaimable, seen_run, gone_run)
				VALUES(?, ?, ?, ?, ?, NULL)
			ON CONFLICT(signature) DO UPDATE SET
				verdict = excluded.verdict, reason = excluded.reason,
				reclaimable = excluded.reclaimable, seen_run = excluded.seen_run, gone_run = NULL`},
		{&s.delScene, `DELETE FROM candidate_scene WHERE signature = ?`},
		{&s.insScene, `INSERT INTO candidate_scene(signature, scene_id) VALUES(?, ?)`},
		{&s.delEv, `DELETE FROM candidate_evidence WHERE signature = ?`},
		{&s.insEv, `INSERT INTO candidate_evidence(signature, generator, kind, detail, value) VALUES(?, ?, ?, ?, ?)`},
	} {
		*step.dst, err = prep(step.query)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("ledger: preparing candidate_group statements: %w", err)
		}
	}
	return &s, nil
}

func (s *groupStatements) close() {
	for _, stmt := range []*sql.Stmt{s.group, s.delScene, s.insScene, s.delEv, s.insEv} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}

func (s *groupStatements) upsertGroup(ctx context.Context, runID int64, g Group) error {
	if _, err := s.group.ExecContext(ctx, g.Signature, g.Verdict, g.Reason, g.Reclaimable, runID); err != nil {
		return fmt.Errorf("ledger: upserting candidate_group %s: %w", g.Signature, err)
	}

	if _, err := s.delScene.ExecContext(ctx, g.Signature); err != nil {
		return fmt.Errorf("ledger: clearing candidate_scene for %s: %w", g.Signature, err)
	}
	for _, id := range g.SceneIDs {
		if _, err := s.insScene.ExecContext(ctx, g.Signature, id); err != nil {
			return fmt.Errorf("ledger: adding candidate_scene to %s: %w", g.Signature, err)
		}
	}

	if _, err := s.delEv.ExecContext(ctx, g.Signature); err != nil {
		return fmt.Errorf("ledger: clearing candidate_evidence for %s: %w", g.Signature, err)
	}
	for _, e := range g.Evidence {
		if _, err := s.insEv.ExecContext(ctx, g.Signature, e.Generator, e.Kind, e.Detail, nullableFloat(e.Value)); err != nil {
			return fmt.Errorf("ledger: adding candidate_evidence to %s: %w", g.Signature, err)
		}
	}
	return nil
}

// Groups returns candidate groups matching filter, each with its scenes and
// evidence populated.
func (l *Ledger) Groups(ctx context.Context, filter GroupFilter) ([]Group, error) {
	query := `SELECT g.signature, g.verdict, g.reason, g.reclaimable, g.keeper,
	                 g.applied_run, r.started_at
	          FROM candidate_group g
	          LEFT JOIN run r ON r.id = g.applied_run
	          WHERE 1=1`
	var args []any
	if !filter.IncludeGone {
		query += ` AND g.gone_run IS NULL`
	}
	if filter.Verdict != "" {
		query += ` AND g.verdict = ?`
		args = append(args, filter.Verdict)
	}
	if !filter.AppliedBefore.IsZero() {
		query += ` AND g.applied_run IS NOT NULL AND r.started_at < ?`
		args = append(args, filter.AppliedBefore.UTC().Format(time.RFC3339))
	}
	query += ` ORDER BY g.signature`

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying candidate_group: %w", err)
	}
	var groups []Group
	for rows.Next() {
		var g Group
		var keeper, appliedAt sql.NullString
		var appliedRun sql.NullInt64
		if err := rows.Scan(&g.Signature, &g.Verdict, &g.Reason, &g.Reclaimable, &keeper,
			&appliedRun, &appliedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("ledger: scanning candidate_group row: %w", err)
		}
		g.Keeper = keeper.String
		g.AppliedRun = appliedRun.Int64
		g.AppliedAt = appliedAt.String
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("ledger: reading candidate_group rows: %w", err)
	}
	_ = rows.Close()

	for i := range groups {
		sceneIDs, err := l.groupSceneIDs(ctx, groups[i].Signature)
		if err != nil {
			return nil, err
		}
		groups[i].SceneIDs = sceneIDs

		ev, err := l.groupEvidence(ctx, groups[i].Signature)
		if err != nil {
			return nil, err
		}
		groups[i].Evidence = ev

		steps, err := l.groupSteps(ctx, groups[i].Signature)
		if err != nil {
			return nil, err
		}
		groups[i].Steps = steps
	}
	return groups, nil
}

func (l *Ledger) groupSceneIDs(ctx context.Context, signature string) ([]string, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT scene_id FROM candidate_scene WHERE signature = ? ORDER BY scene_id`, signature)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying candidate_scene for %s: %w", signature, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ledger: scanning candidate_scene row: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (l *Ledger) groupEvidence(ctx context.Context, signature string) ([]Evidence, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT generator, kind, detail, value FROM candidate_evidence WHERE signature = ? ORDER BY rowid`, signature)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying candidate_evidence for %s: %w", signature, err)
	}
	defer func() { _ = rows.Close() }()

	var evs []Evidence
	for rows.Next() {
		var e Evidence
		var value sql.NullFloat64
		if err := rows.Scan(&e.Generator, &e.Kind, &e.Detail, &value); err != nil {
			return nil, fmt.Errorf("ledger: scanning candidate_evidence row: %w", err)
		}
		if value.Valid {
			v := value.Float64
			e.Value = &v
		}
		evs = append(evs, e)
	}
	return evs, rows.Err()
}

func (l *Ledger) groupSteps(ctx context.Context, signature string) ([]Step, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT rule, outcome, detail FROM candidate_step WHERE signature = ? ORDER BY ord`, signature)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying candidate_step for %s: %w", signature, err)
	}
	defer func() { _ = rows.Close() }()

	var steps []Step
	for rows.Next() {
		var s Step
		if err := rows.Scan(&s.Rule, &s.Outcome, &s.Detail); err != nil {
			return nil, fmt.Errorf("ledger: scanning candidate_step row: %w", err)
		}
		steps = append(steps, s)
	}
	return steps, rows.Err()
}

// SetChoice records phase 3a's pick for signature: the keeper scene id and
// the steps that decided it. Reclaimable is recomputed from losers' primary
// file sizes and stored on the group, so status and groups reflect it.
//
// Takes keeper/losers/steps rather than a keeper.Choice to avoid a ledger →
// keeper import cycle (keeper already imports ledger for Group/PrimaryFile).
func (l *Ledger) SetChoice(ctx context.Context, signature, keeper string, losers []string, steps []Step) error {
	reclaimable, err := l.sumPrimaryFileSizes(ctx, losers)
	if err != nil {
		return err
	}

	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: beginning SetChoice transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE candidate_group SET keeper = ?, reclaimable = ? WHERE signature = ?`,
		keeper, reclaimable, signature); err != nil {
		return fmt.Errorf("ledger: setting keeper for %s: %w", signature, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM candidate_step WHERE signature = ?`, signature); err != nil {
		return fmt.Errorf("ledger: clearing candidate_step for %s: %w", signature, err)
	}
	for i, s := range steps {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO candidate_step(signature, ord, rule, outcome, detail) VALUES(?, ?, ?, ?, ?)`,
			signature, i, s.Rule, s.Outcome, s.Detail); err != nil {
			return fmt.Errorf("ledger: adding candidate_step to %s: %w", signature, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: committing SetChoice: %w", err)
	}
	return nil
}

// SetOutcome records what an apply or prune run did to a group: its new
// verdict, and the run that did it.
func (l *Ledger) SetOutcome(ctx context.Context, signature, verdict string, runID int64) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE candidate_group SET verdict = ?, applied_run = ? WHERE signature = ?`,
		verdict, nullIfZero(runID), signature)
	if err != nil {
		return fmt.Errorf("ledger: setting outcome %s for %s: %w", verdict, signature, err)
	}
	return nil
}

func (l *Ledger) sumPrimaryFileSizes(ctx context.Context, sceneIDs []string) (int64, error) {
	if len(sceneIDs) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(sceneIDs))
	args := make([]any, len(sceneIDs))
	for i, id := range sceneIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT COALESCE(SUM(size), 0) FROM file
		WHERE is_primary = 1 AND scene_id IN (%s)`, strings.Join(placeholders, ","))
	var sum int64
	if err := l.db.QueryRowContext(ctx, query, args...).Scan(&sum); err != nil {
		return 0, fmt.Errorf("ledger: summing loser primary file sizes: %w", err)
	}
	return sum, nil
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// StashIDCluster is one stash-box entry shared by more than one live scene.
type StashIDCluster struct {
	Endpoint string
	StashID  string
	Scenes   []ClusterScene
}

// ClusterScene is one scene in a StashIDCluster, with its primary file's
// identity and duration.
type ClusterScene struct {
	SceneID  string
	Oshash   string // empty when the primary file carries no oshash
	Duration float64
	Size     int64
}

// SharedStashIDs groups live scenes by (endpoint, stash_id) where more than
// one scene shares the pair — the stash_id generator's raw signal.
func (l *Ledger) SharedStashIDs(ctx context.Context) ([]StashIDCluster, error) {
	rows, err := l.db.QueryContext(ctx, `
		WITH live AS (
			SELECT ssi.endpoint, ssi.stash_id, ssi.scene_id
			FROM scene_stash_id ssi
			JOIN scene s ON s.id = ssi.scene_id AND s.gone_run IS NULL
		),
		dup AS (
			SELECT endpoint, stash_id FROM live GROUP BY endpoint, stash_id HAVING COUNT(*) > 1
		)
		SELECT live.endpoint, live.stash_id, live.scene_id, f.oshash, f.duration, f.size
		FROM live
		JOIN dup ON dup.endpoint = live.endpoint AND dup.stash_id = live.stash_id
		LEFT JOIN file f ON f.scene_id = live.scene_id AND f.is_primary = 1 AND f.gone_run IS NULL
		ORDER BY live.endpoint, live.stash_id, live.scene_id`)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying shared stash_ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var clusters []StashIDCluster
	idx := -1
	for rows.Next() {
		var endpoint, stashID, sceneID string
		var oshash sql.NullString
		var duration sql.NullFloat64
		var size sql.NullInt64
		if err := rows.Scan(&endpoint, &stashID, &sceneID, &oshash, &duration, &size); err != nil {
			return nil, fmt.Errorf("ledger: scanning shared stash_id row: %w", err)
		}
		if idx == -1 || clusters[idx].Endpoint != endpoint || clusters[idx].StashID != stashID {
			clusters = append(clusters, StashIDCluster{Endpoint: endpoint, StashID: stashID})
			idx = len(clusters) - 1
		}
		clusters[idx].Scenes = append(clusters[idx].Scenes, ClusterScene{
			SceneID: sceneID, Oshash: oshash.String, Duration: duration.Float64, Size: size.Int64,
		})
	}
	return clusters, rows.Err()
}

// PrimaryFile is a scene's primary file, as `groups` and `plan` formatting
// quote it and as internal/keeper scores it.
type PrimaryFile struct {
	Path       string
	Basename   string
	Size       int64
	Width      int
	Height     int
	Duration   float64
	VideoCodec string
	BitRate    int64
}

// PrimaryFiles returns the primary file metadata for each of the given
// scene ids. A scene with no primary file row is simply absent from the map.
func (l *Ledger) PrimaryFiles(ctx context.Context, sceneIDs []string) (map[string]PrimaryFile, error) {
	out := make(map[string]PrimaryFile, len(sceneIDs))
	if len(sceneIDs) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(sceneIDs))
	args := make([]any, len(sceneIDs))
	for i, id := range sceneIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT scene_id, path, basename, size, width, height, duration, video_codec, bit_rate FROM file
		WHERE is_primary = 1 AND scene_id IN (%s)`, strings.Join(placeholders, ","))

	rows, err := l.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: querying primary files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var sceneID string
		var pf PrimaryFile
		if err := rows.Scan(&sceneID, &pf.Path, &pf.Basename, &pf.Size, &pf.Width, &pf.Height,
			&pf.Duration, &pf.VideoCodec, &pf.BitRate); err != nil {
			return nil, fmt.Errorf("ledger: scanning primary file row: %w", err)
		}
		out[sceneID] = pf
	}
	return out, rows.Err()
}
