package stash

import (
	"context"
	"errors"
	"fmt"

	upstream "github.com/Anastylosis/stash-go"
)

// This file is an adapter, not a client. Every call below is the shared
// client's, named and shaped the way Custodian's workflows talk about them —
// "the losers", "one page of orphans", "the primary file" — so the workflow
// code reads in its own vocabulary while the GraphQL, the retries and the
// error types stay in one place, shared with the other consumers.
//
// A call earns a wrapper here when Custodian's question is narrower than the
// API's: a filter it always passes the same way, a return shape its callers
// want, an argument it must never get wrong.

// ============================================================================
// Workflow A: cross-scene duplicates
// ============================================================================

// FindDuplicateScenes returns groups of perceptually-duplicate scenes.
// distance is the phash hamming distance (0 = identical, 4 = re-encodes,
// >8 risky); durationDiff is the maximum runtime delta in seconds.
//
// At this library's scale (~60k scenes) the response is multi-MB — see
// PLAN.md section 11 for the streaming-decode mitigation.
func (c *Client) FindDuplicateScenes(ctx context.Context, distance int, durationDiff float64) ([][]Scene, error) {
	return c.up.FindDuplicateScenes(ctx, distance, durationDiff)
}

// FindScene fetches one scene by ID. Returns nil if the scene does not exist
// (Stash answers with null data, not an error).
func (c *Client) FindScene(ctx context.Context, id string) (*Scene, error) {
	scene, found, err := c.up.FindScene(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return scene, nil
}

// ============================================================================
// Workflow B: scenes carrying more than one file
// ============================================================================

// FindMultiFileScenesPage returns one page of scenes that have more than one
// file attached. The total is in FindScenesResult.Count so callers can drive
// their own pagination loop.
//
// page is 1-indexed (Stash convention). perPage is capped server-side; 100
// is a sensible default.
func (c *Client) FindMultiFileScenesPage(ctx context.Context, page, perPage int) (*FindScenesResult, error) {
	if page < 1 {
		return nil, errors.New("findMultiFileScenes: page must be >= 1")
	}
	if perPage < 1 {
		return nil, errors.New("findMultiFileScenes: perPage must be >= 1")
	}
	multiFile := true
	return c.scenePage(ctx, upstream.SceneFilter{MultiFile: &multiFile}, page, perPage, "findMultiFileScenes")
}

// ============================================================================
// Tag find-or-create — used by `custodian scenes apply --action tag`
// ============================================================================

// FindOrCreateTag returns a tag with the given name, creating it if it does
// not exist. Idempotent — safe to call repeatedly. Used to materialize the
// _dedupe_loser / _dedupe_keeper tags on first apply.
//
// The shared client checks aliases before creating, so a tag that already
// exists under a name Custodian does not know is reused rather than duplicated.
func (c *Client) FindOrCreateTag(ctx context.Context, name string) (*Tag, error) {
	id, err := c.up.EnsureTag(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("ensureTag(%q): %w", name, err)
	}
	return &Tag{ID: id, Name: name}, nil
}

// BulkAddTag adds a single tag to many scenes in one call. Additive: the
// scenes' existing tags are preserved.
func (c *Client) BulkAddTag(ctx context.Context, sceneIDs []string, tagID string) error {
	if len(sceneIDs) == 0 {
		return nil
	}
	return c.up.AddSceneTags(ctx, []string{tagID}, sceneIDs...)
}

// ============================================================================
// Primary file — workflow B, and workflow A's post-merge cleanup
// ============================================================================

// SetPrimaryFile promotes a file to be the scene's primary one, before the
// others are deleted. The file must already belong to the scene.
func (c *Client) SetPrimaryFile(ctx context.Context, sceneID, fileID string) error {
	return c.up.SetPrimaryFile(ctx, sceneID, fileID)
}

// ============================================================================
// sceneMerge — workflow A's merge action
// ============================================================================

// SceneMergeInput mirrors Stash's SceneMergeInput. Values is the *computed*
// metadata union, NOT the raw loser values — Stash does not auto-union
// metadata, so the caller must compute it. See merge package.
type SceneMergeInput struct {
	Source      []string
	Destination string
	Values      *SceneUpdateVals
	PlayHistory bool
	OHistory    bool
}

// SceneUpdateVals is the subset of a scene update that the merge union engine
// populates. Pointer fields distinguish "leave unchanged" (nil) from "set to
// the empty value" (non-nil pointer to zero).
type SceneUpdateVals struct {
	Title        *string
	Code         *string
	Details      *string
	Director     *string
	URLs         []string
	Date         *string
	Rating100    *int
	Organized    *bool
	StudioID     *string
	GalleryIDs   []string
	PerformerIDs []string
	TagIDs       []string
	StashIDs     []StashIDInput
}

// StashIDInput is what a scene update takes for a stash-box ID. Stash's input
// and output shapes are the same, so this is the read type.
type StashIDInput = StashID

// sceneUpdate renders the computed union as the shared client's update shape.
// The destination id is filled in by the merge itself.
func (v *SceneUpdateVals) sceneUpdate() *upstream.SceneUpdate {
	if v == nil {
		return nil
	}
	return &upstream.SceneUpdate{
		Title:        v.Title,
		Code:         v.Code,
		Details:      v.Details,
		Director:     v.Director,
		URLs:         v.URLs,
		Date:         v.Date,
		Rating100:    v.Rating100,
		Organized:    v.Organized,
		StudioID:     v.StudioID,
		GalleryIDs:   v.GalleryIDs,
		PerformerIDs: v.PerformerIDs,
		TagIDs:       v.TagIDs,
		StashIDs:     v.StashIDs,
	}
}

// SceneMerge folds the losers into the keeper. input.Values must already be
// the computed metadata union — Stash does NOT auto-union scene-level
// metadata.
//
// PlayHistory and OHistory carry the losers' watch times across. Stash
// discards both by default, which is wrong for a deduplicating merge: the
// scenes are the same content, so the times it was watched belong to the copy
// being kept.
func (c *Client) SceneMerge(ctx context.Context, input SceneMergeInput) error {
	if len(input.Source) == 0 {
		return errors.New("sceneMerge: source must contain at least one scene id")
	}
	if input.Destination == "" {
		return errors.New("sceneMerge: destination scene id is required")
	}
	return c.up.MergeScenes(ctx, input.Destination, input.Source, input.Values.sceneUpdate(),
		upstream.MergeOptions{PlayHistory: input.PlayHistory, OHistory: input.OHistory})
}

// ============================================================================
// moveFiles — post-merge rename, and workflow D organize
// ============================================================================

// MoveFilesInput mirrors Stash's MoveFilesInput. DestinationBasename is only
// valid for a single file id.
type MoveFilesInput struct {
	IDs                 []string
	DestinationFolder   string
	DestinationFolderID string
	DestinationBasename string
}

// MoveFiles moves files on disk and updates Stash to match. Used by
// post-merge rename to give the winning file a human-readable basename
// derived from the losing files' structured filenames.
func (c *Client) MoveFiles(ctx context.Context, input MoveFilesInput) error {
	if len(input.IDs) == 0 {
		return nil
	}
	return c.up.MoveFiles(ctx, input.IDs, upstream.MoveTarget{
		Folder:   input.DestinationFolder,
		FolderID: input.DestinationFolderID,
		Basename: input.DestinationBasename,
	})
}

// RenameFile renames exactly one file in place, with no folder change.
func (c *Client) RenameFile(ctx context.Context, fileID, newBasename string) error {
	return c.MoveFiles(ctx, MoveFilesInput{
		IDs:                 []string{fileID},
		DestinationBasename: newBasename,
	})
}

// ============================================================================
// Destructive: scene and file deletion
// ============================================================================

// ScenesDestroy hard-deletes the given scenes AND their files from disk,
// generated content included. Used by `custodian scenes apply --action
// delete` when the user is certain there is nothing worth preserving on the
// losers.
//
// This is the most destructive call in the tool. The cli layer must always
// gate it behind --commit plus an interactive YES (or --yes).
func (c *Client) ScenesDestroy(ctx context.Context, sceneIDs []string) error {
	if len(sceneIDs) == 0 {
		return nil
	}
	return c.up.DeleteScenes(ctx, sceneIDs, upstream.DeleteOptions{
		DeleteFile:      true,
		DeleteGenerated: true,
	})
}

// DeleteFiles removes the given files from Stash's database AND from disk.
//
// This is the "really delete it" mutation. Its neighbour upstream,
// DestroyFiles, only drops the database record and leaves the video where it
// is — which would let the next scan re-add everything this was meant to
// reclaim.
func (c *Client) DeleteFiles(ctx context.Context, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	return c.up.DeleteFiles(ctx, fileIDs...)
}

// ============================================================================
// stash-box: fingerprint submission and orphan lookup
// ============================================================================

// SubmitStashBoxFingerprints uploads the given scenes' fingerprints (phash +
// oshash + md5) to the stash-box at endpoint, so future users who scan the
// same files get an automatic match.
//
// Only scenes already linked to that stash-box contribute — the stash id is
// what says which upstream scene the hashes belong to.
func (c *Client) SubmitStashBoxFingerprints(ctx context.Context, sceneIDs []string, endpoint string) error {
	if len(sceneIDs) == 0 {
		return nil
	}
	_, err := c.up.SubmitFingerprints(ctx, endpoint, sceneIDs...)
	return err
}

// ScrapeMultiScenes asks stash-box for matches to the given scene_ids by
// their fingerprints. The result is parallel to sceneIDs — one entry per
// scene, empty where stash-box recognised nothing.
//
// Stash throttles upstream per its configured max_requests_per_minute, so the
// caller can fire batches as fast as it likes.
func (c *Client) ScrapeMultiScenes(ctx context.Context, endpoint string, sceneIDs []string) ([][]ScrapedScene, error) {
	if endpoint == "" {
		return nil, errors.New("ScrapeMultiScenes: endpoint is required")
	}
	return c.up.ScrapeMultiScenes(ctx, endpoint, sceneIDs)
}

// SetSceneStashIDs replaces a scene's stash_ids list. Used by `custodian
// orphans apply` to write a stash-box match back to Stash.
//
// Note: this REPLACES the list. If the scene already has stash_ids for other
// endpoints, the caller is responsible for unioning them in.
func (c *Client) SetSceneStashIDs(ctx context.Context, sceneID string, stashIDs []StashIDInput) error {
	return c.up.SetSceneStashIDs(ctx, sceneID, stashIDs)
}

// SetSceneTitleDate writes title and/or date onto a scene. Called by orphans
// apply when orphans.write_metadata_on_apply is enabled; the caller is
// responsible for only passing values it is willing to overwrite.
//
// An empty string leaves that field alone rather than clearing it.
func (c *Client) SetSceneTitleDate(ctx context.Context, sceneID, title, date string) error {
	update := upstream.SceneUpdate{ID: sceneID}
	if title != "" {
		update.Title = &title
	}
	if date != "" {
		update.Date = &date
	}
	if update.Title == nil && update.Date == nil {
		return nil // nothing to set
	}
	return c.up.UpdateScene(ctx, update)
}

// ============================================================================
// Whole-library enumeration
// ============================================================================

// FindAllScenesPage returns one page of every scene, unfiltered. Used by
// `custodian organize scan`, which needs the whole library to compute ideal
// paths.
func (c *Client) FindAllScenesPage(ctx context.Context, page, perPage int) (*FindScenesResult, error) {
	return c.scenePage(ctx, upstream.SceneFilter{}, page, perPage, "findAllScenes")
}

// FindOrphanScenesPage returns one page of scenes with no stash_ids attached.
// Used by `custodian orphans scan` to enumerate orphans before querying
// stash-box.
func (c *Client) FindOrphanScenesPage(ctx context.Context, page, perPage int) (*FindScenesResult, error) {
	hasStashID := false
	return c.scenePage(ctx, upstream.SceneFilter{HasStashID: &hasStashID}, page, perPage, "findOrphanScenes")
}

// scenePage is the paging shape every enumeration here wants: one page plus
// the total, so a caller can loop without a second count query.
func (c *Client) scenePage(ctx context.Context, filter upstream.SceneFilter, page, perPage int, what string) (*FindScenesResult, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 100
	}
	scenes, count, err := c.up.FindScenes(ctx, filter, page, perPage)
	if err != nil {
		return nil, fmt.Errorf("%s(page=%d): %w", what, page, err)
	}
	return &FindScenesResult{Count: count, Scenes: scenes}, nil
}

