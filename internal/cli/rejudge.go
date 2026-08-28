package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/evidence"
	"github.com/spf13/cobra"
)

var (
	flagRejudgeTrimmedPct float64
	flagRejudgeTrimmedSec float64
	flagRejudgeLedger     string
)

func newRejudgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rejudge",
		Short: "Re-classify groups against their canonical runtimes",
		Long: `Runs evidence.Rejudge over every live "conflicted" and "certain" group that
now has a canonical duration for all of its scenes (fetched by "custodian
canon"): all scenes matching moves a group to certain, one matching and the
rest shorter moves it to partial, and any scene longer than canonical — or
sources disagreeing on it — moves it to mistag.

Read-only against Stash — rejudge only reads and writes the ledger.`,
		RunE: runRejudge,
	}
	cmd.Flags().Float64Var(&flagRejudgeTrimmedPct, "trimmed-pct", evidence.DefaultTrimmed.Percent*100, "trimmed tolerance, percent of the canonical duration")
	cmd.Flags().Float64Var(&flagRejudgeTrimmedSec, "trimmed-seconds", evidence.DefaultTrimmed.Seconds, "trimmed tolerance, flat seconds cap")
	cmd.Flags().StringVar(&flagRejudgeLedger, "ledger", "", ledgerFlagUsage)
	return cmd
}

func runRejudge(cmd *cobra.Command, _ []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagRejudgeLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	trimmed := evidence.Trimmed{Percent: flagRejudgeTrimmedPct / 100, Seconds: flagRejudgeTrimmedSec}
	sum, err := evidence.Rejudge(context.Background(), l, trimmed)
	if err != nil {
		return err
	}

	fmt.Fprint(cmd.OutOrStdout(), formatRejudge(sum))
	return nil
}

// formatRejudge renders an evidence.RejudgeSummary as the plain-text lines
// `rejudge` prints: from->to counts, then a few examples.
func formatRejudge(s evidence.RejudgeSummary) string {
	var b strings.Builder
	b.WriteString("rejudge\n")
	fmt.Fprintf(&b, "  -> certain:  %d\n", s.ToCertain)
	fmt.Fprintf(&b, "  -> partial:  %d\n", s.ToPartial)
	fmt.Fprintf(&b, "  -> mistag:   %d\n", s.ToMistag)
	fmt.Fprintf(&b, "  duration:    %s\n", s.Duration.Round(time.Millisecond))

	if len(s.Examples) > 0 {
		b.WriteString("examples:\n")
		for _, e := range s.Examples {
			fmt.Fprintf(&b, "  %s: %s -> %s (%s)\n", shortSignature(e.Signature), e.From, e.To, e.Reason)
		}
	}
	return b.String()
}
