package organize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/Anastylosis/StashJanitor/internal/confirm"
	"github.com/Anastylosis/StashJanitor/internal/sidecar"
	"github.com/Anastylosis/StashJanitor/internal/stash"
	"github.com/Anastylosis/StashJanitor/internal/store"
)

// ApplyPlan collects the actionable rows for the commit step.
type ApplyPlan struct {
	Moves   []*store.OrganizePlan
	Renames []*store.OrganizePlan
}

// PlanApply walks the store and builds an ApplyPlan from actionable rows.
func PlanApply(ctx context.Context, st *store.Store) (*ApplyPlan, error) {
	rows, err := st.ListOrganizePlans(ctx, []string{"move", "rename"})
	if err != nil {
		return nil, err
	}
	plan := &ApplyPlan{}
	for _, r := range rows {
		if r.AppliedAt != nil {
			continue
		}
		switch r.Status {
		case "move":
			plan.Moves = append(plan.Moves, r)
		case "rename":
			plan.Renames = append(plan.Renames, r)
		}
	}
	return plan, nil
}

// PrintApplyPlan writes the summary.
func PrintApplyPlan(w io.Writer, plan *ApplyPlan, commit bool) error {
	mode := "DRY RUN"
	if commit {
		mode = "COMMIT"
	}
	total := len(plan.Moves) + len(plan.Renames)
	fmt.Fprintf(w, "=== stash-janitor organize apply (%s) ===\n", mode)
	fmt.Fprintf(w, "Files to move:    %d\n", len(plan.Moves))
	fmt.Fprintf(w, "Files to rename:  %d\n", len(plan.Renames))
	fmt.Fprintf(w, "Total actions:    %d\n", total)
	if total == 0 {
		fmt.Fprintln(w, "\nNothing to do. Try `stash-janitor organize scan` first.")
		return nil
	}
	if !commit {
		fmt.Fprintln(w, "\nThis was a dry run. Re-run with --commit to move files via Stash.")
		fmt.Fprintln(w, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
	}
	return nil
}

// ApplyReport summarizes execution.
type ApplyReport struct {
	Succeeded int
	Failed    int
	Failures  []string

	// Sidecar totals across every file in the batch.
	SidecarsMoved   int
	SidecarsSkipped int
	SidecarsFailed  int
	// SidecarNotes carries the per-file detail worth surfacing: skips and
	// failures. Successful moves are counted, not listed, to keep the report
	// readable on large batches.
	SidecarNotes []string
}

// Execute runs the moveFiles mutations for each actionable plan row, and
// relocates each file's sidecars alongside it.
//
// Ordering matters. The sidecar plan is computed BEFORE the video is moved,
// while the source directory is still intact — that is the only moment the
// competing-video check can be made correctly. The sidecars are then moved
// after Stash confirms the video move, so a failed moveFiles never leaves
// subtitles stranded in a directory their video never reached.
//
// mover may be nil, which disables sidecar handling entirely.
func Execute(ctx context.Context, c *stash.Client, st *store.Store, plan *ApplyPlan, mover *sidecar.Mover) (*ApplyReport, error) {
	if c == nil || st == nil || plan == nil {
		return nil, errors.New("organize.Execute: nil dependency")
	}
	report := &ApplyReport{}

	all := make([]*store.OrganizePlan, 0, len(plan.Moves)+len(plan.Renames))
	all = append(all, plan.Moves...)
	all = append(all, plan.Renames...)

	for _, p := range all {
		targetDir := filepath.Dir(p.TargetPath)
		targetBase := filepath.Base(p.TargetPath)

		// Resolve sidecars first: the source directory is about to change.
		sideMoves, sideErr := mover.Plan(ctx, p.CurrentPath, p.TargetPath)
		if sideErr != nil {
			slog.Warn("could not resolve sidecars; moving the video anyway",
				"file_id", p.FileID, "path", p.CurrentPath, "error", sideErr)
			report.SidecarNotes = append(report.SidecarNotes,
				fmt.Sprintf("%s: could not inspect sidecars: %v", p.CurrentPath, sideErr))
		}

		err := c.MoveFiles(ctx, stash.MoveFilesInput{
			IDs:                 []string{p.FileID},
			DestinationFolder:   targetDir,
			DestinationBasename: targetBase,
		})
		if err != nil {
			slog.Error("moveFiles failed; continuing",
				"file_id", p.FileID, "target", p.TargetPath, "error", err)
			if markErr := st.MarkOrganizePlanFailed(ctx, p.ID, err.Error()); markErr != nil {
				slog.Error("mark-failed also failed", "error", markErr)
			}
			report.Failed++
			report.Failures = append(report.Failures,
				fmt.Sprintf("file %s → %s: %v", p.FileID, p.TargetPath, err))
			continue
		}

		if len(sideMoves) > 0 {
			res := mover.Apply(ctx, sideMoves)
			report.SidecarsMoved += len(res.Moved)
			report.SidecarsSkipped += len(res.Skipped)
			report.SidecarsFailed += len(res.Failed)
			for _, s := range res.Skipped {
				report.SidecarNotes = append(report.SidecarNotes,
					fmt.Sprintf("skipped %s: %s", s.Move.SrcStash, s.Reason))
			}
			for _, f := range res.Failed {
				slog.Error("sidecar move failed",
					"src", f.Move.SrcLocal, "dst", f.Move.DstLocal, "error", f.Err)
				report.SidecarNotes = append(report.SidecarNotes,
					fmt.Sprintf("FAILED %s → %s: %v", f.Move.SrcStash, f.Move.DstStash, f.Err))
			}
		}

		if err := st.MarkOrganizePlanApplied(ctx, p.ID); err != nil {
			slog.Error("mark-applied failed after successful move",
				"file_id", p.FileID, "error", err)
		}
		report.Succeeded++
	}
	return report, nil
}

// PrintApplyReport writes the post-execution summary.
func PrintApplyReport(w io.Writer, r *ApplyReport) error {
	fmt.Fprintf(w, "\n=== organize apply complete ===\n")
	fmt.Fprintf(w, "  succeeded: %d\n", r.Succeeded)
	fmt.Fprintf(w, "  failed:    %d\n", r.Failed)
	if r.SidecarsMoved+r.SidecarsSkipped+r.SidecarsFailed > 0 {
		fmt.Fprintf(w, "  sidecars moved:   %d\n", r.SidecarsMoved)
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
	if r.SidecarsMoved > 0 {
		fmt.Fprintln(w, "\nSidecars moved. Run a Stash scan over the affected folders so the")
		fmt.Fprintln(w, "captions are re-attached to their scenes.")
	}
	return nil
}

// PreviewSidecars resolves the sidecars a dry run would move, so `organize
// apply` without --commit reports them rather than surprising the user at
// commit time. Errors are returned as notes instead of aborting: an
// unreachable library should not stop the caller previewing the video moves.
func PreviewSidecars(ctx context.Context, plan *ApplyPlan, mover *sidecar.Mover) (int, []string) {
	if mover == nil || plan == nil {
		return 0, nil
	}
	var total int
	var notes []string
	all := append(append([]*store.OrganizePlan{}, plan.Moves...), plan.Renames...)
	for _, p := range all {
		moves, err := mover.Plan(ctx, p.CurrentPath, p.TargetPath)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", p.CurrentPath, err))
			continue
		}
		total += len(moves)
	}
	return total, notes
}

// PrintReport renders the per-file organize report.
func PrintReport(w io.Writer, plans []*store.OrganizePlan) error {
	if len(plans) == 0 {
		_, err := io.WriteString(w, "(no organize plans match the filter)\n")
		return err
	}
	var moves, renames, skips, conflicts, correct int
	for _, p := range plans {
		switch p.Status {
		case "move":
			moves++
		case "rename":
			renames++
		case "skip_no_metadata":
			skips++
		case "conflict":
			conflicts++
		case "already_correct":
			correct++
		}
	}
	fmt.Fprintf(w, "=== organize report (%d plans) ===\n", len(plans))
	fmt.Fprintf(w, "  move: %d  rename: %d  already_correct: %d  skip: %d  conflict: %d\n\n",
		moves, renames, correct, skips, conflicts)

	for _, p := range plans {
		switch p.Status {
		case "move", "rename":
			fmt.Fprintf(w, "[%s] scene %s\n", p.Status, p.SceneID)
			fmt.Fprintf(w, "  from: %s\n", p.CurrentPath)
			fmt.Fprintf(w, "  to:   %s\n", p.TargetPath)
		case "conflict":
			fmt.Fprintf(w, "[CONFLICT] scene %s  file %s\n", p.SceneID, p.FileID)
			fmt.Fprintf(w, "  from: %s\n", p.CurrentPath)
			fmt.Fprintf(w, "  to:   %s\n", p.TargetPath)
			if p.Reason != "" {
				fmt.Fprintf(w, "  reason: %s\n", p.Reason)
			}
		case "skip_no_metadata":
			fmt.Fprintf(w, "[skip] scene %s  %s\n", p.SceneID, p.Reason)
			fmt.Fprintf(w, "  path: %s\n", p.CurrentPath)
		}
	}

	// Don't print already_correct individually — too noisy.
	if correct > 0 {
		fmt.Fprintf(w, "\n(%d files already in the correct location, not shown)\n", correct)
	}
	return nil
}

// HumanBytes re-exports for use by the CLI.
var HumanBytes = confirm.HumanBytes
