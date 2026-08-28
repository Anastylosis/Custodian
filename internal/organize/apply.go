package organize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	stash "github.com/Anastylosis/stash-go"
)

// ApplyPlan collects the actionable entries for the commit step.
type ApplyPlan struct {
	Moves   []Entry
	Renames []Entry
}

// BuildApplyPlan splits a scan's entries into the ones `file --commit` acts
// on. Pure: the ledger holds nothing between `custodian file` invocations,
// so there is no "already applied" state to filter out here — re-run the
// scan to see what is left.
func BuildApplyPlan(entries []Entry) *ApplyPlan {
	plan := &ApplyPlan{}
	for _, e := range entries {
		switch e.Status {
		case "move":
			plan.Moves = append(plan.Moves, e)
		case "rename":
			plan.Renames = append(plan.Renames, e)
		}
	}
	return plan
}

// PreviewSidecars resolves the sidecars a dry run would move, so `file`
// without --commit reports them rather than surprising the user at commit
// time. Errors are returned as notes instead of aborting: an unreachable
// library should not stop the caller previewing the video moves.
func PreviewSidecars(ctx context.Context, plan *ApplyPlan, mover *sidecar.Mover) (int, []string) {
	if mover == nil || plan == nil {
		return 0, nil
	}
	var total int
	var notes []string
	all := append(append([]Entry{}, plan.Moves...), plan.Renames...)
	for _, e := range all {
		moves, err := mover.Plan(ctx, e.CurrentPath, e.TargetPath)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", e.CurrentPath, err))
			continue
		}
		total += len(moves)
	}
	return total, notes
}

// Options configures Execute.
type Options struct {
	// Commit sends the mutations. Without it Execute computes the same
	// report and sends nothing — the ledger's mutation log stays empty,
	// matching every other --commit-gated command's dry-run convention.
	Commit bool
}

// ApplyReport summarizes one Execute call.
type ApplyReport struct {
	DryRun    bool
	RunID     int64
	Succeeded int
	Failed    int
	Failures  []string

	// Sidecar totals across every file in the batch.
	SidecarsMoved   int
	SidecarsSkipped int
	SidecarsFailed  int
	// SidecarNotes carries the per-file detail worth surfacing: skips and
	// failures. Successful moves are counted, not listed, to keep the
	// report readable on large batches.
	SidecarNotes []string
}

// Execute runs the moveFiles mutation for each actionable entry, relocates
// its sidecars, and logs both to the ledger under one run of kind "file"
// (REBUILD.md §7 phase 5a).
//
// Ordering matters. The sidecar plan is computed BEFORE the video is moved,
// while the source directory is still intact — that is the only moment the
// competing-video check can be made correctly. The sidecars are then moved
// after Stash confirms the video move, so a failed moveFiles never leaves
// subtitles stranded in a directory their video never reached.
//
// mover may be nil, which disables sidecar handling entirely.
func Execute(ctx context.Context, client *stash.Client, l *ledger.Ledger, plan *ApplyPlan, mover *sidecar.Mover, opts Options) (*ApplyReport, error) {
	if client == nil || l == nil || plan == nil {
		return nil, errors.New("organize.Execute: nil dependency")
	}
	report := &ApplyReport{DryRun: !opts.Commit}

	all := make([]Entry, 0, len(plan.Moves)+len(plan.Renames))
	all = append(all, plan.Moves...)
	all = append(all, plan.Renames...)
	if len(all) == 0 {
		return report, nil
	}

	a := &applier{client: client, l: l, mover: mover, commit: opts.Commit}
	if opts.Commit {
		runID, err := l.BeginRun(ctx, "file", "")
		if err != nil {
			return report, fmt.Errorf("organize: beginning file run: %w", err)
		}
		a.runID, report.RunID = runID, runID
	}

	for _, e := range all {
		a.one(ctx, e, report)
	}

	if a.runID != 0 {
		_ = l.FinishRun(ctx, a.runID, ledger.RunSummary{
			Notes: fmt.Sprintf("moved %d, failed %d", report.Succeeded, report.Failed),
		})
	}
	return report, nil
}

