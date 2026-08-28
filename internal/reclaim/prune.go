package reclaim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// DefaultAge is how long a quarantined group waits before Prune will touch
// it (REBUILD.md §0: "prune merges and deletes after 30 days").
const DefaultAge = 30 * 24 * time.Hour

// PruneOptions configures Prune. The zero value is a dry run over anything
// quarantined more than DefaultAge ago.
type PruneOptions struct {
	Age       time.Duration
	BackupDir string
	Tag       string
	Commit    bool
	Limit     int
}

func (o PruneOptions) withDefaults() PruneOptions {
	if o.Age == 0 {
		o.Age = DefaultAge
	}
	if o.Tag == "" {
		o.Tag = DefaultTag
	}
	if o.BackupDir == "" {
		o.BackupDir = DefaultBackupDir()
	}
	return o
}

// PruneReport is what one Prune run did, or would have done.
type PruneReport struct {
	DryRun bool
	RunID  int64
	Age    time.Duration
	Tag    string
	Backup BackupStep
	Groups []PruneGroup
	// ChangesFile is where the append-only changes JSON landed, beside the
	// backup. Prune is not undoable — the verified backup is its reversal —
	// but the entry still records what was merged and deleted.
	ChangesFile string
	ChangesErr  string
}

// PruneGroup is one quarantined group's outcome: the merge that folded its
// losers into the keeper and the delete that freed their bytes.
type PruneGroup struct {
	Signature string
	Keeper    string
	AppliedAt string
	Skipped   bool
	Reason    string
	Rescued   []string
	Losers    []PruneLoser
	// Multifile marks a group whose losers are files of the SAME scene as
	// Keeper (REBUILD.md's Phase 5b) — no sceneMerge, just deleteFiles.
	Multifile bool
	Errs      []string
	Bytes     int64
	Done      bool
}

// PruneLoser is one loser scene about to be merged away and its file deleted.
type PruneLoser struct {
	SceneID string
	FileID  string
	Path    string
	Bytes   int64
}

// Counts summarises a PruneReport: groups reclaimed, skipped, failed, and the
// bytes freed.
func (r PruneReport) Counts() (done, skipped, failed int, bytes int64) {
	for _, g := range r.Groups {
		switch {
		case g.Skipped:
			skipped++
		case len(g.Errs) > 0:
			failed++
		default:
			done++
			bytes += g.Bytes
		}
	}
	return done, skipped, failed, bytes
}

// Summary is the one-line form recorded as the prune run's notes.
func (r PruneReport) Summary() string {
	done, skipped, failed, bytes := r.Counts()
	return fmt.Sprintf("reclaimed %d, skipped %d, failed %d, %s",
		done, skipped, failed, confirm.HumanBytes(bytes))
}

// String renders the report as the plain text `custodian prune` prints.
func (r PruneReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== custodian prune (%s) ===\n", mode(r.DryRun))
	fmt.Fprintf(&b, "age:        older than %s\n", formatAge(r.Age))
	fmt.Fprintf(&b, "tag:        %s\n", r.Tag)
	writeBackup(&b, r.Backup, r.DryRun)
	b.WriteString("\n")

	for _, g := range r.Groups {
		writePruneGroup(&b, g, r.DryRun)
	}

	done, skipped, failed, bytes := r.Counts()
	verb := "reclaimed"
	if r.DryRun {
		verb = "would reclaim"
	}
	fmt.Fprintf(&b, "%s: %d groups, %s freed\n", verb, done, confirm.HumanBytes(bytes))
	fmt.Fprintf(&b, "skipped:   %d groups\n", skipped)
	fmt.Fprintf(&b, "failed:    %d groups\n", failed)
	switch {
	case r.ChangesErr != "":
		fmt.Fprintf(&b, "\nchanges JSON: FAILED — %s\n", r.ChangesErr)
	case r.ChangesFile != "":
		fmt.Fprintf(&b, "\nchanges JSON: %s\n", r.ChangesFile)
	}
	if r.DryRun {
		b.WriteString("\nDRY RUN — nothing was merged, nothing was deleted, and the mutation\n")
		b.WriteString("log is untouched. Re-run with --commit to act on this plan.\n")
	}
	return b.String()
}

