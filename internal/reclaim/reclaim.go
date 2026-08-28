// Package reclaim applies a duplicate plan reversibly (REBUILD.md §4.4):
// back up, union the losers' metadata onto the keeper, tag each loser and
// move its file to quarantine. Nothing here deletes; `Prune` does that, much
// later.
package reclaim

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	stash "github.com/Anastylosis/stash-go"
)

// Defaults for Options, per the REBUILD.md §0 decisions table.
const (
	DefaultQuarantineDir = "/data/_quarantine"
	DefaultTag           = "_custodian_quarantine"
	// DefaultMistagTag marks every scene in a `mistag` group — REBUILD.md
	// §4.2: the fix is metadata, not deletion, so apply tags rather than
	// quarantining any of them.
	DefaultMistagTag = "_custodian_mistag"
)

// Options configures Apply. The zero value is a dry run with the defaults
// above.
type Options struct {
	// QuarantineDir is where losers' files are moved, in the server's path
	// notation — it has to be inside a Stash library path or moveFiles
	// refuses.
	QuarantineDir string
	// BackupDir is where the verified database backup lands before any
	// write. Empty falls back to ~/.local/share/custodian/backups.
	BackupDir string
	// Tag is put on every loser scene so quarantine can be reviewed, watched
	// and rescued from Stash's own UI.
	Tag string
	// MistagTag is put on every scene in a `mistag` group — no file moves,
	// just a tag: the group's problem is its metadata, not a duplicate to
	// resolve.
	MistagTag string
	// Commit sends the mutations. Without it nothing is written anywhere.
	Commit bool
	// Limit caps how many groups are acted on (0 = no limit).
	Limit int
	// Sidecars moves the subtitles beside a loser's video; nil skips them
	// and says so in the report.
	Sidecars *sidecar.Mover
}

func (o Options) withDefaults() Options {
	if o.QuarantineDir == "" {
		o.QuarantineDir = DefaultQuarantineDir
	}
	if o.Tag == "" {
		o.Tag = DefaultTag
	}
	if o.MistagTag == "" {
		o.MistagTag = DefaultMistagTag
	}
	if o.BackupDir == "" {
		o.BackupDir = DefaultBackupDir()
	}
	return o
}

// DefaultBackupDir is where verified backups land when nothing configures it.
func DefaultBackupDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "custodian", "backups")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "backups"
	}
	return filepath.Join(home, ".local", "share", "custodian", "backups")
}

// Apply quarantines the losers of every live `certain` or `partial` group
// that `plan` has chosen a keeper for, and tags every scene in a live
// `mistag` group, in the order REBUILD.md §4.4 lays down. Without
// Options.Commit it computes and reports the whole plan and sends nothing.
func Apply(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts Options) (Report, error) {
	if client == nil || l == nil {
		return Report{}, errors.New("reclaim: a Stash client and a ledger are both required")
	}
	opts = opts.withDefaults()

	rep := Report{DryRun: !opts.Commit, QuarantineDir: opts.QuarantineDir, Tag: opts.Tag, MistagTag: opts.MistagTag}
	rep.Backup.Dir = opts.BackupDir

	groups, err := plannedGroups(ctx, l, opts.Limit)
	if err != nil {
		return rep, err
	}
	files, err := l.PrimaryFiles(ctx, sceneIDsOf(groups))
	if err != nil {
		return rep, err
	}
	mistags, err := mistagGroups(ctx, l, opts.Limit)
	if err != nil {
		return rep, err
	}

	a := &applier{client: client, l: l, opts: opts, used: map[string]bool{}}
	if opts.Commit {
		runID, err := l.BeginRun(ctx, "apply", "")
		if err != nil {
			return rep, fmt.Errorf("reclaim: beginning apply run: %w", err)
		}
		a.runID, rep.RunID = runID, runID
	}

	err = a.run(ctx, &rep, groups, files, mistags)
	if a.runID != 0 {
		_ = l.FinishRun(ctx, a.runID, ledger.RunSummary{Notes: rep.Summary()})
	}
	return rep, err
}

