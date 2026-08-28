package reclaim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	stash "github.com/Anastylosis/stash-go"
)

// UndoOptions configures Undo. Exactly one of Signature or RunID selects
// what to undo. The zero value (plus one of those two) is a dry run.
type UndoOptions struct {
	// Signature undoes the one group with this signature.
	Signature string
	// RunID undoes every group `apply` quarantined under this run id —
	// everything `custodian apply` did in one invocation.
	RunID int64
	// Commit sends the mutations. Without it nothing is written anywhere.
	Commit bool
	// Sidecars moves subtitles back beside a restored video; nil skips them
	// and says so per loser.
	Sidecars *sidecar.Mover
}

// UndoReport is what one Undo run did, or would have done.
type UndoReport struct {
	DryRun bool
	RunID  int64
	Groups []UndoGroupReport
}

// UndoGroupReport is one group's reversal: the keeper fields restored, each
// loser untagged and moved back, and the verdict it returned to.
type UndoGroupReport struct {
	Signature       string
	Keeper          string
	Skipped         bool
	Reason          string
	KeeperFields    []string
	KeeperNotes     []string
	Losers          []UndoLoserReport
	RestoredVerdict string
	Errs            []string
	Done            bool
}

// UndoLoserReport is one loser scene undone: the tag removed, its file and
// sidecars moved back to where they started.
type UndoLoserReport struct {
	SceneID   string
	FileID    string
	Untagged  bool
	MovedBack bool
	// From is where the file sits now (in quarantine); To is where it is
	// restored to (its recorded original path).
	From     string
	To       string
	Sidecars []SidecarMove
	Notes    []string
	Errs     []string
}

// Counts summarises an UndoReport: groups restored, skipped, and failed.
func (r UndoReport) Counts() (done, skipped, failed int) {
	for _, g := range r.Groups {
		switch {
		case g.Skipped:
			skipped++
		case len(g.Errs) > 0:
			failed++
		default:
			done++
		}
	}
	return done, skipped, failed
}

// Summary is the one-line form recorded as the undo run's notes.
func (r UndoReport) Summary() string {
	done, skipped, failed := r.Counts()
	return fmt.Sprintf("restored %d, skipped %d, failed %d", done, skipped, failed)
}

// String renders the report as the plain text `custodian undo` prints.
func (r UndoReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== custodian undo (%s) ===\n\n", mode(r.DryRun))

	for _, g := range r.Groups {
		writeUndoGroup(&b, g, r.DryRun)
	}

	done, skipped, failed := r.Counts()
	verb := "restored"
	if r.DryRun {
		verb = "would restore"
	}
	fmt.Fprintf(&b, "%s: %d groups\n", verb, done)
	fmt.Fprintf(&b, "skipped:  %d groups\n", skipped)
	fmt.Fprintf(&b, "failed:   %d groups\n", failed)
	if r.DryRun {
		b.WriteString("\nDRY RUN — nothing was sent to Stash, nothing moved on disk, and the\n")
		b.WriteString("mutation log is untouched. Re-run with --commit to act on this plan.\n")
	}
	return b.String()
}

func writeUndoGroup(b *strings.Builder, g UndoGroupReport, dryRun bool) {
	fmt.Fprintf(b, "group %s keeper %s\n", shortSignature(g.Signature), g.Keeper)
	if g.Skipped {
		fmt.Fprintf(b, "  SKIPPED: %s\n\n", g.Reason)
		return
	}
	for _, l := range g.Losers {
		fmt.Fprintf(b, "  loser %s\n", l.SceneID)
		fmt.Fprintf(b, "    untag:   %s\n", verb(dryRun, "removed", "would remove"))
		fmt.Fprintf(b, "    move:    %s → %s\n", l.From, l.To)
		for _, s := range l.Sidecars {
			note := s.Note
			if note == "" && !s.Done {
				note = "planned"
			}
			fmt.Fprintf(b, "    sidecar: %s → %s (%s)\n", s.From, s.To, note)
		}
		for _, n := range l.Notes {
			fmt.Fprintf(b, "    note:    %s\n", n)
		}
		for _, e := range l.Errs {
			fmt.Fprintf(b, "    ERROR:   %s\n", e)
		}
	}
	if len(g.KeeperFields) > 0 {
		fmt.Fprintf(b, "  keeper:  %s %s\n", verb(dryRun, "restored", "would restore"), strings.Join(g.KeeperFields, ", "))
	}
	for _, n := range g.KeeperNotes {
		fmt.Fprintf(b, "  note:    %s\n", n)
	}
	if g.RestoredVerdict != "" {
		fmt.Fprintf(b, "  verdict: %s %s\n", verb(dryRun, "restored to", "would restore to"), g.RestoredVerdict)
	}
	for _, e := range g.Errs {
		fmt.Fprintf(b, "  ERROR: %s\n", e)
	}
	b.WriteString("\n")
}