// formatAge renders a retention window the way it is written on the command
// line: whole days as "30d", anything else as Go's own duration.
func formatAge(d time.Duration) string {
	const day = 24 * time.Hour
	if d >= day && d%day == 0 {
		return fmt.Sprintf("%dd", d/day)
	}
	return d.String()
}

func writePruneGroup(b *strings.Builder, g PruneGroup, dryRun bool) {
	fmt.Fprintf(b, "group %s keeper %s (quarantined %s)\n", shortSignature(g.Signature), g.Keeper, g.AppliedAt)
	if g.Skipped {
		fmt.Fprintf(b, "  SKIPPED: %s\n\n", g.Reason)
		return
	}
	for _, id := range g.Rescued {
		fmt.Fprintf(b, "  rescued: scene %s no longer carries the tag\n", id)
	}
	ids := make([]string, 0, len(g.Losers))
	for _, l := range g.Losers {
		ids = append(ids, l.SceneID)
		fmt.Fprintf(b, "  delete:  %s (%s)\n", l.Path, confirm.HumanBytes(l.Bytes))
	}
	switch {
	case g.Multifile && len(g.Losers) > 0:
		fmt.Fprintf(b, "  %s — same scene, no merge needed\n", verb(dryRun, "deleteFiles", "would deleteFiles"))
	case len(ids) > 0:
		fmt.Fprintf(b, "  merge:   %s %s → %s\n",
			verb(dryRun, "sceneMerge", "would sceneMerge"), strings.Join(ids, ", "), g.Keeper)
	}
	for _, e := range g.Errs {
		fmt.Fprintf(b, "  ERROR: %s\n", e)
	}
	fmt.Fprintf(b, "  freed:   %s\n\n", confirm.HumanBytes(g.Bytes))
}

// Prune is §4.4 step 5, the only irreversible one: for every quarantined
// group older than Age whose losers still carry the tag, merge them into the
// keeper — which carries their play history across — and delete their files.
// A loser someone un-tagged has been rescued, and its group is left alone.
func Prune(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts PruneOptions) (PruneReport, error) {
	if client == nil || l == nil {
		return PruneReport{}, errors.New("reclaim: a Stash client and a ledger are both required")
	}
	opts = opts.withDefaults()

	rep := PruneReport{DryRun: !opts.Commit, Age: opts.Age, Tag: opts.Tag}
	rep.Backup.Dir = opts.BackupDir

	groups, err := l.Groups(ctx, ledger.GroupFilter{
		Verdict:       ledger.VerdictQuarantined,
		AppliedBefore: time.Now().Add(-opts.Age),
	})
	if err != nil {
		return rep, err
	}
	if opts.Limit > 0 && len(groups) > opts.Limit {
		groups = groups[:opts.Limit]
	}

	p := &pruner{client: client, l: l, opts: opts}
	if opts.Commit {
		runID, err := l.BeginRun(ctx, "prune", "")
		if err != nil {
			return rep, fmt.Errorf("reclaim: beginning prune run: %w", err)
		}
		p.runID, rep.RunID = runID, runID
	}

	err = p.run(ctx, &rep, groups)
	if p.runID != 0 {
		_ = l.FinishRun(ctx, p.runID, ledger.RunSummary{Notes: rep.Summary()})
	}
	return rep, err
}

type pruner struct {
	client *stash.Client
	l      *ledger.Ledger
	opts   PruneOptions
	runID  int64
}

func (p *pruner) run(ctx context.Context, rep *PruneReport, groups []ledger.Group) error {
	if err := p.backup(ctx, rep); err != nil {
		return err
	}
	for _, g := range groups {
		if IsMultifileGroup(g) {
			rep.Groups = append(rep.Groups, p.multifileGroup(ctx, g))
			continue
		}
		rep.Groups = append(rep.Groups, p.group(ctx, g))
	}
	p.writeChanges(rep)
	return nil
}