// applier carries the state one Apply run accumulates: the run it logs
// under, the tags it ensured once, and the quarantine basenames it has
// already claimed.
type applier struct {
	client      *stash.Client
	l           *ledger.Ledger
	opts        Options
	runID       int64
	tagID       string
	mistagTagID string
	used        map[string]bool
}

func (a *applier) run(ctx context.Context, rep *Report, groups []ledger.Group, files map[string]ledger.PrimaryFile, mistags []ledger.Group) error {
	if err := a.backup(ctx, rep); err != nil {
		return err
	}
	for _, g := range groups {
		rep.Groups = append(rep.Groups, a.group(ctx, g, files))
	}
	for _, g := range mistags {
		rep.Mistags = append(rep.Mistags, a.mistag(ctx, g))
	}
	return nil
}

// backup is §4.4 step 1. A commit run that cannot take a verified backup
// stops here, before the first write.
func (a *applier) backup(ctx context.Context, rep *Report) error {
	if !a.opts.Commit {
		return nil
	}
	if err := os.MkdirAll(a.opts.BackupDir, 0o755); err != nil {
		return fmt.Errorf("reclaim: creating the backup directory %s: %w", a.opts.BackupDir, err)
	}
	id, err := a.l.AppendMutation(ctx, ledger.Mutation{
		RunID: a.runID, Kind: "backup", Target: a.opts.BackupDir, Note: "DownloadVerifiedBackup",
	})
	if err != nil {
		return err
	}
	// Never IncludeBlobs: with blobs on disk the server answers with a zip, which cannot be verified.
	m, err := a.client.DownloadVerifiedBackup(ctx, stash.BackupOptions{}, a.opts.BackupDir)
	if err != nil {
		_ = a.l.MarkMutation(ctx, id, false, err.Error())
		rep.Backup.Err = err.Error()
		return fmt.Errorf("reclaim: no verified backup, so nothing was written: %w", err)
	}
	rep.Backup.Taken = true
	rep.Backup.File = filepath.Join(a.opts.BackupDir, m.File)
	rep.Backup.Manifest = rep.Backup.File + stash.ManifestSuffix
	rep.Backup.Bytes = m.Bytes
	return a.l.MarkMutation(ctx, id, true, rep.Backup.Manifest)
}

// group runs §4.4 steps 2–5 over one candidate group. An error mid-group
// stops that group and is reported; the caller moves on to the next.
func (a *applier) group(ctx context.Context, g ledger.Group, files map[string]ledger.PrimaryFile) GroupReport {
	gr := GroupReport{Signature: g.Signature, Keeper: g.Keeper}

	fresh, reason, err := a.fetch(ctx, g, files)
	switch {
	case err != nil:
		gr.Skipped, gr.Reason = true, err.Error()
		return gr
	case reason != "":
		gr.Skipped, gr.Reason = true, reason
		return gr
	}

	if a.opts.Sidecars == nil {
		gr.Notes = append(gr.Notes,
			"sidecars: no library access configured, so subtitles beside a loser stay where they are")
	}

	if err := a.union(ctx, fresh.keeper, fresh.losers, &gr); err != nil {
		gr.Errs = append(gr.Errs, err.Error())
		return gr
	}

	for i := range fresh.losers {
		lr, err := a.quarantine(ctx, &fresh.losers[i])
		gr.Losers = append(gr.Losers, lr)
		gr.Bytes += lr.Bytes
		if err != nil {
			gr.Errs = append(gr.Errs, err.Error())
			return gr
		}
	}

	// §4.4 step 4. Losers stay separate scenes in this design, so the keeper
	// should still hold exactly its own file. Say so rather than act.
	if n := len(fresh.keeper.Files); n > 1 {
		gr.Notes = append(gr.Notes, fmt.Sprintf(
			"keeper %s holds %d files — apply did not cause that and does not touch it; run the multifile signal",
			fresh.keeper.ID, n))
	}

	if a.opts.Commit {
		if err := a.l.SetOutcome(ctx, g.Signature, ledger.VerdictQuarantined, a.runID); err != nil {
			gr.Errs = append(gr.Errs, err.Error())
			return gr
		}
	}
	gr.Done = true
	return gr
}