// Undo reverses a live `quarantined` group's apply: untags each loser, moves
// its file and sidecars back, and restores the keeper's recorded pre-union
// values — read from the mutation log the applying run wrote, never from
// Stash's current state. Dry-run default.
func Undo(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts UndoOptions) (UndoReport, error) {
	if client == nil || l == nil {
		return UndoReport{}, errors.New("reclaim: a Stash client and a ledger are both required")
	}
	if (opts.Signature == "") == (opts.RunID == 0) {
		return UndoReport{}, errors.New("reclaim: Undo needs exactly one of Signature or RunID")
	}

	rep := UndoReport{DryRun: !opts.Commit}

	groups, err := undoGroups(ctx, l, opts)
	if err != nil {
		return rep, err
	}
	if len(groups) == 0 {
		return rep, fmt.Errorf("reclaim: no quarantined group found for %s", undoTarget(opts))
	}

	u := &undoer{client: client, l: l, opts: opts}
	if opts.Commit {
		runID, err := l.BeginRun(ctx, "undo", "")
		if err != nil {
			return rep, fmt.Errorf("reclaim: beginning undo run: %w", err)
		}
		u.runID, rep.RunID = runID, runID
	}

	for _, g := range groups {
		rep.Groups = append(rep.Groups, u.group(ctx, g))
	}
	if u.runID != 0 {
		_ = l.FinishRun(ctx, u.runID, ledger.RunSummary{Notes: rep.Summary()})
	}
	return rep, nil
}

func undoGroups(ctx context.Context, l *ledger.Ledger, opts UndoOptions) ([]ledger.Group, error) {
	if opts.Signature != "" {
		g, found, err := l.GroupBySignature(ctx, opts.Signature)
		if err != nil || !found {
			return nil, err
		}
		return []ledger.Group{g}, nil
	}
	return l.GroupsByAppliedRun(ctx, opts.RunID)
}

func undoTarget(opts UndoOptions) string {
	if opts.Signature != "" {
		return "signature " + opts.Signature
	}
	return fmt.Sprintf("run %d", opts.RunID)
}

// undoer carries the state one Undo run accumulates: the run it logs under
// and the quarantine tag id it has already resolved.
type undoer struct {
	client *stash.Client
	l      *ledger.Ledger
	opts   UndoOptions
	runID  int64
	tagID  string
}

// group reverses one candidate group, in the order REBUILD.md §4.4's
// Reversal paragraph lists: untag, move, sidecars — then, last, the keeper's
// fields, since union ran first in apply and so undoes last.
func (u *undoer) group(ctx context.Context, g ledger.Group) UndoGroupReport {
	gr := UndoGroupReport{Signature: g.Signature, Keeper: g.Keeper}

	if g.Verdict != ledger.VerdictQuarantined {
		gr.Skipped, gr.Reason = true, fmt.Sprintf("verdict is %q, not quarantined — refusing to undo", g.Verdict)
		return gr
	}
	if g.AppliedRun == 0 {
		gr.Skipped, gr.Reason = true, "no applying run is recorded for this group"
		return gr
	}

	rows, err := u.l.MutationsByRun(ctx, g.AppliedRun)
	if err != nil {
		gr.Errs = append(gr.Errs, err.Error())
		return gr
	}

	for _, id := range loserIDsOf(g) {
		gr.Losers = append(gr.Losers, u.loser(ctx, rows, id))
	}
	for _, lr := range gr.Losers {
		gr.Errs = append(gr.Errs, lr.Errs...)
	}

	if unionRow, ok := findRow(rows, "union", "scene "+g.Keeper); ok {
		restored, notes, err := u.restoreKeeper(ctx, g.Keeper, unionRow)
		gr.KeeperFields, gr.KeeperNotes = restored, notes
		if err != nil {
			gr.Errs = append(gr.Errs, err.Error())
		}
	} else {
		gr.KeeperNotes = append(gr.KeeperNotes, "no union mutation was logged — the keeper needed nothing restored")
	}

	if len(gr.Errs) > 0 {
		return gr
	}

	gr.RestoredVerdict = restoredVerdict(g.Steps)
	if u.opts.Commit {
		if err := u.l.SetOutcome(ctx, g.Signature, gr.RestoredVerdict, 0); err != nil {
			gr.Errs = append(gr.Errs, err.Error())
			return gr
		}
	}
	gr.Done = true
	return gr
}

