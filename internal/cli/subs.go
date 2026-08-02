package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wasylq/StashJanitor/internal/config"
	"github.com/Wasylq/StashJanitor/internal/confirm"
	"github.com/Wasylq/StashJanitor/internal/pathmap"
	"github.com/Wasylq/StashJanitor/internal/sidecar"
	"github.com/Wasylq/StashJanitor/internal/stash"
	"github.com/Wasylq/StashJanitor/internal/subs"
	"github.com/Wasylq/StashJanitor/internal/vfs"
	"github.com/spf13/cobra"
)

var (
	flagSubsDir           string
	flagSubsRecursive     bool
	flagSubsJSON          string
	flagSubsCandidates    int
	flagSubsPerPage       int
	flagSubsIncludeLikely bool
	flagSubsCommit        bool
	flagSubsYes           bool
)

func newSubsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subs",
		Short: "Match loose subtitle files to Stash scenes",
		Long: `Point this at a folder of subtitles and it works out which scene each
one belongs to.

Stash only sees a subtitle when it sits next to its video and is named
<video-stem>.srt or <video-stem>.<lang>.srt. A subtitle downloaded under
a scraper's original filename, or left behind when its video was moved,
is therefore invisible — the caption simply never appears on the scene.

Matching them back cannot rely on filenames alone, because the two names
often share very little. Three independent signals are combined:

  studio/JAV code   near-decisive when present (VENU-765, RCTD-590)
  title similarity  after stripping scraper noise and folding "step", and
                    after separating creator handles (taken from your own
                    performer and studio names) from title words
  runtime           the subtitle's last cue against the scene duration

Runtime is what makes it trustworthy. Two scenes can share a title, a
performer and a studio, but not a length — in practice the correct match
lands within seconds while a same-titled decoy sits minutes away.

Nothing is moved by "scan". Use "subs apply --commit" to place the
matches, which moves each subtitle next to its video and stamps the
language it detected from the file's own text.`,
	}

	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Match a folder of subtitles against the library and report",
		RunE:  runSubsScan,
	}
	scanCmd.Flags().StringVar(&flagSubsDir, "dir", "", "folder holding the subtitles (Stash-space path, e.g. /data/webm)")
	scanCmd.Flags().BoolVar(&flagSubsRecursive, "recursive", true, "descend into subdirectories")
	scanCmd.Flags().StringVar(&flagSubsJSON, "json", "", "also write the full result to this file")
	scanCmd.Flags().IntVar(&flagSubsCandidates, "candidates", 3, "how many candidates to show per subtitle")
	scanCmd.Flags().IntVar(&flagSubsPerPage, "per-page", 2000, "page size when fetching scenes from Stash")
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
		RunE: runSubsApply,
	}
	applyCmd.Flags().StringVar(&flagSubsDir, "dir", "", "folder holding the subtitles (Stash-space path)")
	applyCmd.Flags().BoolVar(&flagSubsRecursive, "recursive", true, "descend into subdirectories")
	applyCmd.Flags().BoolVar(&flagSubsIncludeLikely, "include-likely", false, "also move LIKELY matches, not just CONFIRMED")
	applyCmd.Flags().IntVar(&flagSubsPerPage, "per-page", 2000, "page size when fetching scenes from Stash")
	applyCmd.Flags().BoolVar(&flagSubsCommit, "commit", false, "actually move files (default is dry-run)")
	applyCmd.Flags().BoolVar(&flagSubsYes, "yes", false, "bypass the interactive YES prompt")
	_ = applyCmd.MarkFlagRequired("dir")

	cmd.AddCommand(scanCmd, applyCmd)
	return cmd
}

// subsEnv is everything both subcommands need. libFS is where the library
// lives; srcFS is where the subtitles being scanned live. They are usually the
// same, but not when the library is on a NAS and the subtitles are on the
// machine running the tool.
type subsEnv struct {
	libFS vfs.FS
	srcFS vfs.FS
	mp    *pathmap.Mapper
	ix    *subs.Index
	dir   string // the source directory, resolved for srcFS
}

