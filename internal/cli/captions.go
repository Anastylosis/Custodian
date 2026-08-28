package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	"github.com/Anastylosis/Custodian/internal/subs"
	"github.com/Anastylosis/Custodian/internal/vfs"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

// captions is `subs` (internal/subs, unchanged) re-pointed at the ledger for
// its scene lookup, per REBUILD.md §7 phase 5a item 3. Everything about how
// a subtitle is matched to a scene — the studio/code, title-similarity and
// runtime signals — is exactly what `subs` already does; only where the
// scene list comes from has moved.

var (
	flagCaptionsDir           string
	flagCaptionsRecursive     bool
	flagCaptionsJSON          string
	flagCaptionsCandidates    int
	flagCaptionsIncludeLikely bool
	flagCaptionsCommit        bool
	flagCaptionsYes           bool
	flagCaptionsLedger        string
)

func newCaptionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "captions",
		Short: "Match loose subtitle files to scenes and move them into place",
		Long: `Point this at a folder of loose subtitles and it works out which scene each
one belongs to: studio and code, title similarity, and runtime, combined.
The scene list comes from the ledger, so it costs nothing to re-run.

"captions scan" moves nothing. "captions apply --commit" places the matches,
moving each subtitle next to its video and stamping the language it detected
from the file's own text.`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Match a folder of subtitles against the ledger and report",
		RunE:  runCaptionsScan,
	}
	scanCmd.Flags().StringVar(&flagCaptionsDir, "dir", "", "folder holding the subtitles (Stash-space path, e.g. /data/webm)")
	scanCmd.Flags().BoolVar(&flagCaptionsRecursive, "recursive", true, "descend into subdirectories")
	scanCmd.Flags().StringVar(&flagCaptionsJSON, "json", "", "also write the full result to this file")
	scanCmd.Flags().IntVar(&flagCaptionsCandidates, "candidates", 3, "how many candidates to show per subtitle")
	scanCmd.Flags().StringVar(&flagCaptionsLedger, "ledger", "", ledgerFlagUsage)
	_ = scanCmd.MarkFlagRequired("dir")

	applyCmd := &cobra.Command{
		Use:   "apply",
		Short: "Move matched subtitles next to their videos",
		Long: `Moves each matched subtitle next to its scene's video, named
<video-stem>.<lang>.srt so Stash attaches it with the right language.

Only CONFIRMED matches are moved by default; --include-likely widens
that. AMBIGUOUS and UNMATCHED subtitles are always left alone.

An existing destination is never overwritten. After applying, run a
Stash scan over the affected folders so the captions are picked up.`,
		RunE: runCaptionsApply,
	}
	applyCmd.Flags().StringVar(&flagCaptionsDir, "dir", "", "folder holding the subtitles (Stash-space path)")
	applyCmd.Flags().BoolVar(&flagCaptionsRecursive, "recursive", true, "descend into subdirectories")
	applyCmd.Flags().BoolVar(&flagCaptionsIncludeLikely, "include-likely", false, "also move LIKELY matches, not just CONFIRMED")
	applyCmd.Flags().BoolVar(&flagCaptionsCommit, "commit", false, "actually move files (default is dry-run)")
	applyCmd.Flags().BoolVar(&flagCaptionsYes, "yes", false, "bypass the interactive YES prompt")
	applyCmd.Flags().StringVar(&flagCaptionsLedger, "ledger", "", ledgerFlagUsage)
	_ = applyCmd.MarkFlagRequired("dir")

	cmd.AddCommand(scanCmd, applyCmd)
	return cmd
}

// captionsEnv is everything both subcommands need. libFS is where the
// library lives; srcFS is where the subtitles being scanned live — usually
// the same, but not when the library is on a NAS and the subtitles are on
// the machine running the tool.
type captionsEnv struct {
	libFS vfs.FS
	srcFS vfs.FS
	mp    *pathmap.Mapper
	ix    *subs.Index
	dir   string
}