// loser reverses one loser scene: remove its tag, move its file back to the
// path the apply run's move mutation recorded, and move its sidecars with
// it. The move mutation is found by the loser's current file id, which
// moveFiles never changes — only its path and basename do.
func (u *undoer) loser(ctx context.Context, rows []ledger.Mutation, sceneID string) UndoLoserReport {
	lr := UndoLoserReport{SceneID: sceneID}

	tagRow, ok := findRow(rows, "tag", "scene "+sceneID)
	if !ok {
		lr.Errs = append(lr.Errs, "no tag mutation logged for this loser in that run")
		return lr
	}

	sc, found, err := u.client.FindScene(ctx, sceneID)
	switch {
	case err != nil:
		lr.Errs = append(lr.Errs, err.Error())
		return lr
	case !found:
		lr.Errs = append(lr.Errs, "scene is gone from Stash since it was quarantined")
		return lr
	}
	pf := sc.PrimaryFile()
	if pf == nil {
		lr.Errs = append(lr.Errs, "scene has no file")
		return lr
	}
	lr.FileID = pf.ID

	move, ok := findRow(rows, "move", "file "+pf.ID)
	if !ok {
		lr.Errs = append(lr.Errs, "no move mutation logged for this loser's current file")
		return lr
	}
	lr.From, lr.To = pf.Path, move.From
	if pf.Path != move.To {
		lr.Notes = append(lr.Notes, fmt.Sprintf(
			"file sits at %s, not where quarantine left it (%s) — restoring it anyway", pf.Path, move.To))
	}

	if err := u.untag(ctx, sceneID, tagRow.To); err != nil {
		lr.Errs = append(lr.Errs, err.Error())
	} else {
		lr.Untagged = true
	}

	if err := u.checkFree(ctx, move.From, sceneID); err != nil {
		lr.Errs = append(lr.Errs, err.Error())
		return lr
	}

	// Planned BEFORE the video moves back, mirroring Apply: afterwards the
	// quarantine directory no longer holds the context to tell competing
	// videos' sidecars apart.
	planned, planErr := u.opts.Sidecars.Plan(ctx, pf.Path, move.From)
	if planErr != nil {
		lr.Notes = append(lr.Notes, fmt.Sprintf("could not inspect sidecars: %v", planErr))
	}
	if u.opts.Sidecars == nil {
		lr.Notes = append(lr.Notes, "sidecars: no library access configured, so subtitles beside this file stay where they are")
	}

	if err := u.do(ctx, ledger.Mutation{
		Kind: "undo:move", Target: "file " + pf.ID, From: pf.Path, To: move.From, Note: "moveFiles",
	}, func() error {
		return u.client.MoveFiles(ctx, []string{pf.ID},
			stash.MoveTarget{Folder: pathmap.Dir(move.From), Basename: pathmap.Base(move.From)})
	}); err != nil {
		lr.Errs = append(lr.Errs, err.Error())
		return lr
	}
	lr.MovedBack = true

	u.sidecarsBack(ctx, planned, &lr)
	return lr
}

// checkFree refuses the restore when a different file already occupies the
// loser's original path — REBUILD.md §4.4's Reversal paragraph. Best-effort:
// a lookup failure does not block the restore, since it would otherwise turn
// a transient Stash error into data left stranded in quarantine.
func (u *undoer) checkFree(ctx context.Context, originalPath, sceneID string) error {
	scenes, _, err := u.client.FindScenes(ctx, stash.SceneFilter{PathContains: originalPath}, 1, 50)
	if err != nil {
		return nil
	}
	for _, sc := range scenes {
		if sc.ID == sceneID {
			continue
		}
		for _, f := range sc.Files {
			if f.Path == originalPath {
				return fmt.Errorf("refusing to move: scene %s already has a file at %s", sc.ID, originalPath)
			}
		}
	}
	return nil
}

func (u *undoer) untag(ctx context.Context, sceneID, tagName string) error {
	return u.do(ctx, ledger.Mutation{
		Kind: "undo:tag", Target: "scene " + sceneID, From: tagName, Note: "bulkSceneUpdate REMOVE",
	}, func() error {
		tagID, err := u.ensureTag(ctx, tagName)
		if err != nil {
			return err
		}
		return u.client.RemoveSceneTags(ctx, []string{tagID}, sceneID)
	})
}

