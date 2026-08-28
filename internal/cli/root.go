// Package cli wires up the cobra command tree and global flags for custodian.
package cli

import (
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// Global flag values, populated by cobra during command setup.
var (
	flagConfigPath string
	flagVerbose    int
)

// chain is the order the commands are meant to be run in, and the list
// `custodian` with no arguments prints. Keeping it here rather than deriving
// it from cobra's alphabetical order is the point: the sequence is the
// product, and a user who runs the tool blind should be told it.
var chain = []struct{ name, what string }{
	{"sync", "mirror Stash's scenes and files into the local ledger"},
	{"scan", "find duplicate-candidate groups from the ledger's signals"},
	{"canon", "fetch canonical runtimes from a stash-box or an FSS catalogue"},
	{"rejudge", "re-classify groups against those runtimes"},
	{"plan", "choose the keeper in every group"},
	{"apply", "union metadata onto the keeper, quarantine the losers"},
	{"prune", "delete quarantined losers once they are old enough"},
	{"undo", "reverse an apply from the mutation log"},
	{"file", "move and rename files to match the organize template"},
	{"captions", "match loose subtitles to scenes and move them into place"},
	{"groups", "list duplicate-candidate groups"},
	{"log", "print the mutation log"},
	{"status", "print where the library stands"},
	{"config", "manage the config file"},
	{"doctor", "check Stash, the ledger and access to the library"},
}

// NewRootCmd builds the root cobra command and attaches all subcommands.
// version/commit/date are injected by main via ldflags at build time.
func NewRootCmd(version, commit, date string) *cobra.Command {
	root := &cobra.Command{
		Use:     "custodian",
		Short:   "Custodian — keep one best copy of every scene in a Stash library",
		Version: version,
		Long: `custodian removes bloat from a library managed by Stash: every scene exists
once, as the best copy you have, and nothing on disk is there twice or larger
than it needs to be.

It works off a local ledger — a mirror of Stash's scenes and files — so every
step after ` + "`sync`" + ` is fast and repeatable. Nothing is deleted on the way: a
loser is quarantined inside the library and only ` + "`prune`" + ` removes it, days
later, after a verified database backup. Every write is dry-run by default and
needs both --commit and a typed YES.

Run the commands in the order below.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			printChain(cmd.OutOrStdout())
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			setupLogger(cmd.ErrOrStderr(), flagVerbose)
			return nil
		},
	}

	// Custom version template showing commit and date alongside the version tag.
	root.SetVersionTemplate(fmt.Sprintf("Custodian %s (commit: %s, built: %s)\n", version, commit, date))

	root.PersistentFlags().StringVar(&flagConfigPath, "config", "", "path to config file (default: ~/.config/custodian/config.yaml or ./config.yaml)")
	root.PersistentFlags().CountVarP(&flagVerbose, "verbose", "v", "increase log verbosity (-v info, -vv debug)")

	root.AddCommand(newSyncCmd())
	root.AddCommand(newScanCmd())
	root.AddCommand(newCanonCmd())
	root.AddCommand(newRejudgeCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newApplyCmd())
	root.AddCommand(newPruneCmd())
	root.AddCommand(newUndoCmd())
	root.AddCommand(newFileCmd())
	root.AddCommand(newCaptionsCmd())
	root.AddCommand(newGroupsCmd())
	root.AddCommand(newLogCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newDoctorCmd())

	return root
}

// printChain writes the chain, one line per command, in run order.
func printChain(w io.Writer) {
	fmt.Fprintln(w, "custodian — one best copy of every scene, nothing on disk twice.")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range chain {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.what)
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Every write is a dry run until you pass --commit.")
	fmt.Fprintln(w, "`custodian <command> --help` for flags; MANUAL.md for the whole story.")
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
