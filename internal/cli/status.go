package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/spf13/cobra"
)

var (
	flagStatusLedger string
	flagStatusJSON   bool
)

func newStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the ledger's current view of the library",
		Long:  `The headline numbers for where the library stands: what the last sync saw.`,
		RunE:  runStatus,
	}
	cmd.Flags().StringVar(&flagStatusLedger, "ledger", "", "path to the ledger sqlite file (default: alongside --db)")
	cmd.Flags().BoolVar(&flagStatusJSON, "json", false, "emit the stats as JSON")
	return cmd
}

func runStatus(cmd *cobra.Command, _ []string) error {
	_, l, _, cleanup, err := loadLedgerAndClient(flagStatusLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	stats, err := l.Stats(ctx)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if flagStatusJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}

	fmt.Fprint(out, formatStats(stats))
	return nil
}

// formatStats renders Stats as the plain-text lines `status` prints.
func formatStats(stats ledger.Stats) string {
	s := fmt.Sprintf("scenes:            %d (%d gone)\n", stats.ScenesLive, stats.ScenesGone)
	s += fmt.Sprintf("files:             %d\n", stats.FilesLive)
	s += fmt.Sprintf("scenes >1 file:    %d\n", stats.ScenesWithMultipleFiles)
	s += fmt.Sprintf("with stash_id:     %d\n", stats.ScenesWithStashID)
	s += fmt.Sprintf("without stash_id:  %d\n", stats.ScenesWithoutStashID)
	s += fmt.Sprintf("files no phash:    %d\n", stats.FilesWithoutPhash)
	s += fmt.Sprintf("bytes on disk:     %s\n", confirm.HumanBytes(stats.BytesLive))
	s += fmt.Sprintf("duplicate groups:  %d (certain %d, conflicted %d, dismissed %d)\n",
		stats.GroupsCertain+stats.GroupsConflicted+stats.GroupsDismissed,
		stats.GroupsCertain, stats.GroupsConflicted, stats.GroupsDismissed)
	s += fmt.Sprintf("reclaimable:       %s\n", confirm.HumanBytes(stats.ReclaimableBytes))
	s += fmt.Sprintf("quarantined:       %d groups (%s)\n",
		stats.GroupsQuarantined, confirm.HumanBytes(stats.QuarantinedBytes))
	s += fmt.Sprintf("reclaimed:         %d groups (%s)\n",
		stats.GroupsReclaimed, confirm.HumanBytes(stats.ReclaimedBytes))
	if stats.LastSync != nil {
		s += fmt.Sprintf("last sync:         %s (%s), %d changed\n",
			stats.LastSync.StartedAt, stats.LastSync.Kind, stats.LastSync.ScenesSeen)
	} else {
		s += "last sync:         never — run `custodian sync`\n"
	}
	if stats.LastScan != nil {
		s += fmt.Sprintf("last scan:         %s, %s\n", stats.LastScan.StartedAt, stats.LastScan.Notes)
	} else {
		s += "last scan:         never — run `custodian scan`\n"
	}
	return s
}
