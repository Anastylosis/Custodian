package cli

import (
	"context"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/reclaim"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	"github.com/spf13/cobra"
)

var (
	flagUndoCommit    bool
	flagUndoYes       bool
	flagUndoRun       int64
	flagUndoBackupDir string
	flagUndoLedger    string
)

func newUndoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "undo [signature]",
		Short: "Reverse a quarantined group's apply",
		Long: `Reverses what "custodian apply" did to one group — or, with --run, to every
group one apply invocation quarantined — reading what it wrote from the
mutation log (REBUILD.md §4.4's Reversal paragraph):

  1. remove the quarantine tag from each loser
  2. move each loser's file back to its recorded original path, refusing if
     a different file now occupies it, and move its subtitles with it
  3. restore the keeper's recorded pre-union field values, skipping any
     field someone changed since apply rather than overwriting it

Only a group still in the "quarantined" verdict can be undone; "custodian
prune" has already merged and deleted a reclaimed one, and its verified
backup is the only way back. Dry run by default.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runUndo,
	}
	cmd.Flags().BoolVar(&flagUndoCommit, "commit", false, "actually reverse the quarantine")
	cmd.Flags().BoolVar(&flagUndoYes, "yes", false, "skip the interactive YES prompt (still needs --commit)")
	cmd.Flags().Int64Var(&flagUndoRun, "run", 0, "undo every group quarantined by this apply run, instead of naming a signature")
	cmd.Flags().StringVar(&flagUndoBackupDir, "backup-dir", "",
		"accepted for symmetry with apply/prune; undo takes no backup of its own — it only reverses already-logged, already-backed-up writes")
	cmd.Flags().StringVar(&flagUndoLedger, "ledger", "", ledgerFlagUsage)
	return cmd
}

func runUndo(cmd *cobra.Command, args []string) error {
	var signature string
	if len(args) == 1 {
		signature = args[0]
	}
	if (signature == "") == (flagUndoRun == 0) {
		return fmt.Errorf("undo: pass exactly one of a signature or --run")
	}

	cfg, l, client, cleanup, err := loadLedgerAndClient(flagUndoLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	opts := reclaim.UndoOptions{Signature: signature, RunID: flagUndoRun, Commit: flagUndoCommit}

	mover, moverCleanup, err := sidecar.Build(ctx, cfg)
	if err != nil {
		return fmt.Errorf("sidecar access: %w", err)
	}
	defer moverCleanup()
	opts.Sidecars = mover

	if flagUndoCommit {
		ok, err := confirmUndo(ctx, cmd, l, opts)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	report, err := reclaim.Undo(ctx, client, l, opts)
	fmt.Fprint(cmd.OutOrStdout(), report.String())
	return err
}

// confirmUndo reads the same groups Undo itself will act on — no Stash
// calls — so the YES gate can say how many groups and scenes are at stake.
func confirmUndo(ctx context.Context, cmd *cobra.Command, l *ledger.Ledger, opts reclaim.UndoOptions) (bool, error) {
	groups, err := undoPreviewGroups(ctx, l, opts)
	if err != nil {
		return false, err
	}
	summary := confirm.Summary{Action: "undo the quarantine of", GroupCount: len(groups)}
	for _, g := range groups {
		summary.SceneCount += len(g.SceneIDs) - 1
		summary.ReclaimableBytes += g.Reclaimable
	}
	return confirm.PromptYES(cmd.InOrStdin(), cmd.OutOrStdout(), summary, flagUndoYes)
}

func undoPreviewGroups(ctx context.Context, l *ledger.Ledger, opts reclaim.UndoOptions) ([]ledger.Group, error) {
	if opts.Signature != "" {
		g, found, err := l.GroupBySignature(ctx, opts.Signature)
		if err != nil || !found {
			return nil, err
		}
		return []ledger.Group{g}, nil
	}
	return l.GroupsByAppliedRun(ctx, opts.RunID)
}