// applier carries the state one Execute call shares across entries.
type applier struct {
	client *stash.Client
	l      *ledger.Ledger
	mover  *sidecar.Mover
	commit bool
	runID  int64
}

func (a *applier) one(ctx context.Context, e Entry, report *ApplyReport) {
	targetDir := filepath.Dir(e.TargetPath)
	targetBase := filepath.Base(e.TargetPath)

	// Resolve sidecars first: the source directory is about to change.
	sideMoves, sideErr := a.mover.Plan(ctx, e.CurrentPath, e.TargetPath)
	if sideErr != nil {
		report.SidecarNotes = append(report.SidecarNotes,
			fmt.Sprintf("%s: could not inspect sidecars: %v", e.CurrentPath, sideErr))
	}

	err := a.do(ctx, ledger.Mutation{
		Kind: "move", Target: "file " + e.FileID, From: e.CurrentPath, To: e.TargetPath, Note: "moveFiles",
	}, func() error {
		return a.client.MoveFiles(ctx, []string{e.FileID}, stash.MoveTarget{Folder: targetDir, Basename: targetBase})
	})
	if err != nil {
		report.Failed++
		report.Failures = append(report.Failures, fmt.Sprintf("file %s → %s: %v", e.FileID, e.TargetPath, err))
		return
	}
	report.Succeeded++

	if len(sideMoves) == 0 {
		return
	}
	if !a.commit {
		report.SidecarsMoved += len(sideMoves) // dry-run preview only; nothing actually moved
		return
	}

	res := a.mover.Apply(ctx, sideMoves)
	report.SidecarsMoved += len(res.Moved)
	report.SidecarsSkipped += len(res.Skipped)
	report.SidecarsFailed += len(res.Failed)
	for _, m := range res.Moved {
		a.logSidecar(ctx, m.SrcStash, m.DstStash, true, "")
	}
	for _, s := range res.Skipped {
		report.SidecarNotes = append(report.SidecarNotes, fmt.Sprintf("skipped %s: %s", s.Move.SrcStash, s.Reason))
		a.logSidecar(ctx, s.Move.SrcStash, s.Move.DstStash, false, s.Reason)
	}
	for _, f := range res.Failed {
		report.SidecarNotes = append(report.SidecarNotes, fmt.Sprintf("FAILED %s → %s: %v", f.Move.SrcStash, f.Move.DstStash, f.Err))
		a.logSidecar(ctx, f.Move.SrcStash, f.Move.DstStash, false, f.Err.Error())
	}
}

func (a *applier) logSidecar(ctx context.Context, from, to string, ok bool, note string) {
	id, err := a.l.AppendMutation(ctx, ledger.Mutation{
		RunID: a.runID, Kind: "sidecar", Target: filepath.Base(from), From: from, To: to,
	})
	if err != nil {
		return
	}
	_ = a.l.MarkMutation(ctx, id, ok, note)
}