// writeChanges appends this run's entries to <backup file>.changes.json, the
// same file Apply writes to. Prune is not undoable — the verified backup is
// its reversal — but the record still says what merged and what was deleted.
func (p *pruner) writeChanges(rep *PruneReport) {
	if !p.opts.Commit || !rep.Backup.Taken {
		return
	}
	path := rep.Backup.File + ".changes.json"
	entries := pruneChangeEntries(*rep, time.Now().UTC().Format(time.RFC3339))
	if err := writeChanges(path, entries); err != nil {
		rep.ChangesErr = err.Error()
		return
	}
	if len(entries) > 0 {
		rep.ChangesFile = path
	}
}

// backup is the same gate Apply has: a delete without a verified backup does
// not happen.
func (p *pruner) backup(ctx context.Context, rep *PruneReport) error {
	a := &applier{client: p.client, l: p.l, runID: p.runID,
		opts: Options{Commit: p.opts.Commit, BackupDir: p.opts.BackupDir}}
	inner := Report{}
	inner.Backup.Dir = p.opts.BackupDir
	err := a.backup(ctx, &inner)
	rep.Backup = inner.Backup
	return err
}

func (p *pruner) group(ctx context.Context, g ledger.Group) PruneGroup {
	pg := PruneGroup{Signature: g.Signature, Keeper: g.Keeper, AppliedAt: g.AppliedAt}

	keeper, found, err := p.client.FindScene(ctx, g.Keeper)
	switch {
	case err != nil:
		pg.Skipped, pg.Reason = true, err.Error()
		return pg
	case !found:
		pg.Skipped, pg.Reason = true, fmt.Sprintf("keeper %s is gone from Stash", g.Keeper)
		return pg
	}

	if reason := p.collectLosers(ctx, g, &pg); reason != "" {
		pg.Skipped, pg.Reason = true, reason
		return pg
	}
	if len(pg.Losers) == 0 {
		pg.Skipped, pg.Reason = true, "nothing left to reclaim — the losers are already gone"
		return pg
	}

	if err := p.mergeAndDelete(ctx, keeper.ID, &pg); err != nil {
		pg.Errs = append(pg.Errs, err.Error())
		return pg
	}

	if p.opts.Commit {
		if err := p.l.SetOutcome(ctx, g.Signature, ledger.VerdictReclaimed, p.runID); err != nil {
			pg.Errs = append(pg.Errs, err.Error())
			return pg
		}
	}
	pg.Done = true
	return pg
}

// collectLosers re-reads every loser and checks it still carries the tag. A
// rescued one aborts the whole group: half a merge is not a state anybody
// asked for.
func (p *pruner) collectLosers(ctx context.Context, g ledger.Group, pg *PruneGroup) string {
	for _, id := range g.SceneIDs {
		if id == g.Keeper {
			continue
		}
		sc, found, err := p.client.FindScene(ctx, id)
		if err != nil {
			return err.Error()
		}
		if !found {
			continue // already reclaimed, or deleted by hand
		}
		if !hasTag(sc, p.opts.Tag) {
			pg.Rescued = append(pg.Rescued, id)
			continue
		}
		pf := sc.PrimaryFile()
		if pf == nil {
			return fmt.Sprintf("loser %s has no file left", id)
		}
		pg.Losers = append(pg.Losers, PruneLoser{
			SceneID: id, FileID: pf.ID, Path: pf.Path, Bytes: pf.Size,
		})
		pg.Bytes += pf.Size
	}
	if len(pg.Rescued) > 0 {
		return fmt.Sprintf("rescued: scene(s) %s no longer carry %s",
			strings.Join(pg.Rescued, ", "), p.opts.Tag)
	}
	return ""
}