func subsSetup(ctx context.Context, cmd *cobra.Command, cfg *config.Config, client *stash.Client) (
	env *subsEnv, cleanup func(), err error,
) {
	out := cmd.OutOrStdout()
	cleanup = func() {}

	rules := make([]pathmap.Rule, 0, len(cfg.Sidecars.PathMap))
	for _, r := range cfg.Sidecars.PathMap {
		rules = append(rules, pathmap.Rule{StashPrefix: r.Stash, LocalPrefix: r.Local})
	}
	mp, err := pathmap.New(rules)
	if err != nil {
		return nil, cleanup, fmt.Errorf("sidecars.path_map: %w", err)
	}

	libFS, cleanup, err := openSubsFS(ctx, cfg)
	if err != nil {
		return nil, func() {}, err
	}

	// Where do the subtitles actually live? A folder that exists on this
	// machine is read locally even when the library is remote, so "point at a
	// folder" means the same thing wherever the folder happens to be.
	srcFS, dir := libFS, flagSubsDir
	if local, ok := localDirCandidate(flagSubsDir); ok {
		srcFS, dir = vfs.NewLocal(), local
	} else if mapped, mErr := mp.ToLocal(flagSubsDir); mErr == nil {
		dir = mapped
	} else {
		fmt.Fprintf(out, "no path_map rule covers %s — reading it as a path on %s\n",
			flagSubsDir, libFS.Describe())
	}

	if srcFS == libFS {
		fmt.Fprintf(out, "reading subtitles via %s\n", srcFS.Describe())
	} else {
		fmt.Fprintf(out, "reading subtitles via %s, library on %s\n",
			srcFS.Describe(), libFS.Describe())
	}

	fmt.Fprintln(out, "fetching performer and studio names…")
	names, err := client.CreatorNames(ctx)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	vocab := subs.NewVocab(names)

	fmt.Fprintln(out, "fetching scenes from Stash…")
	scenes, err := loadSceneRefs(ctx, client, flagSubsPerPage)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	ix := subs.NewIndex(scenes, vocab)
	fmt.Fprintf(out, "indexed %d scenes, %d creator words\n\n", ix.Len(), vocab.Len())
	return &subsEnv{libFS: libFS, srcFS: srcFS, mp: mp, ix: ix, dir: dir}, cleanup, nil
}

// localDirCandidate reports whether the given path is a directory on this
// machine, expanding a leading ~.
func localDirCandidate(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p, true
		}
		return abs, true
	}
	return "", false
}

// openSubsFS builds the backend from the sidecars config, ignoring
// sidecars.enabled — `subs` is a read-only report unless apply --commit is
// used, so it stays available even when automatic sidecar moving is off.
func openSubsFS(ctx context.Context, cfg *config.Config) (vfs.FS, func(), error) {
	mode := cfg.Sidecars.Access
	if mode == "" || mode == "auto" {
		if cfg.Sidecars.SSH.Host != "" {
			mode = "ssh"
		} else {
			mode = "local"
		}
	}
	switch mode {
	case "ssh":
		sc := cfg.Sidecars.SSH
		conn, err := vfs.NewSSH(ctx, vfs.SSHConfig{
			Host: sc.Host, Port: sc.Port, User: sc.User,
			KeyFile:          sc.KeyFile,
			Passphrase:       osGetenv(sc.PassphraseEnv),
			Password:         osGetenv(sc.PasswordEnv),
			KnownHostsFile:   sc.KnownHostsFile,
			AcceptNewHostKey: sc.AcceptNewHostKey,
		})
		if err != nil {
			return nil, func() {}, err
		}
		return conn, func() { _ = conn.Close() }, nil
	default:
		l := vfs.NewLocal()
		return l, func() {}, nil
	}
}