// ============================================================================
// Diagnostics & configuration discovery
// ============================================================================

// LibraryStats holds quick top-level numbers about the user's Stash library.
type LibraryStats struct {
	SceneCount     int     `json:"scene_count"`
	ScenesSizeGB   float64 `json:"scenes_size_gb"`
	ScenesDuration float64 `json:"scenes_duration"`
	WithMetadata   int     `json:"with_metadata"` // scenes that have at least one stash_id
}

// LibraryStats fetches aggregate counts from Stash. Used by `custodian stats`.
//
// Two calls: the server's own totals, plus a count of the scenes carrying
// stash-box metadata, which the totals do not include.
func (c *Client) LibraryStats(ctx context.Context) (*LibraryStats, error) {
	stats, err := c.up.LibraryStats(ctx)
	if err != nil {
		return nil, fmt.Errorf("libraryStats: %w", err)
	}
	hasStashID := true
	_, withMetadata, err := c.up.FindScenes(ctx, upstream.SceneFilter{HasStashID: &hasStashID}, 1, 0)
	if err != nil {
		return nil, fmt.Errorf("libraryStats: counting scenes with metadata: %w", err)
	}
	return &LibraryStats{
		SceneCount:     stats.SceneCount,
		ScenesSizeGB:   stats.ScenesSize / (1024 * 1024 * 1024),
		ScenesDuration: stats.ScenesDuration,
		WithMetadata:   withMetadata,
	}, nil
}

