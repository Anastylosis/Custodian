package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/evidence"
	"github.com/spf13/cobra"
)

var (
	flagScanTolerancePct float64
	flagScanToleranceSec float64
	flagScanLedger       string
)

func newScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Find duplicate-candidate groups from the ledger's signals",
		Long: `Runs every evidence generator against the ledger, judges each group against
the duration tolerance, and replaces the ledger's candidate groups for this run.

Reads only what "custodian sync" already mirrored — scan never talks to Stash.`,
		RunE: runScan,
	}
	cmd.Flags().Float64Var(&flagScanTolerancePct, "tolerance-pct", 2, "duration tolerance, percent of the longer duration")
	cmd.Flags().Float64Var(&flagScanToleranceSec, "tolerance-seconds", 120, "duration tolerance, flat seconds cap")
	cmd.Flags().StringVar(&flagScanLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runScan(cmd *cobra.Command, _ []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagScanLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	policy := evidence.Policy{Tolerance: evidence.Tolerance{
		Percent: flagScanTolerancePct / 100,
		Seconds: flagScanToleranceSec,
	}}
	gens := []evidence.Generator{evidence.StashIDGenerator{}}

	summary, err := evidence.Run(context.Background(), l, gens, policy)
	if err != nil {
		return err
	}

	fmt.Fprint(cmd.OutOrStdout(), formatSummary(summary))
	return nil
}

// formatSummary renders an evidence.Summary as the plain-text lines `scan` prints.
func formatSummary(s evidence.Summary) string {
	out := "scan\n"
	out += fmt.Sprintf("  certain:     %d (%s)\n", s.Certain, confirm.HumanBytes(s.CertainBytes))
	out += fmt.Sprintf("  conflicted:  %d (%s)\n", s.Conflicted, confirm.HumanBytes(s.ConflictedBytes))
	out += fmt.Sprintf("  dismissed:   %d (%s)\n", s.Dismissed, confirm.HumanBytes(s.DismissedBytes))
	out += fmt.Sprintf("  duration:    %s\n", s.Duration.Round(time.Millisecond))
	return out
}