func (u *undoer) ensureTag(ctx context.Context, name string) (string, error) {
	if u.tagID != "" {
		return u.tagID, nil
	}
	id, err := u.client.EnsureTag(ctx, name)
	if err != nil {
		return "", fmt.Errorf("reclaim: resolving tag %s: %w", name, err)
	}
	u.tagID = id
	return id, nil
}

// sidecarsBack applies the planned companion-file moves after the video's
// own move-back succeeded, and logs each one — the same shape applier.
// sidecars uses forward.
func (u *undoer) sidecarsBack(ctx context.Context, planned []sidecar.PlannedMove, lr *UndoLoserReport) {
	for _, m := range planned {
		lr.Sidecars = append(lr.Sidecars, SidecarMove{From: m.SrcStash, To: m.DstStash})
	}
	if !u.opts.Commit || len(planned) == 0 {
		return
	}

	res := u.opts.Sidecars.Apply(ctx, planned)
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
		u.logSidecar(ctx, lr.Sidecars[i])
	}
}

func (u *undoer) logSidecar(ctx context.Context, m SidecarMove) {
	id, err := u.l.AppendMutation(ctx, ledger.Mutation{
		RunID: u.runID, Kind: "undo:sidecar", Target: pathmap.Base(m.From), From: m.From, To: m.To,
	})
	if err != nil {
		return
	}
	_ = u.l.MarkMutation(ctx, id, m.Done, m.Note)
}

// restoreKeeper decodes the union mutation's before/after JSON and writes
// back only the fields whose current value still matches what the union
// left (§4.4's Reversal paragraph); anything edited since is skipped with a
// note, never overwritten.
func (u *undoer) restoreKeeper(ctx context.Context, keeperID string, unionRow ledger.Mutation) ([]string, []string, error) {
	var before, after fieldSnapshot
	if err := json.Unmarshal([]byte(unionRow.From), &before); err != nil {
		return nil, nil, fmt.Errorf("reclaim: decoding union before-state: %w", err)
	}
	if err := json.Unmarshal([]byte(unionRow.To), &after); err != nil {
		return nil, nil, fmt.Errorf("reclaim: decoding union after-state: %w", err)
	}

	sc, found, err := u.client.FindScene(ctx, keeperID)
	if err != nil {
		return nil, nil, err
	}
	if !found {
		return nil, nil, fmt.Errorf("keeper %s is gone from Stash", keeperID)
	}

	update, clearFields, restored, notes := diffRestore(sc, before, after)

	if len(restored) > len(clearFields) {
		update.ID = keeperID
		if err := u.do(ctx, ledger.Mutation{
			Kind: "undo:union", Target: "scene " + keeperID,
			To: strings.Join(restored, ", "), Note: "sceneUpdate",
		}, func() error { return u.client.UpdateScene(ctx, update) }); err != nil {
			return restored, notes, err
		}
	}
	if len(clearFields) > 0 {
		if err := u.do(ctx, ledger.Mutation{
			Kind: "undo:union", Target: "scene " + keeperID,
			To: strings.Join(clearFields, ", "), Note: "clearSceneFields",
		}, func() error { return u.client.ClearSceneFields(ctx, keeperID, clearFields...) }); err != nil {
			return restored, notes, err
		}
	}

	return restored, notes, nil
}

