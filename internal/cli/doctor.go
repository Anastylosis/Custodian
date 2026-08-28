package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/doctor"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

var (
	flagDoctorLedger string
	flagDoctorSample int
	flagDoctorPath   string
	flagDoctorJSON   bool

	flagDoctorGenerate bool
	flagDoctorCommit   bool
	flagDoctorYes      bool
	flagDoctorLimit    int
)

// newDoctorCmd builds `custodian doctor`, the one diagnostic in the tool.
//
// It exists for a failure the chain cannot report on its own. Stash does not
// index subtitles as files — it looks next to each video at scan time and
// attaches what it finds as a property of the scene — so a caption has no
// file ID and moveFiles cannot carry it. custodian moves sidecars itself,
// which means it needs real filesystem access to the library and a correct
// sidecars.path_map between the paths Stash reports (usually /data/... from
// inside a container) and the paths this machine can reach. A mapping that
// parses cleanly but points nowhere only shows up when a move silently
// leaves the subtitles behind. This checks it against real library paths
// first, and touches nothing.
func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Stash, the ledger and filesystem access to the library",
		Long: `Read-only. Verifies, in order:

  - Stash answers at stash.url
  - the ledger exists and how much it holds
  - the sidecar backend (local or ssh) can be opened
  - sidecars.path_map resolves real library paths to readable ones

...then prints a gap report from the ledger (REBUILD.md §7 phase 0): files
with no phash, files sitting in the quarantine directory that no logged move
put there, scenes with no primary file, scenes tombstoned in the ledger but
still referenced by a live duplicate group, and how the ledger's last sync
compares to what Stash reports now. Each finding suggests an action.

Run it after editing the config, and before the first ` + "`--commit`" + `.

` + "`--generate-phashes`" + ` hands the gap report's phash-less scene ids to Stash's
generate task instead of printing the report. Dry run by default.`,
		RunE: runDoctor,
	}
	cmd.Flags().StringVar(&flagDoctorLedger, "ledger", "", ledgerFlagUsage)
	cmd.Flags().IntVar(&flagDoctorSample, "sample", 5, "how many real library paths to test the mapping against")
	cmd.Flags().StringVar(&flagDoctorPath, "path", "", "test one Stash path instead of sampling (e.g. /data/Performer/clip.mp4)")
	cmd.Flags().BoolVar(&flagDoctorJSON, "json", false, "emit the gap report as JSON instead of the text checks")
	cmd.Flags().BoolVar(&flagDoctorGenerate, "generate-phashes", false, "generate phashes for every live file that has none")
	cmd.Flags().BoolVar(&flagDoctorCommit, "commit", false, "with --generate-phashes, actually start the job")
	cmd.Flags().BoolVar(&flagDoctorYes, "yes", false, "skip the interactive YES prompt (still needs --commit)")
	cmd.Flags().IntVar(&flagDoctorLimit, "limit", 0, "with --generate-phashes, cap how many files are acted on (0 = no limit)")
	return cmd
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	cfg, l, client, cleanup, err := loadLedgerAndClient(flagDoctorLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()

	if flagDoctorGenerate {
		return runDoctorGenerate(ctx, cmd, l, client)
	}

	if flagDoctorJSON {
		rep, err := doctor.Check(ctx, client, l, cfg)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	out := cmd.OutOrStdout()
	var failed int

	version, err := client.Version(ctx)
	if err != nil {
		failed++
		fmt.Fprintf(out, "stash:     unreachable at %s: %v\n", cfg.Stash.URL, err)
	} else {
		fmt.Fprintf(out, "stash:     %s at %s\n", version, cfg.Stash.URL)
	}

	stats, err := l.Stats(ctx)
	switch {
	case err != nil:
		failed++
		fmt.Fprintf(out, "ledger:    unreadable: %v\n", err)
	case stats.ScenesLive == 0:
		fmt.Fprintln(out, "ledger:    empty — run `custodian sync`")
	default:
		fmt.Fprintf(out, "ledger:    %d scenes, %d files\n", stats.ScenesLive, stats.FilesLive)
	}

	failed += doctorSidecars(ctx, cmd, cfg, l)

	fmt.Fprintln(out, "\ngap report:")
	rep, err := doctor.Check(ctx, client, l, cfg)
	if err != nil {
		failed++
		fmt.Fprintf(out, "  could not compute: %v\n", err)
	} else {
		for _, line := range rep.Lines() {
			fmt.Fprintln(out, "  "+line)
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Fprintln(out, "\nAll checks passed.")
	return nil
}

// runDoctorGenerate is `custodian doctor --generate-phashes`: collects the
// scene ids of live files with no phash and, in commit mode, sends them
// through Stash's generate task.
func runDoctorGenerate(ctx context.Context, cmd *cobra.Command, l *ledger.Ledger, client *stash.Client) error {
	out := cmd.OutOrStdout()

	if !flagDoctorCommit {
		rep, err := doctor.GeneratePhashes(ctx, client, l, doctor.GenerateOptions{Limit: flagDoctorLimit})
		if err != nil {
			return err
		}
		fmt.Fprint(out, formatGenerateReport(rep))
		return nil
	}

	ok, err := confirmGenerate(ctx, cmd, l, flagDoctorLimit)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	rep, err := doctor.GeneratePhashes(ctx, client, l, doctor.GenerateOptions{
		Commit: true,
		Limit:  flagDoctorLimit,
		Progress: func(j stash.Job) {
			pct := ""
			if j.Progress != nil && *j.Progress >= 0 {
				pct = fmt.Sprintf(" (%.0f%%)", *j.Progress*100)
			}
			fmt.Fprintf(out, "  job %s: %s%s\n", j.ID, j.Status, pct)
		},
	})
	fmt.Fprint(out, formatGenerateReport(rep))
	return err
}

// confirmGenerate reads the phash gap out of the ledger — no Stash calls —
// so the YES gate can say how many files are about to be generated.
func confirmGenerate(ctx context.Context, cmd *cobra.Command, l *ledger.Ledger, limit int) (bool, error) {
	files, err := l.PhashGapScenes(ctx)
	if err != nil {
		return false, err
	}
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	summary := confirm.Summary{Action: "generate phashes for", SceneCount: len(files)}
	return confirm.PromptYES(cmd.InOrStdin(), cmd.OutOrStdout(), summary, flagDoctorYes)
}

// formatGenerateReport renders a doctor.GenerateReport as the plain-text
// lines `doctor --generate-phashes` prints.
func formatGenerateReport(rep doctor.GenerateReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "generate phashes: %d file(s) with none\n", rep.Count)

	if !rep.Committed {
		if rep.Count == 0 {
			b.WriteString("  nothing to do\n")
			return b.String()
		}
		b.WriteString("  first paths:\n")
		for _, p := range rep.SamplePaths {
			fmt.Fprintf(&b, "    %s\n", p)
		}
		b.WriteString("\nDRY RUN — nothing was sent to Stash. Re-run with --commit to act.\n")
		return b.String()
	}

	if rep.JobID == "" {
		b.WriteString("  nothing to generate\n")
		return b.String()
	}
	fmt.Fprintf(&b, "  job:      %s (%s)\n", rep.JobID, rep.JobStatus)
	fmt.Fprintf(&b, "  resynced: %d scene(s)\n", rep.Resynced)
	return b.String()
}

// doctorSidecars runs the filesystem half of the diagnosis and returns the
// number of failures.
func doctorSidecars(ctx context.Context, cmd *cobra.Command, cfg *config.Config, l *ledger.Ledger) int {
	out := cmd.OutOrStdout()

	if !cfg.Sidecars.Enabled {
		fmt.Fprintln(out, "sidecars:  disabled — subtitles will NOT follow videos that `file` moves")
		return 0
	}

	exts := cfg.Sidecars.Extensions
	if len(exts) == 0 {
		exts = sidecar.DefaultExtensions
	}
	fmt.Fprintf(out, "sidecars:  %s\n", strings.Join(exts, ", "))

	if len(cfg.Sidecars.PathMap) == 0 {
		fmt.Fprintln(out, "path_map:  none — assuming custodian sees the paths Stash reports")
	} else {
		fmt.Fprintln(out, "path_map:")
		for _, r := range cfg.Sidecars.PathMap {
			fmt.Fprintf(out, "             %s -> %s\n", r.Stash, r.Local)
		}
	}

	mover, moverCleanup, err := sidecar.Build(ctx, cfg)
	if err != nil {
		fmt.Fprintf(out, "library:   cannot be opened: %v\n", err)
		return 1
	}
	defer moverCleanup()
	if mover == nil {
		fmt.Fprintln(out, "library:   sidecars.access is off — nothing to check")
		return 0
	}
	fmt.Fprintf(out, "library:   %s\n", mover.Describe())

	paths, err := doctorPaths(ctx, l)
	if err != nil {
		fmt.Fprintf(out, "paths:     %v\n", err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintln(out, "paths:     none in the ledger — run `custodian sync` first")
		return 0
	}

	var ok, bad, found int
	fmt.Fprintf(out, "paths:     testing %d\n", len(paths))
	for _, p := range paths {
		moves, err := mover.Plan(ctx, p, p)
		if err != nil {
			bad++
			fmt.Fprintf(out, "             FAIL %s\n                  %v\n", p, err)
			continue
		}
		ok++
		found += len(moves)
		fmt.Fprintf(out, "             ok   %s\n", p)
		for _, m := range moves {
			fmt.Fprintf(out, "                  sidecar: %s\n", pathmap.Base(m.SrcLocal))
		}
	}
	fmt.Fprintf(out, "           readable %d, failed %d, sidecars found %d\n", ok, bad, found)
	if bad > 0 {
		fmt.Fprintln(out, "\nFix sidecars.path_map so every Stash prefix maps to a directory this")
		fmt.Fprintln(out, "machine can read, then re-run.")
		return 1
	}
	return 0
}

// doctorPaths picks the library paths to test: the one named by --path, or a
// sample from the ledger. Sampling the ledger rather than the server keeps
// the check honest about the paths the rest of the chain will actually act on.
func doctorPaths(ctx context.Context, l *ledger.Ledger) ([]string, error) {
	if flagDoctorPath != "" {
		return []string{flagDoctorPath}, nil
	}
	live, err := l.LiveFilePaths(ctx)
	if err != nil {
		return nil, err
	}
	n := flagDoctorSample
	if n < 1 {
		n = 1
	}
	paths := make([]string, 0, n)
	for p := range live {
		if len(paths) == n {
			break
		}
		paths = append(paths, p)
	}
	return paths, nil
}