func captionsSetup(ctx context.Context, cmd *cobra.Command, cfg *config.Config, l *ledger.Ledger) (
	env *captionsEnv, cleanup func(), err error,
) {
	out := cmd.OutOrStdout()
	cleanup = func() {}

	mp, err := pathMapper(cfg)
	if err != nil {
		return nil, cleanup, fmt.Errorf("sidecars.path_map: %w", err)
	}

	libFS, cleanup, err := openLibraryFS(ctx, cfg)
	if err != nil {
		return nil, func() {}, err
	}

	srcFS, dir := libFS, flagCaptionsDir
	if local, ok := localDirCandidate(flagCaptionsDir); ok {
		srcFS, dir = vfs.NewLocal(), local
	} else if mapped, mErr := mp.ToLocal(flagCaptionsDir); mErr == nil {
		dir = mapped
	} else {
		fmt.Fprintf(out, "no path_map rule covers %s — reading it as a path on %s\n",
			flagCaptionsDir, libFS.Describe())
	}

	if srcFS == libFS {
		fmt.Fprintf(out, "reading subtitles via %s\n", srcFS.Describe())
	} else {
		fmt.Fprintf(out, "reading subtitles via %s, library on %s\n",
			srcFS.Describe(), libFS.Describe())
	}

	fmt.Fprintln(out, "fetching performer and studio names…")
	names, err := subs.CreatorNames(ctx, stash.NewClient(cfg.Stash.URL, stash.WithAPIKey(cfg.StashAPIKey())))
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	vocab := subs.NewVocab(names)

	fmt.Fprintln(out, "reading scenes from the ledger…")
	scenes, err := sceneRefsFromLedger(ctx, l)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	ix := subs.NewIndex(scenes, vocab)
	fmt.Fprintf(out, "indexed %d scenes, %d creator words\n\n", ix.Len(), vocab.Len())
	return &captionsEnv{libFS: libFS, srcFS: srcFS, mp: mp, ix: ix, dir: dir}, cleanup, nil
}

// sceneRefsFromLedger converts the ledger's filing inputs into the matcher's
// compact form — the ledger read that replaces subs's old FindAllScenesPage
// pagination.
func sceneRefsFromLedger(ctx context.Context, l *ledger.Ledger) ([]subs.SceneRef, error) {
	scenes, err := l.FilingInputs(ctx, ledger.FilingFilter{})
	if err != nil {
		return nil, err
	}
	out := make([]subs.SceneRef, 0, len(scenes))
	for _, s := range scenes {
		out = append(out, subs.SceneRef{
			ID: s.SceneID, Title: s.Title, Date: s.Date, Studio: s.StudioName,
			Performers: s.Performers,
			Path:       s.Path,
			Stem:       trimExt(pathmap.Base(s.Path)),
			Duration:   time.Duration(s.Duration * float64(time.Second)),
		})
	}
	return out, nil
}

// captionsCollectAndMatch scans the folder and matches everything not
// already attached to a scene.
func captionsCollectAndMatch(ctx context.Context, cmd *cobra.Command, env *captionsEnv, cfg *config.Config) (
	[]subs.Match, int, error,
) {
	out := cmd.OutOrStdout()
	fs, ix, localDir := env.srcFS, env.ix, env.dir
	found, problems, err := subs.Collect(ctx, fs, localDir, cfg.Sidecars.Extensions, flagCaptionsRecursive)
	if err != nil {
		return nil, 0, fmt.Errorf("scan %s: %w", localDir, err)
	}
	for _, p := range problems {
		fmt.Fprintf(out, "  ⚠ %s\n", p)
	}
	if len(found) == 0 {
		return nil, 0, nil
	}

	var matches []subs.Match
	var attached int
	for _, s := range found {
		ok, err := subs.AlreadyAttached(ctx, fs, s, subs.DefaultVideoExtensions)
		if err == nil && ok {
			attached++
			continue
		}
		matches = append(matches, ix.Match(s, flagCaptionsCandidates))
	}
	return matches, attached, nil
}