// StashBoxes returns the stash-box endpoints configured in Stash. Used to
// decide which endpoint(s) to query when the user has not passed --endpoint.
func (c *Client) StashBoxes(ctx context.Context) ([]StashBoxConfig, error) {
	return c.up.StashBoxes(ctx)
}

// Version returns Stash's version and build time. Used for connectivity smoke
// tests and to record which server custodian was talking to in scan_runs.
func (c *Client) Version(ctx context.Context) (string, string, error) {
	v, err := c.up.ServerVersion(ctx)
	if err != nil {
		return "", "", err
	}
	return v.Version, v.BuildTime, nil
}

// ============================================================================
// creator vocabulary — performer and studio names, used by `subs` matching
// ============================================================================

// CreatorNames returns every performer and studio name in the library, plus
// their aliases.
//
// Subtitle matching uses these to tell "who made this" apart from "what it is
// called". Deriving the list from Stash rather than hard-coding it means it
// covers the whole library, stays correct as the library grows, and keeps
// personal names out of committed source.
//
// Blank entries are dropped. Stash lets an alias list hold an empty string,
// and a few in any real library do; an empty string is not a name, and
// letting one into a matching vocabulary is the kind of value that matches
// everything.
func (c *Client) CreatorNames(ctx context.Context) ([]string, error) {
	const perPage = 1000
	var out []string

	add := func(names ...string) {
		for _, n := range names {
			if n != "" {
				out = append(out, n)
			}
		}
	}

	for page := 1; ; page++ {
		performers, count, err := c.up.FindPerformers(ctx, upstream.PerformerFilter{}, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("findPerformers(page=%d): %w", page, err)
		}
		if len(performers) == 0 {
			break
		}
		for _, p := range performers {
			add(p.Name)
			add(p.Aliases...)
		}
		if page*perPage >= count {
			break
		}
	}

	for page := 1; ; page++ {
		studios, count, err := c.up.Studios(ctx, page, perPage)
		if err != nil {
			return nil, fmt.Errorf("findStudios(page=%d): %w", page, err)
		}
		if len(studios) == 0 {
			break
		}
		for _, s := range studios {
			add(s.Name)
			add(s.Aliases...)
		}
		if page*perPage >= count {
			break
		}
	}
	return out, nil
}
