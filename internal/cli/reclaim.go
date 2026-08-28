package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/reclaim"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	"github.com/spf13/cobra"
)

var (
	flagApplyCommit     bool
	flagApplyYes        bool
	flagApplyLimit      int
	flagApplyQuarantine string
	flagApplyBackupDir  string
	flagApplyLedger     string
)

func newApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Quarantine the losers of every planned duplicate group",
		Long: `Acts on what "custodian plan" chose, in the order REBUILD.md §4.4 lays down:

  1. take a verified database backup, and refuse to continue without one
  2. union each loser's metadata onto the keeper (sceneUpdate)
  3. tag every loser, move its file into the quarantine directory, and move
     its subtitles after it

Nothing is deleted. The loser scene stays in Stash carrying the quarantine
tag, so it can be reviewed and rescued from Stash's own UI; "custodian prune"
is what merges and deletes, 30 days later.

Dry run by default: the plan above is computed in full — including the exact
mutations that would be sent — and nothing is written anywhere.`,
		RunE: runApply,
	}
	cmd.Flags().BoolVar(&flagApplyCommit, "commit", false, "actually write to Stash and move files")
	cmd.Flags().BoolVar(&flagApplyYes, "yes", false, "skip the interactive YES prompt (still needs --commit)")
	cmd.Flags().IntVar(&flagApplyLimit, "limit", 0, "limit the number of groups acted on (0 = no limit)")
	cmd.Flags().StringVar(&flagApplyQuarantine, "quarantine-dir", "", "where losers' files go, in the server's path notation")
	cmd.Flags().StringVar(&flagApplyBackupDir, "backup-dir", "", "where the verified database backup is written")
	cmd.Flags().StringVar(&flagApplyLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runApply(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagApplyLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	opts := reclaim.Options{
		QuarantineDir: pick(flagApplyQuarantine, cfg.Quarantine.Dir),
		BackupDir:     pick(flagApplyBackupDir, cfg.Quarantine.BackupDir),
		Tag:           cfg.Quarantine.Tag,
		MistagTag:     cfg.Quarantine.MistagTag,
		Commit:        flagApplyCommit,
		Limit:         flagApplyLimit,
	}

	mover, moverCleanup, err := sidecar.Build(ctx, cfg)
	if err != nil {
		return fmt.Errorf("sidecar access: %w", err)
	}
	defer moverCleanup()
	opts.Sidecars = mover

	if flagApplyCommit {
		ok, err := confirmApply(ctx, cmd, l, opts.Limit)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	report, err := reclaim.Apply(ctx, client, l, opts)
	fmt.Fprint(cmd.OutOrStdout(), report.String())
	return err
}

// confirmApply reads the plan out of the ledger — no Stash calls — so the
// YES gate can say how many groups and how many bytes are at stake.
func confirmApply(ctx context.Context, cmd *cobra.Command, l *ledger.Ledger, limit int) (bool, error) {
	var summary confirm.Summary
	summary.Action = "quarantine"
	for _, verdict := range []string{ledger.VerdictCertain, ledger.VerdictPartial} {
		if limit > 0 && summary.GroupCount >= limit {
			break
		}
		groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: verdict})
		if err != nil {
			return false, err
		}
		for _, g := range groups {
			multifile := reclaim.IsMultifileGroup(g)
			if !multifile && g.Keeper == "" {
				continue
			}
			summary.GroupCount++
			summary.SceneCount += multifileSceneCount(g, multifile)
			summary.ReclaimableBytes += g.Reclaimable
			if limit > 0 && summary.GroupCount == limit {
				break
			}
		}
	}
	return confirm.PromptYES(cmd.InOrStdin(), cmd.OutOrStdout(), summary, flagApplyYes)
}

// multifileSceneCount is how many "scenes" a group's quarantine touches for
// the YES-prompt summary: every loser scene for a normal group, zero for a
// multifile one — no scene disappears, only files within the one it already
// has (`plan` never sets Keeper on these, so they need the flag rather than
// the usual Keeper check).
func multifileSceneCount(g ledger.Group, multifile bool) int {
	if multifile {
		return 0
	}
	return len(g.SceneIDs) - 1
}

var (
	flagPruneAge       string
	flagPruneCommit    bool
	flagPruneYes       bool
	flagPruneLimit     int
	flagPruneBackupDir string
	flagPruneLedger    string
)

func newPruneCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Merge and delete quarantined losers once they are old enough",
		Long: `The only irreversible step, and it runs on nothing that was not first
quarantined and logged. For every group "custodian apply" quarantined more
than --age ago, whose losers still carry the quarantine tag:

  1. take a verified database backup
  2. sceneMerge the losers into the keeper, carrying their play history
  3. deleteFiles their videos from disk

A loser someone un-tagged in Stash has been rescued, and its whole group is
left alone. Dry run by default.`,
		RunE: runPrune,
	}
	cmd.Flags().StringVar(&flagPruneAge, "age", "30d", "how long a quarantine must have sat before it is pruned")
	cmd.Flags().BoolVar(&flagPruneCommit, "commit", false, "actually merge and delete")
	cmd.Flags().BoolVar(&flagPruneYes, "yes", false, "skip the interactive YES prompt (still needs --commit)")
	cmd.Flags().IntVar(&flagPruneLimit, "limit", 0, "limit the number of groups acted on (0 = no limit)")
	cmd.Flags().StringVar(&flagPruneBackupDir, "backup-dir", "", "where the verified database backup is written")
	cmd.Flags().StringVar(&flagPruneLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runPrune(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagPruneLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	age, err := parseAge(flagPruneAge)
	if err != nil {
		return err
	}

	ctx := context.Background()
	opts := reclaim.PruneOptions{
		Age:       age,
		BackupDir: pick(flagPruneBackupDir, cfg.Quarantine.BackupDir),
		Tag:       cfg.Quarantine.Tag,
		Commit:    flagPruneCommit,
		Limit:     flagPruneLimit,
	}

	if flagPruneCommit {
		ok, err := confirmPrune(ctx, cmd, l, opts)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	report, err := reclaim.Prune(ctx, client, l, opts)
	fmt.Fprint(cmd.OutOrStdout(), report.String())
	return err
}

func confirmPrune(ctx context.Context, cmd *cobra.Command, l *ledger.Ledger, opts reclaim.PruneOptions) (bool, error) {
	groups, err := l.Groups(ctx, ledger.GroupFilter{
		Verdict:       ledger.VerdictQuarantined,
		AppliedBefore: time.Now().Add(-opts.Age),
	})
	if err != nil {
		return false, err
	}
	if opts.Limit > 0 && len(groups) > opts.Limit {
		groups = groups[:opts.Limit]
	}
	summary := confirm.Summary{Action: "delete quarantined files", GroupCount: len(groups)}
	for _, g := range groups {
		summary.SceneCount += len(g.SceneIDs) - 1
		summary.ReclaimableBytes += g.Reclaimable
	}
	return confirm.PromptYES(cmd.InOrStdin(), cmd.OutOrStdout(), summary, flagPruneYes)
}

var (
	flagLogLimit  int
	flagLogLedger string
)

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Print the mutation log — every write custodian made, newest first",
		Long: `Each row is one write, recorded before it was sent and stamped with its
outcome afterwards, so a run that died mid-way still shows what it was
attempting. A dry run writes nothing here.`,
		RunE: runLog,
	}
	cmd.Flags().IntVar(&flagLogLimit, "limit", 50, "how many entries to print (0 = all)")
	cmd.Flags().StringVar(&flagLogLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runLog(cmd *cobra.Command, _ []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagLogLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	entries, err := l.Mutations(context.Background(), flagLogLimit)
	if err != nil {
		return err
	}
	fmt.Fprint(cmd.OutOrStdout(), formatMutations(entries))
	return nil
}

// formatMutations renders the mutation log as the lines `log` prints.
func formatMutations(entries []ledger.Mutation) string {
	if len(entries) == 0 {
		return "(the mutation log is empty — nothing has been written to Stash)\n"
	}
	var b strings.Builder
	for _, m := range entries {
		fmt.Fprintf(&b, "%s  %-8s %-7s %s\n", m.At, m.Kind, outcomeOf(m.OK), m.Target)
		if m.From != "" || m.To != "" {
			fmt.Fprintf(&b, "    %s → %s\n", orDash(m.From), orDash(m.To))
		}
		if m.Note != "" {
			fmt.Fprintf(&b, "    %s\n", m.Note)
		}
	}
	fmt.Fprintf(&b, "\n%d entries\n", len(entries))
	return b.String()
}

func outcomeOf(ok *bool) string {
	switch {
	case ok == nil:
		return "PENDING"
	case *ok:
		return "ok"
	default:
		return "FAILED"
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// pick returns the first non-empty of its arguments, so a flag beats config
// and config beats the package default.
func pick(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseAge accepts Go durations plus the day and week suffixes a retention
// window is naturally written in: "30d", "6w", "720h".
func parseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return reclaim.DefaultAge, nil
	}
	for suffix, unit := range map[string]time.Duration{
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
	} {
		if !strings.HasSuffix(s, suffix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, suffix), 64)
		if err != nil {
			return 0, fmt.Errorf("--age %q: %w", s, err)
		}
		return time.Duration(n * float64(unit)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--age %q: want something like 30d, 6w or 720h", s)
	}
	return d, nil
}
