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
)

func newGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "List duplicate-candidate groups from the last scan",
		RunE:  runGroups,
	}
	cmd.Flags().StringVar(&flagGroupsVerdict, "verdict", "", "filter to one verdict: certain, conflicted, dismissed")
	cmd.Flags().BoolVar(&flagGroupsJSON, "json", false, "emit groups as JSON")
	cmd.Flags().IntVar(&flagGroupsLimit, "limit", 0, "limit the number of groups printed (0 = no limit)")
	cmd.Flags().StringVar(&flagGroupsLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
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
