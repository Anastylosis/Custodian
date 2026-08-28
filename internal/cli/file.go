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

var (
	flagFileCommit bool
	flagFileYes    bool
	flagFileLimit  int
	flagFileLedger string
)

func newFileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "file",
		Short: "Move and rename scenes' files to match the organize template",
		Long: `Computes an ideal path for every live scene in the ledger from a
configurable template and Stash metadata (performer, studio, title, date,
resolution), and reports what would move. Video moves go through Stash's
moveFiles API and are logged to the ledger's mutation log; sidecar files
(subtitles, and anything else sharing a video's stem) travel with their
video via internal/sidecar, since Stash does not index them and moveFiles
cannot carry them.

Scenes that are the losing side of a quarantined or partial duplicate
group are left alone — they are about to leave the library.

Dry-run default, --commit required, interactive YES prompt on commit.`,
		RunE: runFile,
	}
	cmd.Flags().BoolVar(&flagFileCommit, "commit", false, "actually move files (default is dry-run)")
	cmd.Flags().BoolVar(&flagFileYes, "yes", false, "bypass the interactive YES prompt")
	cmd.Flags().IntVar(&flagFileLimit, "limit", 0, "limit how many scenes are considered (0 = no limit)")
	cmd.Flags().StringVar(&flagFileLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runFile(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagFileLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	result, err := organize.Scan(ctx, l, cfg, organize.ScanOptions{Limit: flagFileLimit})
	if err != nil {
		return err
	}
	if err := organize.PrintReport(out, result); err != nil {
		return err
	}

	mover, moverCleanup, err := sidecar.Build(ctx, cfg)
	if err != nil {
		return fmt.Errorf("%w\n\nRun `custodian sidecars check` to diagnose, or set sidecars.enabled: false", err)
	}
	defer moverCleanup()

	plan := organize.BuildApplyPlan(result.Entries)
	total := len(plan.Moves) + len(plan.Renames)
	if total > 0 && mover != nil {
		n, notes := organize.PreviewSidecars(ctx, plan, mover)
		fmt.Fprintf(out, "\nSidecars to move: %d  (via %s)\n", n, mover.Describe())
		for _, note := range notes {
			fmt.Fprintf(out, "  ⚠ %s\n", note)
		}
	}

	if !flagFileCommit {
		if total > 0 {
			fmt.Fprintln(out, "\nDry run. Re-run with --commit to move these via Stash.")
		}
		return nil
	}
	if total == 0 {
		return nil
	}

	ok, err := confirm.PromptYES(os.Stdin, out, confirm.Summary{
		Action: "move/rename files", GroupCount: total, SceneCount: total,
	}, flagFileYes)
	if err != nil || !ok {
		return err
	}

	report, err := organize.Execute(ctx, client, l, plan, mover, organize.Options{Commit: true})
	if err != nil {
		return err
	}
	return organize.PrintApplyReport(out, report)
}