// freshGroup is one group as Stash reports it right now, not as the ledger
// remembers it.
type freshGroup struct {
	keeper *stash.Scene
	losers []stash.Scene
}

// fetch re-reads the keeper and every loser from Stash (§4.4 step 2's
// precondition). The ledger can be hours stale, and a scene that has since
// been deleted, emptied or moved is a group to leave alone, not to act on.
// A non-empty reason means "skip this group", and is not an error.
func (a *applier) fetch(ctx context.Context, g ledger.Group, files map[string]ledger.PrimaryFile) (freshGroup, string, error) {
	var out freshGroup
	if g.Keeper == "" {
		return out, "no keeper stored — run `custodian plan` first", nil
	}

	keeper, reason, err := a.scene(ctx, g.Keeper, files)
	if err != nil || reason != "" {
		return out, reason, err
	}
	out.keeper = keeper

	for _, id := range g.SceneIDs {
		if id == g.Keeper {
			continue
		}
		loser, reason, err := a.scene(ctx, id, files)
		if err != nil || reason != "" {
			return freshGroup{}, reason, err
		}
		out.losers = append(out.losers, *loser)
	}
	if len(out.losers) == 0 {
		return freshGroup{}, "no losers left in the group", nil
	}
	return out, "", nil
}

// scene fetches one scene and checks it against what the ledger recorded.
func (a *applier) scene(ctx context.Context, id string, files map[string]ledger.PrimaryFile) (*stash.Scene, string, error) {
	sc, found, err := a.client.FindScene(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, fmt.Sprintf("scene %s is gone from Stash since the last sync", id), nil
	}
	pf := sc.PrimaryFile()
	if pf == nil {
		return nil, fmt.Sprintf("scene %s has no file", id), nil
	}
	if known, ok := files[id]; ok && known.Path != "" && known.Path != pf.Path {
		return nil, fmt.Sprintf("scene %s moved since the last sync (%s → %s) — re-run `custodian sync`",
			id, known.Path, pf.Path), nil
	}
	return sc, "", nil
}

// union is §4.4 step 2: everything the losers know lands on the keeper
// before they are taken out of the way.
func (a *applier) union(ctx context.Context, keeper *stash.Scene, losers []stash.Scene, gr *GroupReport) error {
	update, conflicts := stash.Union(*keeper, losers, stash.DefaultUnionPolicy())
	step := &UnionStep{Scene: keeper.ID, Fields: unionFields(update)}
	for _, c := range conflicts {
		step.Conflicts = append(step.Conflicts,
			fmt.Sprintf("%s: kept %s, dropped %s", c.Endpoint, c.Kept, c.Dropped))
	}
	gr.Union = step

	if len(step.Fields) == 0 {
		return nil
	}
	update.ID = keeper.ID
	return a.do(ctx, ledger.Mutation{
		Kind: "union", Target: "scene " + keeper.ID,
		To: strings.Join(step.Fields, ", "), Note: "sceneUpdate",
	}, func() error { return a.client.UpdateScene(ctx, update) })
}

// quarantine is §4.4 step 3 for one loser: tag it, plan its sidecars, move
// its file, then move the sidecars after Stash confirms.
func (a *applier) quarantine(ctx context.Context, loser *stash.Scene) (LoserReport, error) {
	pf := loser.PrimaryFile()
	basename := a.quarantineName(pf.Basename, loser.ID)
	lr := LoserReport{
		SceneID: loser.ID, FileID: pf.ID, From: pf.Path, Bytes: pf.Size,
		To: pathmap.Join(a.opts.QuarantineDir, basename),
	}

	if err := a.tag(ctx, loser.ID); err != nil {
		return lr, err
	}
	lr.Tagged = true

	// Planned BEFORE the video moves: afterwards the source directory no
	// longer holds the context that tells competing videos apart.
	planned, err := a.opts.Sidecars.Plan(ctx, pf.Path, lr.To)
	if err != nil {
		lr.Notes = append(lr.Notes, fmt.Sprintf("could not inspect sidecars: %v", err))
	}

	if err := a.do(ctx, ledger.Mutation{
		Kind: "move", Target: "file " + pf.ID, From: pf.Path, To: lr.To, Note: "moveFiles",
	}, func() error {
		return a.client.MoveFiles(ctx, []string{pf.ID},
			stash.MoveTarget{Folder: a.opts.QuarantineDir, Basename: basename})
	}); err != nil {
		return lr, err
	}

	a.sidecars(ctx, planned, &lr)
	return lr, nil
}

