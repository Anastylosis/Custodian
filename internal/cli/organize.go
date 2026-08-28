package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/organize"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	"github.com/spf13/cobra"
)

// `custodian organize` predates the ledger (REBUILD.md §7 phase 5a): it used
// to page Stash directly and cache its plan in a SQLite table
// (internal/store's organize_plans). That table is now dead — kept, not
// deleted, per REBUILD.md §5 — because there is nothing left between runs to
// persist: the ledger is the record, and a fresh Scan is cheap enough to
// redo on every invocation. These subcommands are now thin wrappers over the
// same engine `custodian file` uses, kept so the command names don't
// disappear out from under anyone's scripts; `file` is the one to reach for
// going forward.

var (
	flagOrganizeScanMaxScenes int
	flagOrganizeApplyCommit   bool
	flagOrganizeApplyYes      bool
	flagOrganizeLedger        string
)

func newOrganizeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "organize",
		Short: "Workflow D: move and rename files based on Stash metadata (see `custodian file`)",
		Long: `Superseded by "custodian file", which does scan, report and apply in one
command against the ledger. Kept as a thin wrapper over the same engine so
these subcommand names keep working.

Video moves go through Stash's moveFiles API. Same safety model as every
other workflow: dry-run default, --commit required, interactive YES
prompt on commit.

Sidecar files (subtitles, and anything else sharing a video's stem) are
moved by custodian itself, because Stash does not index them as files and
moveFiles cannot carry them. That is the one place this tool touches the
filesystem, and it needs sidecars.path_map configured so it can find the
library. Run "custodian sidecars check" to verify.`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Compute ideal paths for every live scene and report them",
		RunE:  runOrganizeScan,
	}
	scanCmd.Flags().IntVar(&flagOrganizeScanMaxScenes, "max-scenes", 0, "stop after considering N scenes (0 = no limit)")
	scanCmd.Flags().StringVar(&flagOrganizeLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")

	reportCmd := &cobra.Command{
		Use:   "report",
		Short: "Alias for scan — there is no separate cached plan any more",
		RunE:  runOrganizeScan,
	}
	reportCmd.Flags().IntVar(&flagOrganizeScanMaxScenes, "max-scenes", 0, "stop after considering N scenes (0 = no limit)")
	reportCmd.Flags().StringVar(&flagOrganizeLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")

	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Execute the proposed moves via Stash's moveFiles API",
		RunE:  runOrganizeApply,
	}
	applyCmd.Flags().BoolVar(&flagOrganizeApplyCommit, "commit", false, "actually move files (default is dry-run)")
	applyCmd.Flags().BoolVar(&flagOrganizeApplyYes, "yes", false, "bypass interactive YES prompt for --commit")
	applyCmd.Flags().StringVar(&flagOrganizeLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")

	cmd.AddCommand(scanCmd, reportCmd, applyCmd)
	return cmd
}

func runOrganizeScan(cmd *cobra.Command, _ []string) error {
	cfg, l, _, cleanup, err := loadLedgerAndClient(flagOrganizeLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	res, err := organize.Scan(context.Background(), l, cfg, organize.ScanOptions{Limit: flagOrganizeScanMaxScenes})
	if err != nil {
		return err
	}
	return organize.PrintReport(cmd.OutOrStdout(), res)
}

func runOrganizeApply(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagOrganizeLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	res, err := organize.Scan(ctx, l, cfg, organize.ScanOptions{})
	if err != nil {
		return err
	}
	if err := organize.PrintReport(out, res); err != nil {
		return err
	}

	mover, moverCleanup, err := sidecar.Build(ctx, cfg)
	if err != nil {
		return fmt.Errorf("%w\n\nRun `custodian sidecars check` to diagnose, or set sidecars.enabled: false", err)
	}
	defer moverCleanup()

	plan := organize.BuildApplyPlan(res.Entries)
	total := len(plan.Moves) + len(plan.Renames)
	if total > 0 && mover != nil {
		n, notes := organize.PreviewSidecars(ctx, plan, mover)
		fmt.Fprintf(out, "\nSidecars to move: %d  (via %s)\n", n, mover.Describe())
		for _, note := range notes {
			fmt.Fprintf(out, "  ⚠ %s\n", note)
		}
	}

	if !flagOrganizeApplyCommit {
		if total > 0 {
			fmt.Fprintln(out, "\nThis was a dry run. Re-run with --commit to move files via Stash.")
			fmt.Fprintln(out, "--commit triggers an interactive YES confirmation; --yes bypasses it.")
		}
		return nil
	}
	if total == 0 {
		return nil
	}

	ok, err := confirm.PromptYES(os.Stdin, out, confirm.Summary{
		Action: "move/rename files", GroupCount: total, SceneCount: total,
	}, flagOrganizeApplyYes)
	if err != nil || !ok {
		return err
	}

	report, err := organize.Execute(ctx, client, l, plan, mover, organize.Options{Commit: true})
	if err != nil {
		return err
	}
	return organize.PrintApplyReport(out, report)
}