func runCaptionsScan(cmd *cobra.Command, _ []string) error {
	cfg, l, _, cleanup, err := loadLedgerAndClient(flagCaptionsLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	env, fsCleanup, err := captionsSetup(ctx, cmd, cfg, l)
	if err != nil {
		return err
	}
	defer fsCleanup()

	matches, attached, err := captionsCollectAndMatch(ctx, cmd, env, cfg)
	if err != nil {
		return err
	}
	if len(matches) == 0 && attached == 0 {
		fmt.Fprintf(out, "No subtitle files found under %s\n", flagCaptionsDir)
		return nil
	}

	sum := subs.Summarize(matches, attached)
	if err := subs.PrintReport(out, matches, sum, flagCaptionsCandidates); err != nil {
		return err
	}
	if flagCaptionsJSON != "" {
		f, err := os.Create(flagCaptionsJSON)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		if err := subs.WriteJSON(f, matches, sum); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nFull result written to %s\n", flagCaptionsJSON)
	}
	fmt.Fprintln(out, "\nNothing was moved. Use `custodian captions apply --dir ... --commit` to place these.")
	return nil
}

func runCaptionsApply(cmd *cobra.Command, _ []string) error {
	cfg, l, _, cleanup, err := loadLedgerAndClient(flagCaptionsLedger)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	env, fsCleanup, err := captionsSetup(ctx, cmd, cfg, l)
	if err != nil {
		return err
	}
	defer fsCleanup()

	flagCaptionsCandidates = 3
	matches, attached, err := captionsCollectAndMatch(ctx, cmd, env, cfg)
	if err != nil {
		return err
	}

	plan := subs.BuildPlacement(matches, env.mp, flagCaptionsIncludeLikely)
	mode := "DRY RUN"
	if flagCaptionsCommit {
		mode = "COMMIT"
	}
	fmt.Fprintf(out, "=== captions apply (%s) ===\n", mode)
	fmt.Fprintf(out, "  already attached: %d\n", attached)
	fmt.Fprintf(out, "  to place:         %d\n", len(plan.Moves))
	fmt.Fprintf(out, "  left alone:       %d\n", len(plan.Skipped))
	for _, m := range plan.Moves {
		fmt.Fprintf(out, "  [%s] %s\n      → %s\n", m.Verdict, pathmap.Base(m.SrcStash), m.DstStash)
	}
	for _, s := range plan.Skipped {
		fmt.Fprintf(out, "  (skip: %s) %s\n", s.Reason, pathmap.Base(s.SrcStash))
	}
	if len(plan.Moves) == 0 {
		return nil
	}
	if !flagCaptionsCommit {
		fmt.Fprintln(out, "\nDry run. Re-run with --commit to move these.")
		return nil
	}

	ok, err := confirm.PromptYES(os.Stdin, out, confirm.Summary{
		Action:     "move subtitle files",
		GroupCount: len(plan.Moves),
		SceneCount: len(plan.Moves),
	}, flagCaptionsYes)
	if err != nil || !ok {
		return err
	}

	mover, err := sidecar.NewMover(env.libFS, env.mp, cfg.Sidecars.Extensions)
	if err != nil {
		return err
	}
	// Subtitles may live on a different machine from the library; the mover
	// streams across the two when they differ.
	mover = mover.WithSource(env.srcFS)
	res := mover.Apply(ctx, plan.PlannedMoves())
	fmt.Fprintf(out, "\nmoved:   %d\n", len(res.Moved))
	fmt.Fprintf(out, "skipped: %d\n", len(res.Skipped))
	fmt.Fprintf(out, "failed:  %d\n", len(res.Failed))
	for _, s := range res.Skipped {
		fmt.Fprintf(out, "  skip %s: %s\n", pathmap.Base(s.Move.SrcStash), s.Reason)
	}
	for _, f := range res.Failed {
		fmt.Fprintf(out, "  FAIL %s: %v\n", pathmap.Base(f.Move.SrcStash), f.Err)
	}
	if len(res.Moved) > 0 {
		fmt.Fprintln(out, "\nRun a Stash scan over the affected folders so the captions attach.")
	}
	return nil
}
