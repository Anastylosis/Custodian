package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/evidence"
	"github.com/Anastylosis/Custodian/internal/keeper"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

var (
	flagPlanVerdict string
	flagPlanJSON    bool
	flagPlanLimit   int
	flagPlanLedger  string
)

// planRuleOrder is internal/keeper's rule chain order, so the histogram
// reads the same way the rules run rather than in map-iteration order.
// "completeness" leads a `partial` group's Steps but is never itself the
// last (deciding) step unless it was the group's only eligible scene.
var planRuleOrder = []string{"tier", "bitrate", "codec", "size", "location", "scene_id", "completeness"}

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Choose the keeper in every duplicate-candidate group and store the plan",
		Long: `Runs internal/keeper.Choose over every live group with the given verdict
(certain by default), stores its choice, and reports why: the keeper, the
losers, and the rule that decided it.

Pure and read-only against Stash — plan never calls Stash and never writes a
file. "apply" (later) is what acts on this plan.`,
		RunE: runPlan,
	}
	cmd.Flags().StringVar(&flagPlanVerdict, "verdict", ledger.VerdictCertain, "verdict to plan: certain, conflicted, dismissed, partial")
	cmd.Flags().BoolVar(&flagPlanJSON, "json", false, "emit the planned groups as JSON")
	cmd.Flags().IntVar(&flagPlanLimit, "limit", 0, "limit the number of groups planned (0 = no limit)")
	cmd.Flags().StringVar(&flagPlanLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runPlan(cmd *cobra.Command, _ []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagPlanLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: flagPlanVerdict})
	if err != nil {
		return err
	}
	if flagPlanLimit > 0 && len(groups) > flagPlanLimit {
		groups = groups[:flagPlanLimit]
	}

	files, err := l.PrimaryFiles(ctx, allSceneIDs(groups))
	if err != nil {
		return err
	}

	histogram, total, err := choose(ctx, l, groups, files)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if flagPlanJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(groups)
	}

	fmt.Fprint(out, formatPlan(groups, files, histogram, total))
	return nil
}

// choose runs keeper.Choose over every group, stores each choice, and fills
// in Keeper/Steps/Reclaimable on groups in place. Returns a rule-name
// histogram of what decided each group and the total reclaimable bytes.
func choose(ctx context.Context, l *ledger.Ledger, groups []ledger.Group, files map[string]ledger.PrimaryFile) (map[string]int, int64, error) {
	rules := keeper.DefaultRules()
	histogram := map[string]int{}
	var total int64

	for i := range groups {
		gr := rules
		if groups[i].Verdict == ledger.VerdictPartial {
			gr.Eligible = evidence.CompleteSceneIDs(groups[i])
		}
		c := keeper.Choose(groups[i], files, gr)

		var reclaimable int64
		for _, loser := range c.Losers {
			reclaimable += files[loser].Size
		}

		if err := l.SetChoice(ctx, groups[i].Signature, c.Keeper, c.Losers, c.Steps); err != nil {
			return nil, 0, err
		}

		groups[i].Keeper = c.Keeper
		groups[i].Steps = c.Steps
		groups[i].Reclaimable = reclaimable
		total += reclaimable

		if len(c.Steps) > 0 {
			histogram[c.Steps[len(c.Steps)-1].Rule]++
		}
	}
	return histogram, total, nil
}

// formatPlan renders planned groups as the plain-text blocks `plan` prints,
// followed by the groups-planned/reclaimable/decided-by summary.
func formatPlan(groups []ledger.Group, files map[string]ledger.PrimaryFile, histogram map[string]int, total int64) string {
	var b strings.Builder
	for _, g := range groups {
		formatPlanGroup(&b, g, files)
	}
	fmt.Fprintf(&b, "planned: %d groups, %s reclaimable\n", len(groups), confirm.HumanBytes(total))
	if len(histogram) > 0 {
		b.WriteString("decided by:\n")
		for _, rule := range planRuleOrder {
			if n, ok := histogram[rule]; ok {
				fmt.Fprintf(&b, "  %-9s %d\n", rule+":", n)
			}
		}
	}
	return b.String()
}

func formatPlanGroup(b *strings.Builder, g ledger.Group, files map[string]ledger.PrimaryFile) {
	fmt.Fprintf(b, "group %s (%s)\n", shortSignature(g.Signature), g.Verdict)
	fmt.Fprintf(b, "  keeper: %s\n", formatFileSummary(g.Keeper, files[g.Keeper]))
	for _, id := range g.SceneIDs {
		if id == g.Keeper {
			continue
		}
		fmt.Fprintf(b, "  loser:  %s\n", formatFileSummary(id, files[id]))
	}
	// Only the rules that were not ties, plus the first deciding one, so the
	// report stays short — keeper.Choose already stops at the first rule
	// that decides, so this is just the tail of Steps.
	for _, s := range g.Steps {
		if isTieOutcome(s.Outcome) {
			continue
		}
		fmt.Fprintf(b, "  %s: %s → %s\n", s.Rule, s.Detail, s.Outcome)
	}
	fmt.Fprintf(b, "  reclaimable: %s\n\n", confirm.HumanBytes(g.Reclaimable))
}

func isTieOutcome(outcome string) bool {
	return outcome == "tie" || strings.HasPrefix(outcome, "tie ")
}

// formatFileSummary renders one scene's primary file as `plan`'s keeper/
// loser line: id, resolution tier, bitrate, size, basename.
func formatFileSummary(id string, pf ledger.PrimaryFile) string {
	tier := stash.TierOf(pf.Width, pf.Height)
	return fmt.Sprintf("%s · %s · %s · %s · %s", id, tier, formatBitrate(pf.BitRate), confirm.HumanBytes(pf.Size), pf.Basename)
}

func formatBitrate(bps int64) string {
	switch {
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f Mbps", float64(bps)/1_000_000)
	case bps > 0:
		return fmt.Sprintf("%d kbps", bps/1_000)
	default:
		return "unknown"
	}
}