// diffRestore compares the keeper's current values against what the union
// left (after) and builds the SceneUpdate that puts back what it found
// (before). A list field whose recorded before-value is empty cannot be
// expressed through SceneUpdate — it omits empty fields so a partial update
// stays non-destructive — so its name goes in clearFields for
// ClearSceneFields instead, which always sends the field.
func diffRestore(cur *stash.Scene, before, after fieldSnapshot) (u stash.SceneUpdate, clearFields, restored, notes []string) {
	str := func(name, curVal string, afterVal, beforeVal *string, set func(*string)) {
		if afterVal == nil {
			return
		}
		if curVal != *afterVal {
			notes = append(notes, fmt.Sprintf("%s: edited since apply — left alone", name))
			return
		}
		set(beforeVal)
		restored = append(restored, name)
	}

	curStudio := ""
	if cur.Studio != nil {
		curStudio = cur.Studio.ID
	}

	str("title", cur.Title, after.Title, before.Title, func(v *string) { u.Title = v })
	str("code", cur.Code, after.Code, before.Code, func(v *string) { u.Code = v })
	str("date", cur.Date, after.Date, before.Date, func(v *string) { u.Date = v })
	str("details", cur.Details, after.Details, before.Details, func(v *string) { u.Details = v })
	str("director", cur.Director, after.Director, before.Director, func(v *string) { u.Director = v })
	str("studio_id", curStudio, after.StudioID, before.StudioID, func(v *string) { u.StudioID = v })

	if after.Rating100 != nil {
		switch {
		case !equalIntPtr(cur.Rating100, after.Rating100):
			notes = append(notes, "rating100: edited since apply — left alone")
		case before.Rating100 == nil:
			notes = append(notes, "rating100: was unset before the union — clearing it back to unset isn't supported, left as the union left it")
		default:
			u.Rating100 = before.Rating100
			restored = append(restored, "rating100")
		}
	}

	if after.Organized != nil {
		if cur.Organized != *after.Organized {
			notes = append(notes, "organized: edited since apply — left alone")
		} else {
			u.Organized = before.Organized
			restored = append(restored, "organized")
		}
	}

	list := func(name string, curVal, afterVal, beforeVal []string, set func([]string)) {
		if afterVal == nil {
			return
		}
		if !sameSet(curVal, afterVal) {
			notes = append(notes, fmt.Sprintf("%s: edited since apply — left alone", name))
			return
		}
		if len(beforeVal) == 0 {
			clearFields = append(clearFields, name)
		} else {
			set(beforeVal)
		}
		restored = append(restored, name)
	}
	list("urls", cur.URLs, after.URLs, before.URLs, func(v []string) { u.URLs = v })
	list("tag_ids", tagIDsOf(cur.Tags), after.TagIDs, before.TagIDs, func(v []string) { u.TagIDs = v })
	list("performer_ids", performerIDsOf(cur.Performers), after.PerformerIDs, before.PerformerIDs, func(v []string) { u.PerformerIDs = v })
	list("gallery_ids", galleryIDsOf(cur.Galleries), after.GalleryIDs, before.GalleryIDs, func(v []string) { u.GalleryIDs = v })

	if after.StashIDs != nil {
		if !sameStashIDs(cur.StashIDs, after.StashIDs) {
			notes = append(notes, "stash_ids: edited since apply — left alone")
		} else {
			if len(before.StashIDs) == 0 {
				clearFields = append(clearFields, "stash_ids")
			} else {
				u.StashIDs = before.StashIDs
			}
			restored = append(restored, "stash_ids")
		}
	}

	return u, clearFields, restored, notes
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameSet compares two string slices as sets: order does not survive a round
// trip through Stash, so restoring must not treat a reorder as an edit.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	return slices.Equal(sa, sb)
}

func sameStashIDs(a, b []stash.StashID) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(s stash.StashID) string { return s.Endpoint + "\x00" + s.ID }
	sa := make([]string, len(a))
	for i, s := range a {
		sa[i] = key(s)
	}
	sb := make([]string, len(b))
	for i, s := range b {
		sb[i] = key(s)
	}
	sort.Strings(sa)
	sort.Strings(sb)
	return slices.Equal(sa, sb)
}

// findRow returns the first row of kind whose Target matches, in the order
// MutationsByRun returned them.
func findRow(rows []ledger.Mutation, kind, target string) (ledger.Mutation, bool) {
	for _, m := range rows {
		if m.Kind == kind && m.Target == target {
			return m, true
		}
	}
	return ledger.Mutation{}, false
}

func loserIDsOf(g ledger.Group) []string {
	out := make([]string, 0, len(g.SceneIDs))
	for _, id := range g.SceneIDs {
		if id != g.Keeper {
			out = append(out, id)
		}
	}
	return out
}

// restoredVerdict infers the verdict a group had before apply quarantined
// it. A leading "completeness" step (internal/keeper.Rules.Eligible) means
// plan restricted the keeper pool to canonical-matching copies, which only
// happens for a `partial` group; anything else was `certain`. The verdict
// itself is not recoverable — SetOutcome overwrites it — so this is a
// best-effort inference, not a stored fact.
func restoredVerdict(steps []ledger.Step) string {
	if len(steps) > 0 && steps[0].Rule == "completeness" {
		return ledger.VerdictPartial
	}
	return ledger.VerdictCertain
}

// do sends one write, recording it in the mutation log before it goes and
// stamping the outcome after. A dry run sends nothing and logs nothing.
func (u *undoer) do(ctx context.Context, m ledger.Mutation, send func() error) error {
	if !u.opts.Commit {
		return nil
	}
	m.RunID = u.runID
	id, err := u.l.AppendMutation(ctx, m)
	if err != nil {
		return err
	}
	if err := send(); err != nil {
		_ = u.l.MarkMutation(ctx, id, false, err.Error())
		return err
	}
	return u.l.MarkMutation(ctx, id, true, "")
}