func osGetenv(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

// loadSceneRefs pages the whole library into the matcher's compact form.
func loadSceneRefs(ctx context.Context, client *stash.Client, perPage int) ([]subs.SceneRef, error) {
	if perPage < 1 {
		perPage = 2000
	}
	var out []subs.SceneRef
	for page := 1; ; page++ {
		res, err := client.FindAllScenesPage(ctx, page, perPage)
		if err != nil {
			return nil, err
		}
		if len(res.Scenes) == 0 {
			break
		}
		for _, sc := range res.Scenes {
			ref := subs.SceneRef{
				ID: sc.ID, Title: sc.Title, Date: sc.Date,
			}
			if sc.Studio != nil {
				ref.Studio = sc.Studio.Name
			}
			for _, p := range sc.Performers {
				ref.Performers = append(ref.Performers, p.Name)
			}
			if len(sc.Files) > 0 {
				f := sc.Files[0]
				ref.Path = f.Path
				ref.Stem = trimExt(pathmap.Base(f.Path))
				ref.Duration = time.Duration(f.Duration * float64(time.Second))
			}
			out = append(out, ref)
		}
		if len(out) >= res.Count {
			break
		}
	}
	return out, nil
}

func trimExt(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return name[:i]
		}
	}
	return name
}

// collectAndMatch scans the folder and matches everything that is not already
// attached to a scene.
func collectAndMatch(ctx context.Context, cmd *cobra.Command, env *subsEnv, cfg *config.Config) (
	[]subs.Match, int, error,
) {
	out := cmd.OutOrStdout()
	fs, ix, localDir := env.srcFS, env.ix, env.dir
	found, problems, err := subs.Collect(ctx, fs, localDir, cfg.Sidecars.Extensions, flagSubsRecursive)
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
		matches = append(matches, ix.Match(s, flagSubsCandidates))
	}
	return matches, attached, nil
}

func runSubsScan(cmd *cobra.Command, args []string) error {
	cfg, _, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	env, fsCleanup, err := subsSetup(ctx, cmd, cfg, client)
	if err != nil {
		return err
	}
	defer fsCleanup()

	matches, attached, err := collectAndMatch(ctx, cmd, env, cfg)
	if err != nil {
		return err
	}
	if len(matches) == 0 && attached == 0 {
		fmt.Fprintf(out, "No subtitle files found under %s\n", flagSubsDir)
		return nil
	}

	sum := subs.Summarize(matches, attached)
	if err := subs.PrintReport(out, matches, sum, flagSubsCandidates); err != nil {
		return err
	}
	if flagSubsJSON != "" {
		f, err := os.Create(flagSubsJSON)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := subs.WriteJSON(f, matches, sum); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nFull result written to %s\n", flagSubsJSON)
	}
	fmt.Fprintln(out, "\nNothing was moved. Use `stash-janitor subs apply --dir ... --commit` to place these.")
	return nil
}

func runSubsApply(cmd *cobra.Command, args []string) error {
	cfg, _, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	env, fsCleanup, err := subsSetup(ctx, cmd, cfg, client)
	if err != nil {
		return err
	}
	defer fsCleanup()

	flagSubsCandidates = 3
	matches, attached, err := collectAndMatch(ctx, cmd, env, cfg)
	if err != nil {
		return err
	}

	plan := subs.BuildPlacement(matches, env.mp, flagSubsIncludeLikely)
	mode := "DRY RUN"
	if flagSubsCommit {
		mode = "COMMIT"
	}
	fmt.Fprintf(out, "=== subs apply (%s) ===\n", mode)
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
	if !flagSubsCommit {
		fmt.Fprintln(out, "\nDry run. Re-run with --commit to move these.")
		return nil
	}

	ok, err := confirm.PromptYES(os.Stdin, out, confirm.Summary{
		Action:     "move subtitle files",
		GroupCount: len(plan.Moves),
		SceneCount: len(plan.Moves),
	}, flagSubsYes)
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
