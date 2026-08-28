package cli

import (
	"context"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/canonical"
	"github.com/Anastylosis/Custodian/internal/config"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

var (
	flagCanonSource   string
	flagCanonEndpoint string
	flagCanonFSSDB    string
	flagCanonFSSJSON  string
	flagCanonVerdict  string
	flagCanonLimit    int
	flagCanonLedger   string
)

func newCanonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "canon",
		Short: "Fetch canonical runtimes from a stash-box or an FSS catalogue",
		Long: `Finds scenes belonging to a live group with the given verdict (conflicted by
default) that carry no canonical duration yet, asks the configured source(s)
about them, and stores whatever each source found.

"custodian rejudge" is what reads canon's results and moves groups between
verdicts; canon itself only fetches and stores.`,
		RunE: runCanon,
	}
	cmd.Flags().StringVar(&flagCanonSource, "source", "", "stash-box, fss, or empty to run every configured source")
	cmd.Flags().StringVar(&flagCanonEndpoint, "endpoint", "", "stash-box endpoint (default: sources.stash_box_endpoint, or the server's only configured one)")
	cmd.Flags().StringVar(&flagCanonFSSDB, "fss-db", "", "path to FSS's sqlite database (default: sources.fss.db)")
	cmd.Flags().StringVar(&flagCanonFSSJSON, "fss-json", "", "directory of FSS's *.json exports (default: sources.fss.json_dir)")
	cmd.Flags().StringVar(&flagCanonVerdict, "verdict", "conflicted", "verdict whose scenes need a canonical")
	cmd.Flags().IntVar(&flagCanonLimit, "limit", 0, "limit the number of scenes looked up (0 = no limit)")
	cmd.Flags().StringVar(&flagCanonLedger, "ledger", "", ledgerFlagUsage)
	return cmd
}

func runCanon(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagCanonLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	sources, err := buildCanonSources(ctx, cfg, client)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("canon: no source available — pass --source, or configure sources.stash_box_endpoint / sources.fss in the config")
	}

	rep, err := canonical.Fetch(ctx, l, sources, canonical.Options{
		Verdicts: []string{flagCanonVerdict},
		Limit:    flagCanonLimit,
	})
	fmt.Fprint(cmd.OutOrStdout(), rep.String())
	return err
}

// buildCanonSources resolves --source into the canonical.Source(s) to run.
// An explicit --source picks exactly one and fails if it can't be
// configured; an empty --source runs every source that a flag or the
// config makes available, silently skipping the rest.
func buildCanonSources(ctx context.Context, cfg *config.Config, client *stash.Client) ([]canonical.Source, error) {
	switch flagCanonSource {
	case "stash-box":
		src, err := stashBoxSource(ctx, cfg, client)
		if err != nil {
			return nil, err
		}
		return []canonical.Source{src}, nil
	case "fss":
		return []canonical.Source{fssSource(cfg)}, nil
	case "":
		var sources []canonical.Source
		if src, err := stashBoxSource(ctx, cfg, client); err == nil {
			sources = append(sources, src)
		}
		if fss := fssSource(cfg); fss.DBPath != "" || fss.JSONDir != "" {
			sources = append(sources, fss)
		}
		return sources, nil
	default:
		return nil, fmt.Errorf("canon: --source must be stash-box or fss, got %q", flagCanonSource)
	}
}

// stashBoxSource resolves the endpoint to scrape: --endpoint, then
// sources.stash_box_endpoint, then the server's only configured stash-box
// if it has exactly one.
func stashBoxSource(ctx context.Context, cfg *config.Config, client *stash.Client) (canonical.StashBoxSource, error) {
	endpoint := pick(flagCanonEndpoint, cfg.Sources.StashBoxEndpoint)
	if endpoint == "" {
		boxes, err := client.StashBoxes(ctx)
		if err != nil {
			return canonical.StashBoxSource{}, fmt.Errorf("canon: listing stash-boxes: %w", err)
		}
		if len(boxes) != 1 {
			return canonical.StashBoxSource{}, fmt.Errorf(
				"canon: no --endpoint given and the server has %d stash-boxes configured (want exactly 1 to default to)", len(boxes))
		}
		endpoint = boxes[0].Endpoint
	}
	return canonical.StashBoxSource{Client: client, Endpoint: endpoint}, nil
}

func fssSource(cfg *config.Config) *canonical.FSSSource {
	return &canonical.FSSSource{
		DBPath:  pick(flagCanonFSSDB, cfg.Sources.FSS.DB),
		JSONDir: pick(flagCanonFSSJSON, cfg.Sources.FSS.JSONDir),
	}
}