// mergeAndDelete folds the losers into the keeper and then deletes their
// files, in that order: merging first moves the files onto the keeper, so
// deleting them afterwards leaves no scene pointing at a missing video.
func (p *pruner) mergeAndDelete(ctx context.Context, keeperID string, pg *PruneGroup) error {
	sceneIDs := make([]string, 0, len(pg.Losers))
	fileIDs := make([]string, 0, len(pg.Losers))
	for _, l := range pg.Losers {
		sceneIDs = append(sceneIDs, l.SceneID)
		fileIDs = append(fileIDs, l.FileID)
	}

	a := &applier{l: p.l, runID: p.runID, opts: Options{Commit: p.opts.Commit}}
	if err := a.do(ctx, ledger.Mutation{
		Kind: "merge", Target: "scene " + keeperID,
		From: strings.Join(sceneIDs, ", "), To: keeperID, Note: "sceneMerge, play+o history",
	}, func() error {
		return p.client.MergeScenes(ctx, keeperID, sceneIDs, nil,
			stash.MergeOptions{PlayHistory: true, OHistory: true})
	}); err != nil {
		return err
	}

	return a.do(ctx, ledger.Mutation{
		Kind: "delete", Target: strings.Join(fileIDs, ", "),
		From: pathsOf(pg.Losers), Note: "deleteFiles — removes the videos from disk",
	}, func() error { return p.client.DeleteFiles(ctx, fileIDs...) })
}

// multifileGroup is Prune's branch for a group whose losers are files of the
// same scene as its keeper (REBUILD.md's Phase 5b, "make prune branch on
// this shape"): no sceneMerge — there is only one scene — just deleteFiles
// on whatever the applying run's mutation log shows it moved into
// quarantine and this scene still holds.
func (p *pruner) multifileGroup(ctx context.Context, g ledger.Group) PruneGroup {
	pg := PruneGroup{Signature: g.Signature, Multifile: true}
	if len(g.SceneIDs) != 1 {
		pg.Skipped, pg.Reason = true, fmt.Sprintf("multifile group has %d scenes, want 1", len(g.SceneIDs))
		return pg
	}
	sceneID := g.SceneIDs[0]
	pg.Keeper, pg.AppliedAt = sceneID, g.AppliedAt

	sc, found, err := p.client.FindScene(ctx, sceneID)
	switch {
	case err != nil:
		pg.Skipped, pg.Reason = true, err.Error()
		return pg
	case !found:
		pg.Skipped, pg.Reason = true, fmt.Sprintf("scene %s is gone from Stash", sceneID)
		return pg
	}

	rows, err := p.l.MutationsByRun(ctx, g.AppliedRun)
	if err != nil {
		pg.Skipped, pg.Reason = true, err.Error()
		return pg
	}
	for _, f := range sc.Files {
		if _, ok := findRow(rows, "move", "file "+f.ID); !ok {
			continue // the file this group kept as primary — never moved
		}
		pg.Losers = append(pg.Losers, PruneLoser{SceneID: sceneID, FileID: f.ID, Path: f.Path, Bytes: f.Size})
		pg.Bytes += f.Size
	}
	if len(pg.Losers) == 0 {
		pg.Skipped, pg.Reason = true, "nothing left to reclaim — the losers are already gone"
		return pg
	}

	fileIDs := make([]string, len(pg.Losers))
	for i, l := range pg.Losers {
		fileIDs[i] = l.FileID
	}
	a := &applier{l: p.l, runID: p.runID, opts: Options{Commit: p.opts.Commit}}
	if err := a.do(ctx, ledger.Mutation{
		Kind: "delete", Target: strings.Join(fileIDs, ", "),
		From: pathsOf(pg.Losers), Note: "deleteFiles — removes the videos from disk",
	}, func() error { return p.client.DeleteFiles(ctx, fileIDs...) }); err != nil {
		pg.Errs = append(pg.Errs, err.Error())
		return pg
	}

	if p.opts.Commit {
		if err := p.l.SetOutcome(ctx, g.Signature, ledger.VerdictReclaimed, p.runID); err != nil {
			pg.Errs = append(pg.Errs, err.Error())
			return pg
		}
	}
	pg.Done = true
	return pg
}

func pathsOf(losers []PruneLoser) string {
	out := make([]string, 0, len(losers))
	for _, l := range losers {
		out = append(out, l.Path)
	}
	return strings.Join(out, ", ")
}

func hasTag(sc *stash.Scene, name string) bool {
	for _, t := range sc.Tags {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}