// do sends one write, recording it in the mutation log before it goes and
// stamping the outcome after (internal/reclaim's own convention). A dry run
// sends nothing and logs nothing: the report Execute builds is the whole of
// its output.
func (a *applier) do(ctx context.Context, m ledger.Mutation, send func() error) error {
	if !a.commit {
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

// PrintReport renders the per-file plan and its counts — `custodian file`'s
// output before any --commit gate.
func PrintReport(w io.Writer, r *ScanResult) error {
	if r == nil || (len(r.Entries) == 0 && r.ScenesProcessed == 0 && r.SkippedDuplicate == 0) {
		_, err := io.WriteString(w, "(no live scenes to file)\n")
		return err
	}
	fmt.Fprintf(w, "=== custodian file ===\n")
	for _, e := range r.Entries {
		switch e.Status {
		case "move", "rename":
			fmt.Fprintf(w, "[%s] scene %s\n", e.Status, e.SceneID)
			fmt.Fprintf(w, "  from: %s\n", e.CurrentPath)
			fmt.Fprintf(w, "  to:   %s\n", e.TargetPath)
		case "conflict":
			fmt.Fprintf(w, "[CONFLICT] scene %s  file %s\n", e.SceneID, e.FileID)
			fmt.Fprintf(w, "  from: %s\n", e.CurrentPath)
			fmt.Fprintf(w, "  to:   %s\n", e.TargetPath)
			if e.Reason != "" {
				fmt.Fprintf(w, "  reason: %s\n", e.Reason)
			}
		case "skip_no_metadata":
			fmt.Fprintf(w, "[skip] scene %s  %s\n", e.SceneID, e.Reason)
			fmt.Fprintf(w, "  path: %s\n", e.CurrentPath)
		}
	}
	// already_correct entries aren't printed individually — too noisy on a
	// full library scan.

	fmt.Fprintf(w, "\nscenes considered: %d\n", r.ScenesProcessed)
	fmt.Fprintf(w, "  to move:       %d\n", r.Moves)
	fmt.Fprintf(w, "  to rename:     %d\n", r.Renames)
	fmt.Fprintf(w, "  already filed: %d\n", r.AlreadyCorrect)
	fmt.Fprintf(w, "  no metadata:   %d\n", r.SkipNoMetadata)
	fmt.Fprintf(w, "  conflicts:     %d\n", r.Conflicts)
	if r.SkippedDuplicate > 0 {
		fmt.Fprintf(w, "  skipped (duplicate, about to leave): %d\n", r.SkippedDuplicate)
	}
	if r.MultiFileWarning > 0 {
		fmt.Fprintf(w, "\n⚠ %d scenes have multiple files — only the primary file is moved.\n", r.MultiFileWarning)
		fmt.Fprintf(w, "  Run `custodian files apply --commit` first to clean up multi-file scenes.\n")
	}
	return nil
}

// PrintApplyReport writes the post-Execute summary.
func PrintApplyReport(w io.Writer, r *ApplyReport) error {
	verb, sidecarVerb := "moved", "moved"
	if r.DryRun {
		verb, sidecarVerb = "would move", "would move"
	}
	fmt.Fprintf(w, "\n=== file apply (%s) ===\n", mode(r.DryRun))
	fmt.Fprintf(w, "  %s:    %d\n", verb, r.Succeeded)
	fmt.Fprintf(w, "  failed:    %d\n", r.Failed)
	if r.SidecarsMoved+r.SidecarsSkipped+r.SidecarsFailed > 0 {
		fmt.Fprintf(w, "  sidecars %s: %d\n", sidecarVerb, r.SidecarsMoved)
		if r.SidecarsSkipped > 0 {
			fmt.Fprintf(w, "  sidecars skipped: %d\n", r.SidecarsSkipped)
		}
		if r.SidecarsFailed > 0 {
			fmt.Fprintf(w, "  sidecars failed:  %d\n", r.SidecarsFailed)
		}
	}
	if r.Failed > 0 {
		fmt.Fprintln(w, "\nFailures:")
		for _, f := range r.Failures {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if len(r.SidecarNotes) > 0 {
		fmt.Fprintln(w, "\nSidecar notes:")
		for _, n := range r.SidecarNotes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	if r.SidecarsMoved > 0 && !r.DryRun {
		fmt.Fprintln(w, "\nSidecars moved. Run a Stash scan over the affected folders so the")
		fmt.Fprintln(w, "captions are re-attached to their scenes.")
	}
	return nil
}

func mode(dryRun bool) string {
	if dryRun {
		return "DRY RUN"
	}
	return "COMMIT"
}

// HumanBytes re-exports for use by the CLI.
var HumanBytes = confirm.HumanBytes