// sidecars applies the planned companion-file moves, after the video's own
// move succeeded, and logs each one.
func (a *applier) sidecars(ctx context.Context, planned []sidecar.PlannedMove, lr *LoserReport) {
	for _, m := range planned {
		lr.Sidecars = append(lr.Sidecars, SidecarMove{From: m.SrcStash, To: m.DstStash})
	}
	if !a.opts.Commit || len(planned) == 0 {
		return
	}

	res := a.opts.Sidecars.Apply(ctx, planned)
	outcome := make(map[string]SidecarMove, len(planned))
	for _, m := range res.Moved {
		outcome[m.SrcStash] = SidecarMove{From: m.SrcStash, To: m.DstStash, Done: true}
	}
	for _, s := range res.Skipped {
		outcome[s.Move.SrcStash] = SidecarMove{From: s.Move.SrcStash, To: s.Move.DstStash, Note: s.Reason}
	}
	for _, f := range res.Failed {
		outcome[f.Move.SrcStash] = SidecarMove{From: f.Move.SrcStash, To: f.Move.DstStash, Note: f.Err.Error()}
	}

	for i, m := range lr.Sidecars {
		if got, ok := outcome[m.From]; ok {
			lr.Sidecars[i] = got
		}
		a.logSidecar(ctx, lr.Sidecars[i])
	}
}

func (a *applier) logSidecar(ctx context.Context, m SidecarMove) {
	id, err := a.l.AppendMutation(ctx, ledger.Mutation{
		RunID: a.runID, Kind: "sidecar", Target: pathmap.Base(m.From), From: m.From, To: m.To,
	})
	if err != nil {
		return
	}
	_ = a.l.MarkMutation(ctx, id, m.Done, m.Note)
}

// tag puts the quarantine tag on a loser, creating the tag once per run.
func (a *applier) tag(ctx context.Context, sceneID string) error {
	return a.do(ctx, ledger.Mutation{
		Kind: "tag", Target: "scene " + sceneID, To: a.opts.Tag, Note: "bulkSceneUpdate ADD",
	}, func() error {
		tagID, err := a.ensureTag(ctx)
		if err != nil {
			return err
		}
		return a.client.AddSceneTags(ctx, []string{tagID}, sceneID)
	})
}

func (a *applier) ensureTag(ctx context.Context) (string, error) {
	if a.tagID != "" {
		return a.tagID, nil
	}
	id, err := a.client.EnsureTag(ctx, a.opts.Tag)
	if err != nil {
		return "", fmt.Errorf("reclaim: ensuring tag %s: %w", a.opts.Tag, err)
	}
	a.tagID = id
	return id, nil
}

// quarantineName is the basename a loser's file takes in the quarantine
// directory, suffixed with the scene id when this run has already claimed
// that name — two performers' copies of one scene routinely share a
// filename.
func (a *applier) quarantineName(basename, sceneID string) string {
	name := basename
	if a.used[strings.ToLower(name)] {
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + "_" + sceneID + ext
	}
	a.used[strings.ToLower(name)] = true
	return name
}

