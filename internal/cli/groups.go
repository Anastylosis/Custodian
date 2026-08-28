package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/spf13/cobra"
)

var (
	flagGroupsVerdict string
	flagGroupsJSON    bool
	flagGroupsLimit   int
	flagGroupsLedger  string
	flagDismissReason string
)

func newGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "List duplicate-candidate groups",
		RunE:  runGroups,
	}
	cmd.Flags().StringVar(&flagGroupsVerdict, "verdict", "", "filter to one verdict: certain, conflicted, dismissed, partial, mistag, probable, rejected")
	cmd.Flags().BoolVar(&flagGroupsJSON, "json", false, "emit groups as JSON")
	cmd.Flags().IntVar(&flagGroupsLimit, "limit", 0, "limit the number of groups printed (0 = no limit)")
	cmd.Flags().StringVar(&flagGroupsLedger, "ledger", "", ledgerFlagUsage)

	cmd.AddCommand(newDismissCmd(), newRestoreCmd())
	return cmd
}

// newDismissCmd and newRestoreCmd are how an operator overrules the policy.
//
// A decision is keyed on the group's signature — its sorted file oshashes,
// falling back to a shared stash_id — and never on scene ids, because scene
// ids die on a merge and change on a rescan. So a dismissal survives both,
// and `scan` re-applies it to the group it rebuilds.
func newDismissCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dismiss <signature>",
		Short: "Mark a group not a duplicate, permanently",
		Long: `Records the decision against the group's file hashes, so it survives a
rescan and a merge, and moves the group to the dismissed verdict now.

Take the signature from "custodian groups"; the printed prefix is enough as
long as it is unique.`,
		Args: cobra.ExactArgs(1),
		RunE: runDismiss,
	}
	cmd.Flags().StringVar(&flagDismissReason, "reason", "", "why, recorded with the decision")
	cmd.Flags().StringVar(&flagGroupsLedger, "ledger", "", ledgerFlagUsage)
	return cmd
}

func newRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <signature>",
		Short: "Undo a dismissal so the group is judged again",
		Args:  cobra.ExactArgs(1),
		RunE:  runRestore,
	}
	cmd.Flags().StringVar(&flagGroupsLedger, "ledger", "", ledgerFlagUsage)
	return cmd
}

func runDismiss(cmd *cobra.Command, args []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagGroupsLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	g, err := resolveSignature(ctx, l, args[0])
	if err != nil {
		return err
	}

	reason := flagDismissReason
	if reason == "" {
		reason = "dismissed by the operator"
	}
	if err := l.PutDecision(ctx, ledger.Decision{
		Key:       g.Signature,
		Kind:      "not_duplicate",
		DecidedAt: time.Now().UTC().Format(time.RFC3339),
		Notes:     reason,
	}); err != nil {
		return err
	}
	if err := l.UpdateVerdict(ctx, g.Signature, ledger.VerdictDismissed, reason, nil); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "dismissed %s: %s\n", shortSignature(g.Signature), reason)
	return nil
}

func runRestore(cmd *cobra.Command, args []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagGroupsLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	g, err := resolveSignature(ctx, l, args[0])
	if err != nil {
		return err
	}
	if _, ok, err := l.GetDecision(ctx, g.Signature); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%s carries no decision to restore", shortSignature(g.Signature))
	}
	if err := l.DeleteDecision(ctx, g.Signature); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"restored %s — run `custodian scan` to judge it again\n", shortSignature(g.Signature))
	return nil
}

// resolveSignature accepts either a full signature or the truncated prefix
// `groups` prints, and refuses an ambiguous one rather than picking.
func resolveSignature(ctx context.Context, l *ledger.Ledger, arg string) (ledger.Group, error) {
	arg = strings.TrimSuffix(arg, "…")
	if g, found, err := l.GroupBySignature(ctx, arg); err != nil {
		return ledger.Group{}, err
	} else if found {
		return g, nil
	}

	all, err := l.Groups(ctx, ledger.GroupFilter{})
	if err != nil {
		return ledger.Group{}, err
	}
	var matches []ledger.Group
	for _, g := range all {
		if strings.HasPrefix(g.Signature, arg) {
			matches = append(matches, g)
		}
	}
	switch len(matches) {
	case 0:
		return ledger.Group{}, fmt.Errorf("no group with signature %q — run `custodian groups` to list them", arg)
	case 1:
		return matches[0], nil
	default:
		return ledger.Group{}, fmt.Errorf("%q matches %d groups; give more of the signature", arg, len(matches))
	}
}

func runGroups(cmd *cobra.Command, _ []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagGroupsLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: flagGroupsVerdict})
	if err != nil {
		return err
	}
	if flagGroupsLimit > 0 && len(groups) > flagGroupsLimit {
		groups = groups[:flagGroupsLimit]
	}

	out := cmd.OutOrStdout()
	if flagGroupsJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(groups)
	}

	files, err := l.PrimaryFiles(ctx, allSceneIDs(groups))
	if err != nil {
		return err
	}
	if flagGroupsVerdict == ledger.VerdictProbable {
		fmt.Fprintln(out, "probable groups are review-only: plan does not choose a keeper for them yet")
		fmt.Fprintln(out)
	}
	fmt.Fprint(out, formatGroups(groups, files))
	return nil
}

func allSceneIDs(groups []ledger.Group) []string {
	var ids []string
	for _, g := range groups {
		ids = append(ids, g.SceneIDs...)
	}
	return ids
}

// formatGroups renders groups as the plain-text blocks `groups` prints,
// given each scene's primary file info.
func formatGroups(groups []ledger.Group, files map[string]ledger.PrimaryFile) string {
	var b strings.Builder
	for _, g := range groups {
		formatGroup(&b, g, files)
	}
	return b.String()
}

func formatGroup(b *strings.Builder, g ledger.Group, files map[string]ledger.PrimaryFile) {
	fmt.Fprintf(b, "group %s (%s)\n", shortSignature(g.Signature), g.Verdict)
	fmt.Fprintf(b, "  reason: %s\n", g.Reason)
	b.WriteString("  scenes:\n")
	for _, id := range g.SceneIDs {
		pf := files[id]
		fmt.Fprintf(b, "    %s · %s · %s · %s\n", id, formatSceneDuration(pf.Duration), confirm.HumanBytes(pf.Size), pf.Basename)
	}
	fmt.Fprintf(b, "  reclaimable: %s\n\n", confirm.HumanBytes(g.Reclaimable))
}

func formatSceneDuration(seconds float64) string {
	return time.Duration(seconds * float64(time.Second)).Round(time.Second).String()
}

// shortSignature truncates a group's signature to a readable prefix.
func shortSignature(sig string) string {
	const n = 12
	r := []rune(sig)
	if len(r) <= n {
		return sig
	}
	return string(r[:n]) + "…"
}
