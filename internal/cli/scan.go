package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/evidence"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

var (
	flagScanTolerancePct float64
	flagScanToleranceSec float64
	flagScanLedger       string
	flagScanGenerators   string
)

// allGenerators is scan's default --generators list, in the order the
// summary reports them.
var allGenerators = []string{"stash_id", "phash", "multifile", "filename", "title_date"}

func newScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Find duplicate-candidate groups from the ledger's signals",
		Long: `Runs every evidence generator against the ledger, judges each group against
the duration tolerance, and replaces the ledger's candidate groups for this run.

Every generator but phash reads only what "custodian sync" already mirrored.
phash calls Stash's own findDuplicateScenes live — read-only, but it needs a
reachable server; without stash.url configured it is skipped with a note.`,
		RunE: runScan,
	}
	cmd.Flags().Float64Var(&flagScanTolerancePct, "tolerance-pct", 2, "duration tolerance, percent of the longer duration")
	cmd.Flags().Float64Var(&flagScanToleranceSec, "tolerance-seconds", 120, "duration tolerance, flat seconds cap")
	cmd.Flags().StringVar(&flagScanLedger, "ledger", "", ledgerFlagUsage)
	cmd.Flags().StringVar(&flagScanGenerators, "generators", strings.Join(allGenerators, ","),
		"comma-separated generators to run: stash_id, phash, multifile, filename, title_date")
	return cmd
}

func runScan(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagScanLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	policy := evidence.Policy{Tolerance: evidence.Tolerance{
		Percent: flagScanTolerancePct / 100,
		Seconds: flagScanToleranceSec,
	}}
	gens, notes := buildGenerators(strings.Split(flagScanGenerators, ","), cfg, client)
	for _, note := range notes {
		fmt.Fprintln(cmd.OutOrStdout(), note)
	}

	summary, err := evidence.Run(context.Background(), l, gens, policy)
	if err != nil {
		return err
	}

	fmt.Fprint(cmd.OutOrStdout(), formatSummary(summary))
	return nil
}

// buildGenerators turns a --generators list into evidence.Generator values.
// phash is skipped (with a note, not an error) when stash.url isn't
// configured — every other generator only reads the ledger. An unknown name
// is also a note rather than a hard failure, so a typo doesn't blank the run.
func buildGenerators(names []string, cfg *config.Config, client *stash.Client) ([]evidence.Generator, []string) {
	var gens []evidence.Generator
	var notes []string
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		switch name {
		case "":
			continue
		case "stash_id":
			gens = append(gens, evidence.StashIDGenerator{})
		case "multifile":
			gens = append(gens, evidence.MultifileGenerator{})
		case "filename":
			gens = append(gens, evidence.FilenameGenerator{})
		case "title_date":
			gens = append(gens, evidence.TitleDateGenerator{})
		case "phash":
			if cfg.Stash.URL == "" {
				notes = append(notes, "skipping phash: no stash.url configured")
				continue
			}
			gens = append(gens, evidence.PhashGenerator{
				Client:       client,
				Distance:     cfg.Scan.PhashDistance,
				DurationDiff: cfg.Scan.DurationDiffSeconds,
			})
		default:
			notes = append(notes, fmt.Sprintf("skipping unknown generator %q", name))
		}
	}
	return gens, notes
}

// formatSummary renders an evidence.Summary as the plain-text lines `scan` prints.
func formatSummary(s evidence.Summary) string {
	out := "scan\n"
	if len(s.Generators) > 0 {
		out += "  generators:\n"
		for _, name := range allGenerators {
			gs, ok := s.Generators[name]
			if !ok {
				continue
			}
			out += fmt.Sprintf("    %-10s found %d (new %d, known %d)\n", name, gs.Found, gs.New, gs.Known)
		}
	}
	out += fmt.Sprintf("  certain:     %d (%s)\n", s.Certain, confirm.HumanBytes(s.CertainBytes))
	out += fmt.Sprintf("  conflicted:  %d (%s)\n", s.Conflicted, confirm.HumanBytes(s.ConflictedBytes))
	out += fmt.Sprintf("  probable:    %d (%s) — review only, plan does not choose a keeper yet\n", s.Probable, confirm.HumanBytes(s.ProbableBytes))
	out += fmt.Sprintf("  rejected:    %d (%s)\n", s.Rejected, confirm.HumanBytes(s.RejectedBytes))
	out += fmt.Sprintf("  dismissed:   %d (%s)\n", s.Dismissed, confirm.HumanBytes(s.DismissedBytes))
	out += fmt.Sprintf("  duration:    %s\n", s.Duration.Round(time.Millisecond))
	return out
}
