// Package cli wires up the cobra command tree and global flags for custodian.
package cli

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
)

// Global flag values, populated by cobra during command setup.
var (
	flagConfigPath string
	flagDBPath     string
	flagVerbose    int
)

// NewRootCmd builds the root cobra command and attaches all subcommands.
// version/commit/date are injected by main via ldflags at build time.
func NewRootCmd(version, commit, date string) *cobra.Command {
	root := &cobra.Command{
		Use:     "custodian",
		Short:   "Custodian — find and resolve duplicate scenes in a Stash library",
		Version: version,
		Long: `custodian is a CLI for safely deduplicating videos managed by Stash (stashapp/stash).

It addresses two distinct problems:
  - Workflow A: cross-scene duplicates (separate Stash scenes with the same content)
  - Workflow B: within-scene multi-file cleanup (a single scene with multiple attached files)

All destructive actions go through Stash's GraphQL API. The one exception is
sidecar files (subtitles): Stash does not index them as files, so its moveFiles
mutation cannot move them, and custodian relocates those itself. See
"custodian sidecars check".

The default action is always the safest one: dry-run for apply commands,
tag-only for cross-scene duplicates, report-only for within-scene files.

See PLAN.md for the full design.`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			setupLogger(cmd.ErrOrStderr(), flagVerbose)
			return nil
		},
	}

	// Custom version template showing commit and date alongside the version tag.
	root.SetVersionTemplate(fmt.Sprintf("Custodian %s (commit: %s, built: %s)\n", version, commit, date))

	root.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config file (default: ~/.config/custodian/config.yaml or ./config.yaml)")
	root.PersistentFlags().StringVar(&flagDBPath, "db", "", "path to local sqlite cache (default: ~/.local/share/custodian/ or ./custodian.sqlite)")
	root.PersistentFlags().CountVarP(&flagVerbose, "verbose", "v", "increase log verbosity (-v info, -vv debug)")

	root.AddCommand(newConfigCmd())
	root.AddCommand(newScenesCmd())
	root.AddCommand(newFilesCmd())
	root.AddCommand(newOrphansCmd())
	root.AddCommand(newOrganizeCmd())
	root.AddCommand(newSidecarsCmd())
	root.AddCommand(newSubsCmd())
	root.AddCommand(newStatsCmd())
	root.AddCommand(newReviewCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newScanCmd())
	root.AddCommand(newGroupsCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newApplyCmd())
	root.AddCommand(newPruneCmd())
	root.AddCommand(newLogCmd())

	return root
}

// setupLogger installs a slog default handler whose level depends on -v count:
//
//	0 (default) → warn
//	1 (-v)      → info
//	2+ (-vv)    → debug
func setupLogger(w io.Writer, verbose int) {
	level := slog.LevelWarn
	switch {
	case verbose >= 2:
		level = slog.LevelDebug
	case verbose == 1:
		level = slog.LevelInfo
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
	})
	slog.SetDefault(slog.New(handler))
}
