package cli

import (
	"context"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/identify"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

var (
	flagIdentifyVia      string
	flagIdentifyEndpoint string
	flagIdentifyLimit    int
	flagIdentifyCommit   bool
	flagIdentifyYes      bool
	flagIdentifyLedger   string
)

func newIdentifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identify --via url|stash-box",
		Short: "Link scenes to a stash-box, opt-in and never silent",
		Long: `REBUILD.md §0's decision: Custodian does not identify scenes by default, and
never sets stash_ids itself. This command is the opt-in exception, and it
never guesses which route you want — --via is required.

  --via url        scenes with a URL and no stash_id (e.g. from FSS) are
                    asked, through Stash's own URL scraper, what that page
                    says. Empty text fields are filled from the answer;
                    every write reads the plan first, and none happens
                    without --commit.
  --via stash-box   scenes with no stash_id are pre-screened with
                    scrapeMultiScenes; only a single candidate whose
                    duration matches within the trimmed tolerance (5%/90s)
                    is handed to Stash's own MetadataIdentify job.
                    stash-box fingerprint matching has real false
                    positives — read the report before trusting it.

Neither route ever calls sceneUpdate with stash_ids: the identity decision
stays Stash's own MetadataIdentify job. Dry run by default.`,
		RunE: runIdentify,
	}
	cmd.Flags().StringVar(&flagIdentifyVia, "via", "", "url or stash-box (required, no default)")
	cmd.Flags().StringVar(&flagIdentifyEndpoint, "endpoint", "", "stash-box endpoint (--via stash-box; default: sources.stash_box_endpoint, or the server's only configured one)")
	cmd.Flags().IntVar(&flagIdentifyLimit, "limit", 0, "limit the number of scenes considered (0 = no limit)")
	cmd.Flags().BoolVar(&flagIdentifyCommit, "commit", false, "actually write to Stash")
	cmd.Flags().BoolVar(&flagIdentifyYes, "yes", false, "skip the interactive YES prompt (still needs --commit)")
	cmd.Flags().StringVar(&flagIdentifyLedger, "ledger", "", ledgerFlagUsage)
	return cmd
}

func runIdentify(cmd *cobra.Command, _ []string) error {
	if flagIdentifyVia != "url" && flagIdentifyVia != "stash-box" {
		return fmt.Errorf("identify: --via must be url or stash-box (no default) — say which one")
	}

	cfg, l, client, cleanup, err := loadLedgerAndClient(flagIdentifyLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	endpoint := flagIdentifyEndpoint
	if flagIdentifyVia == "stash-box" {
		endpoint, err = resolveIdentifyEndpoint(ctx, cfg, client)
		if err != nil {
			return err
		}
	}

	if flagIdentifyCommit {
		ok, err := confirmIdentify(ctx, cmd, l, flagIdentifyVia, endpoint, flagIdentifyLimit)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	before, err := l.Stats(ctx)
	if err != nil {
		return err
	}

	rep, err := identify.Identify(ctx, client, l, identify.Options{
		Via:      flagIdentifyVia,
		Endpoint: endpoint,
		Limit:    flagIdentifyLimit,
		Commit:   flagIdentifyCommit,
	})
	out := cmd.OutOrStdout()
	fmt.Fprint(out, rep.String())
	if err != nil {
		return err
	}

	if flagIdentifyCommit {
		syncReport, syncErr := mirror.Sync(ctx, client, l, mirror.Options{})
		if syncErr != nil {
			return fmt.Errorf("identify: syncing the ledger after commit: %w", syncErr)
		}
		after, err := l.Stats(ctx)
		if err != nil {
			return err
		}
		fmt.Fprint(out, "\n"+formatReport(syncReport))
		fmt.Fprintf(out, "%d scene(s) gained a stash_id\n", after.ScenesWithStashID-before.ScenesWithStashID)
	}
	return nil
}

// resolveIdentifyEndpoint picks the stash-box for --via stash-box:
// --endpoint, then sources.stash_box_endpoint, then the server's only
// configured stash-box — the same fallback canon.go's stashBoxSource uses.
func resolveIdentifyEndpoint(ctx context.Context, cfg *config.Config, client *stash.Client) (string, error) {
	if endpoint := pick(flagIdentifyEndpoint, cfg.Sources.StashBoxEndpoint); endpoint != "" {
		return endpoint, nil
	}
	boxes, err := client.StashBoxes(ctx)
	if err != nil {
		return "", fmt.Errorf("identify: listing stash-boxes: %w", err)
	}
	if len(boxes) != 1 {
		return "", fmt.Errorf(
			"identify: no --endpoint given and the server has %d stash-boxes configured (want exactly 1 to default to)", len(boxes))
	}
	return boxes[0].Endpoint, nil
}

// confirmIdentify reads the candidate population straight from the ledger —
// no Stash calls, so it is cheap — and shows its count next to the endpoint
// before the YES gate. Identify itself may select fewer once it actually
// scrapes and screens; this is the upper bound.
func confirmIdentify(ctx context.Context, cmd *cobra.Command, l *ledger.Ledger, via, endpoint string, limit int) (bool, error) {
	var ids []string
	var err error
	switch via {
	case "url":
		ids, err = l.ScenesWithURLNoStashID(ctx, limit)
	case "stash-box":
		ids, err = l.ScenesWithoutStashID(ctx, limit)
	}
	if err != nil {
		return false, err
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "considering: %d scene(s) via %s\n", len(ids), via)
	if endpoint != "" {
		fmt.Fprintf(out, "endpoint: %s\n", endpoint)
	}

	summary := confirm.Summary{Action: "identify via " + via, SceneCount: len(ids)}
	return confirm.PromptYES(cmd.InOrStdin(), cmd.OutOrStdout(), summary, flagIdentifyYes)
}
