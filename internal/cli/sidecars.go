package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/Anastylosis/StashJanitor/internal/pathmap"
	"github.com/Anastylosis/StashJanitor/internal/sidecar"
	"github.com/Anastylosis/StashJanitor/internal/stash"
	"github.com/spf13/cobra"
)

var (
	flagSidecarsCheckSample int
	flagSidecarsCheckPath   string
)

func newSidecarsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sidecars",
		Short: "Diagnose subtitle/companion-file handling",
		Long: `Sidecars are files that share a video's name — most often subtitles:

    clip_1080p.mp4
    clip_1080p.en.srt
    clip_1080p.es.srt

Stash does not index them as files. It looks next to each video at scan
time and attaches whatever it finds as a property of the scene, so a
caption has no file ID and Stash's moveFiles mutation cannot move it.
When organize moves a video, stash-janitor must move the sidecars itself
— which means it needs real access to the library.

Because Stash reports paths from inside its own process (typically
/data/...), those usually have to be mapped to something reachable from
wherever stash-janitor runs. This command verifies that mapping against
your real library before you rely on it.`,
	}

	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Verify sidecar access and path mapping against the live library",
		RunE:  runSidecarsCheck,
	}
	checkCmd.Flags().IntVar(&flagSidecarsCheckSample, "sample", 5,
		"how many real scene paths to test the mapping against")
	checkCmd.Flags().StringVar(&flagSidecarsCheckPath, "path", "",
		"test one specific Stash path instead of sampling (e.g. /data/Performer/clip.mp4)")

	cmd.AddCommand(checkCmd)
	return cmd
}

func runSidecarsCheck(cmd *cobra.Command, args []string) error {
	cfg, _, client, cleanup, err := loadConfigAndStore()
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	out := cmd.OutOrStdout()

	fmt.Fprintln(out, "=== sidecars check ===")

	if !cfg.Sidecars.Enabled {
		fmt.Fprintln(out, "\nsidecars.enabled is false — subtitles will NOT follow videos when")
		fmt.Fprintln(out, "organize moves or renames them. Set sidecars.enabled: true to fix.")
		return nil
	}

	exts := cfg.Sidecars.Extensions
	if len(exts) == 0 {
		exts = sidecar.DefaultExtensions
	}
	fmt.Fprintf(out, "extensions: %s\n", strings.Join(exts, ", "))

	if len(cfg.Sidecars.PathMap) == 0 {
		fmt.Fprintln(out, "\npath_map: (none) — assuming stash-janitor sees exactly the paths")
		fmt.Fprintln(out, "Stash reports. That is correct only when it runs on the same")
		fmt.Fprintln(out, "filesystem as Stash.")
	} else {
		fmt.Fprintln(out, "\npath_map:")
		for _, r := range cfg.Sidecars.PathMap {
			fmt.Fprintf(out, "  %s → %s\n", r.Stash, r.Local)
		}
	}

	mover, moverCleanup, err := sidecar.Build(ctx, cfg)
	if err != nil {
		fmt.Fprintf(out, "\n✗ could not open the library: %v\n", err)
		return fmt.Errorf("sidecar access is not working")
	}
	defer moverCleanup()
	if mover == nil {
		fmt.Fprintln(out, "\nsidecars.access is off — nothing to check.")
		return nil
	}
	fmt.Fprintf(out, "\n✓ backend reachable: %s\n", mover.Describe())

	// Exercise the mapping against real scene paths — the failure this is
	// designed to catch is a mapping that parses fine but points nowhere.
	var paths []string
	if flagSidecarsCheckPath != "" {
		paths = []string{flagSidecarsCheckPath}
	} else {
		res, err := client.FindAllScenesPage(ctx, 1, flagSidecarsCheckSample)
		if err != nil {
			fmt.Fprintf(out, "\n⚠ could not fetch sample scenes from Stash: %v\n", err)
			return nil
		}
		for _, sc := range res.Scenes {
			if p := primaryPath(sc); p != "" {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		fmt.Fprintln(out, "\n⚠ Stash returned no scenes to sample.")
		return nil
	}

	fmt.Fprintf(out, "\nTesting %d path(s):\n", len(paths))
	var okCount, failCount, sidecarCount int
	for _, p := range paths {
		moves, err := mover.Plan(ctx, p, p)
		if err != nil {
			failCount++
			fmt.Fprintf(out, "  ✗ %s\n      %v\n", p, err)
			continue
		}
		okCount++
		sidecarCount += len(moves)
		fmt.Fprintf(out, "  ✓ %s\n", p)
		for _, m := range moves {
			fmt.Fprintf(out, "      sidecar: %s\n", pathmap.Base(m.SrcLocal))
		}
	}

	fmt.Fprintf(out, "\nreadable: %d   failed: %d   sidecars found: %d\n",
		okCount, failCount, sidecarCount)
	if failCount > 0 {
		fmt.Fprintln(out, "\nSome paths could not be resolved. Fix sidecars.path_map so every")
		fmt.Fprintln(out, "Stash prefix maps to a directory this machine can read, then re-run.")
		return fmt.Errorf("%d of %d sample paths failed", failCount, okCount+failCount)
	}
	fmt.Fprintln(out, "\nSidecars are configured correctly.")
	return nil
}

// primaryPath returns a scene's first file path, or "".
func primaryPath(sc stash.Scene) string {
	if len(sc.Files) == 0 {
		return ""
	}
	return sc.Files[0].Path
}
