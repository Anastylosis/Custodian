package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

var (
	flagSyncFull    bool
	flagSyncPerPage int
	flagSyncLedger  string
)

func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Mirror Stash's scene library into the local ledger",
		Long: `Pages Stash's scenes and files into the ledger, incrementally by default:
only scenes updated since the last sync are fetched.

Incremental sync cannot see a scene deleted from Stash outright — there is no
updated_at event for "this row is gone". Run with --full periodically, or
after any bulk deletion in Stash, so what quietly vanished gets tombstoned.`,
		RunE: runSync,
	}
	cmd.Flags().BoolVar(&flagSyncFull, "full", false, "resync the whole library and tombstone what's gone")
	cmd.Flags().IntVar(&flagSyncPerPage, "per-page", 500, "scenes fetched per page")
	cmd.Flags().StringVar(&flagSyncLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	return cmd
}

func runSync(cmd *cobra.Command, _ []string) error {
	_, l, client, cleanup, err := loadLedgerAndClient(flagSyncLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	report, err := mirror.Sync(ctx, client, l, mirror.Options{
		Full:    flagSyncFull,
		PerPage: flagSyncPerPage,
	})
	if err != nil {
		return err
	}

	fmt.Fprint(cmd.OutOrStdout(), formatReport(report))
	return nil
}

// formatReport renders a mirror.Report as the plain-text lines `sync` prints.
func formatReport(report mirror.Report) string {
	s := fmt.Sprintf("sync (%s)\n", report.Kind)
	if report.Since != "" {
		s += fmt.Sprintf("  since:    %s\n", report.Since)
	}
	s += fmt.Sprintf("  pages:    %d\n", report.Pages)
	s += fmt.Sprintf("  scenes:   %d\n", report.Scenes)
	s += fmt.Sprintf("  files:    %d\n", report.Files)
	s += fmt.Sprintf("  gone:     %d\n", report.Gone)
	s += fmt.Sprintf("  duration: %s\n", report.Duration.Round(time.Second))
	return s
}

// loadLedgerAndClient is the boilerplate `sync` and `status` share: parse
// config, open the ledger, build a Stash client. Returns a cleanup func the
// caller MUST defer.
func loadLedgerAndClient(ledgerOverride string) (*config.Config, *ledger.Ledger, *stash.Client, func(), error) {
	cfgPath := flagConfigPath
	if cfgPath == "" {
		cfgPath = config.DefaultConfigPath()
	}
	dbPath := flagDBPath
	if dbPath == "" {
		dbPath = config.DefaultDBPath()
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	ledgerPath := ledgerOverride
	if ledgerPath == "" {
		ledgerPath = defaultLedgerPath(dbPath)
	}
	if err := config.EnsureDir(ledgerPath); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("creating ledger directory: %w", err)
	}

	l, err := ledger.Open(ledgerPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("opening ledger at %s: %w", ledgerPath, err)
	}
	cleanup := func() { _ = l.Close() }

	client := stash.NewClient(cfg.Stash.URL, stash.WithAPIKey(cfg.StashAPIKey()))
	return cfg, l, client, cleanup, nil
}

// defaultLedgerPath derives the ledger's path from the store db path so the
// two SQLite files sit side by side without a separate config knob:
// /data/custodian.sqlite -> /data/custodian.ledger.sqlite.
func defaultLedgerPath(dbPath string) string {
	ext := filepath.Ext(dbPath)
	return strings.TrimSuffix(dbPath, ext) + ".ledger.sqlite"
}
