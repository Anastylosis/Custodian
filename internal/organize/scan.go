package organize

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// Entry is one file's proposed move, held in memory. The ledger is the
// record of what actually moved (mutation_log); Entry is never persisted —
// re-run `custodian file` to see the plan again.
type Entry struct {
	SceneID     string
	FileID      string
	CurrentPath string
	TargetPath  string
	Status      string // move|rename|already_correct|skip_no_metadata|conflict
	Reason      string
}

// ScanOptions configures one invocation of `custodian file`.
type ScanOptions struct {
	// Limit caps how many live scenes are considered (0 = no limit).
	Limit int
}

// ScanResult summarizes the scan for the CLI and carries the per-scene entries.
type ScanResult struct {
	ScenesProcessed  int
	Moves            int
	Renames          int
	AlreadyCorrect   int
	SkipNoMetadata   int
	Conflicts        int
	MultiFileWarning int // scenes with >1 file where only the primary gets moved
	SkippedDuplicate int // losers of a quarantined/partial group, left alone: about to leave
	Entries          []Entry
}

// Scan reads every live scene from the ledger, computes its ideal path per
// the template, and returns the plan in memory. Scenes that are the
// non-keeper side of a quarantined or partial duplicate group are skipped
// outright: they are about to leave the library, so filing them is wasted
// work (REBUILD.md §7 phase 5a).
func Scan(ctx context.Context, l *ledger.Ledger, cfg *config.Config, opts ScanOptions) (*ScanResult, error) {
	if l == nil {
		return nil, errors.New("organize.Scan: a ledger is required")
	}
	orgCfg := &cfg.Organize
	if orgCfg.BaseDir == "" {
		return nil, errors.New("organize.base_dir must be set in config")
	}
	if orgCfg.PathTemplate == "" {
		return nil, errors.New("organize.path_template must be set in config")
	}

	scenes, err := l.FilingInputs(ctx, ledger.FilingFilter{Limit: opts.Limit})
	if err != nil {
		return nil, fmt.Errorf("organize: reading filing inputs: %w", err)
	}
	losers, err := l.DuplicateLoserSceneIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("organize: reading duplicate loser scenes: %w", err)
	}
	multi, err := l.MultiFileScenes(ctx)
	if err != nil {
		return nil, fmt.Errorf("organize: reading multi-file scenes: %w", err)
	}
	existing, err := l.LiveFilePaths(ctx)
	if err != nil {
		return nil, fmt.Errorf("organize: reading live file paths: %w", err)
	}

	res := &ScanResult{}
	targetSeen := map[string]*Entry{} // target_path → first entry that claimed it
	var entries []*Entry              // pointers: a later conflict can still flip an earlier entry

	for _, fs := range scenes {
		if losers[fs.SceneID] {
			res.SkippedDuplicate++
			continue
		}
		res.ScenesProcessed++
		if _, ok := multi[fs.SceneID]; ok {
			res.MultiFileWarning++
		}

		scene, file := filingScene(fs)
		target, skipReason := ComputeTargetPath(scene, file, orgCfg)

		e := &Entry{SceneID: fs.SceneID, FileID: fs.FileID, CurrentPath: fs.Path}
		switch {
		case skipReason != "":
			e.Status, e.Reason, e.TargetPath = "skip_no_metadata", skipReason, fs.Path
			res.SkipNoMetadata++
		case target == fs.Path:
			e.Status, e.TargetPath = "already_correct", target
			res.AlreadyCorrect++
		case path.Dir(target) == path.Dir(fs.Path) && orgCfg.RenameInPlace:
			e.Status, e.TargetPath = "rename", target
			res.Renames++
		case path.Dir(target) == path.Dir(fs.Path):
			e.Status, e.TargetPath, e.Reason = "already_correct", fs.Path, "in correct folder; rename_in_place is off"
			res.AlreadyCorrect++
		default:
			e.Status, e.TargetPath = "move", target
			res.Moves++
		}

		// Conflict detection: a target already occupied by a different
		// scene's file on disk, or by another entry claimed earlier in
		// this same scan.
		if e.Status == "move" || e.Status == "rename" {
			claimTarget(e, target, fs.SceneID, existing, targetSeen, res)
		}

		entries = append(entries, e)
	}

	res.Entries = make([]Entry, len(entries))
	for i, e := range entries {
		res.Entries[i] = *e
	}
	return res, nil
}

// claimTarget resolves target-path collisions, converting e (and, if it was
// the first claimant, the entry that got there first) to "conflict" and
// correcting the move/rename counts it had already been added to.
func claimTarget(e *Entry, target, sceneID string, existing map[string]string, targetSeen map[string]*Entry, res *ScanResult) {
	if occupant, occupied := existing[target]; occupied && occupant != sceneID {
		conflict(e, res, fmt.Sprintf("target path already occupied by scene %s", occupant))
		return
	}
	if first, ok := targetSeen[target]; ok {
		if first.Status != "conflict" {
			conflict(first, res, fmt.Sprintf("conflicts with file %s (scene %s)", e.FileID, e.SceneID))
		}
		conflict(e, res, fmt.Sprintf("conflicts with file %s (scene %s)", first.FileID, first.SceneID))
		return
	}
	targetSeen[target] = e
}

// conflict flips e to "conflict", undoing whichever move/rename count it was
// already tallied into.
func conflict(e *Entry, res *ScanResult, reason string) {
	switch e.Status {
	case "move":
		res.Moves--
	case "rename":
		res.Renames--
	}
	e.Status, e.Reason = "conflict", reason
	res.Conflicts++
}

// filingScene builds the stash-go Scene/File pair ComputeTargetPath expects
// out of one ledger.FilingScene. Only the fields extractVars (template.go)
// reads are populated.
func filingScene(fs ledger.FilingScene) (*stash.Scene, *stash.File) {
	var studio *stash.Studio
	if fs.StudioName != "" {
		studio = &stash.Studio{Name: fs.StudioName}
	}
	performers := make([]stash.Performer, len(fs.Performers))
	for i, name := range fs.Performers {
		performers[i] = stash.Performer{Name: name}
	}
	scene := &stash.Scene{ID: fs.SceneID, Title: fs.Title, Date: fs.Date, Studio: studio, Performers: performers}
	file := &stash.File{Basename: fs.Basename, Height: fs.Height}
	return scene, file
}