// do sends one write, recording it in the mutation log before it goes and
// stamping the outcome after. A dry run sends nothing and logs nothing: the
// report it builds is the whole of its output.
func (a *applier) do(ctx context.Context, m ledger.Mutation, send func() error) error {
	if !a.opts.Commit {
		return nil
	}
	m.RunID = a.runID
	id, err := a.l.AppendMutation(ctx, m)
	if err != nil {
		return err
	}
	if err := send(); err != nil {
		_ = a.l.MarkMutation(ctx, id, false, err.Error())
		return err
	}
	return a.l.MarkMutation(ctx, id, true, "")
}

// plannedGroups returns the live `certain` and `partial` groups `plan` has
// chosen a keeper for, capped at limit. `partial` is treated exactly like
// `certain` here (REBUILD.md §4.2): its keeper is already the matching
// copy plan chose among the complete scenes, so the rest of Apply doesn't
// need to know which verdict got it there.
func plannedGroups(ctx context.Context, l *ledger.Ledger, limit int) ([]ledger.Group, error) {
	var all []ledger.Group
	for _, v := range []string{ledger.VerdictCertain, ledger.VerdictPartial} {
		gs, err := l.Groups(ctx, ledger.GroupFilter{Verdict: v})
		if err != nil {
			return nil, err
		}
		all = append(all, gs...)
	}
	out := make([]ledger.Group, 0, len(all))
	for _, g := range all {
		if g.Keeper == "" {
			continue
		}
		out = append(out, g)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// mistagGroups returns the live `mistag` groups, capped at limit. Unlike
// plannedGroups these have no keeper — every scene in the group gets the
// mistag tag, not just the losers.
func mistagGroups(ctx context.Context, l *ledger.Ledger, limit int) ([]ledger.Group, error) {
	all, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictMistag})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// mistag tags every scene in g with opts.MistagTag — no file move, no
// keeper: the group's problem is metadata, and the fix is the tag showing
// up for review in Stash's UI.
func (a *applier) mistag(ctx context.Context, g ledger.Group) MistagReport {
	mr := MistagReport{Signature: g.Signature, SceneIDs: g.SceneIDs, Reason: g.Reason}

	err := a.do(ctx, ledger.Mutation{
		Kind:   "tag",
		Target: fmt.Sprintf("group %s (%d scenes)", g.Signature, len(g.SceneIDs)),
		To:     a.opts.MistagTag, Note: "bulkSceneUpdate ADD",
	}, func() error {
		tagID, err := a.ensureMistagTag(ctx)
		if err != nil {
			return err
		}
		return a.client.AddSceneTags(ctx, []string{tagID}, g.SceneIDs...)
	})
	if err != nil {
		mr.Err = err.Error()
		return mr
	}
	mr.Tagged = true
	return mr
}

func (a *applier) ensureMistagTag(ctx context.Context) (string, error) {
	if a.mistagTagID != "" {
		return a.mistagTagID, nil
	}
	id, err := a.client.EnsureTag(ctx, a.opts.MistagTag)
	if err != nil {
		return "", fmt.Errorf("reclaim: ensuring tag %s: %w", a.opts.MistagTag, err)
	}
	a.mistagTagID = id
	return id, nil
}

func sceneIDsOf(groups []ledger.Group) []string {
	var ids []string
	for _, g := range groups {
		ids = append(ids, g.SceneIDs...)
	}
	return ids
}

// unionFields names the fields a SceneUpdate would write, so the plan can
// quote them without re-deriving the union.
func unionFields(u stash.SceneUpdate) []string {
	var f []string
	for _, field := range []struct {
		name string
		set  bool
	}{
		{"title", u.Title != nil},
		{"code", u.Code != nil},
		{"date", u.Date != nil},
		{"details", u.Details != nil},
		{"director", u.Director != nil},
		{"studio_id", u.StudioID != nil},
		{"rating100", u.Rating100 != nil},
		{"organized", u.Organized != nil},
		{"urls", len(u.URLs) > 0},
		{"tag_ids", len(u.TagIDs) > 0},
		{"performer_ids", len(u.PerformerIDs) > 0},
		{"gallery_ids", len(u.GalleryIDs) > 0},
		{"stash_ids", len(u.StashIDs) > 0},
	} {
		if field.set {
			f = append(f, field.name)
		}
	}
	return f
}
