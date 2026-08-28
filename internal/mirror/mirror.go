// Package mirror pages Stash's scene library into the ledger, incrementally
// by default and in full when asked.
package mirror

import (
	"context"
	"fmt"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// defaultPerPage is used when Options.PerPage is unset.
const defaultPerPage = 500

// Options controls one Sync call.
type Options struct {
	// Full resyncs the whole library and tombstones what it didn't see.
	Full bool
	// PerPage is the page size requested from Stash. Defaults to 500.
	PerPage int
	// Progress, if set, is called after each page with scenes fetched and
	// the total the first page reported.
	Progress func(done, total int)
}

// Report summarises one Sync call.
type Report struct {
	Kind     string // "full" | "incremental"
	Since    string
	Pages    int
	Scenes   int
	Files    int
	Gone     int
	Duration time.Duration
}

// Sync pages Stash's scenes into the ledger and records a run. On a mid-page
// error it still finishes the run with what it saw so far, recording the
// error in the run's notes, and returns the partial Report alongside it.
func Sync(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts Options) (Report, error) {
	start := time.Now()
	perPage := opts.PerPage
	if perPage <= 0 {
		perPage = defaultPerPage
	}

	kind, since, err := resolveSince(ctx, l, opts.Full)
	if err != nil {
		return Report{}, err
	}

	version, err := client.Version(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("mirror: reading stash version: %w", err)
	}

	runID, err := l.BeginRun(ctx, kind, since)
	if err != nil {
		return Report{}, fmt.Errorf("mirror: beginning run: %w", err)
	}

	filter := stash.SceneFilter{}
	if since != "" {
		filter.UpdatedAfter = since
	}

	pages, scenesSeen, filesSeen, syncErr := syncPages(ctx, client, l, runID, filter, perPage, opts.Progress)

	var goneScenes int
	var notes string
	if syncErr == nil {
		goneScenes, notes, syncErr = tombstoneIfFull(ctx, l, runID, kind)
	}
	if syncErr != nil {
		notes = syncErr.Error()
	}

	if err := l.FinishRun(ctx, runID, ledger.RunSummary{
		ScenesSeen:   scenesSeen,
		FilesSeen:    filesSeen,
		ScenesGone:   goneScenes,
		StashVersion: version,
		Notes:        notes,
	}); err != nil {
		return Report{}, fmt.Errorf("mirror: finishing run: %w", err)
	}

	report := Report{
		Kind:     kind,
		Since:    since,
		Pages:    pages,
		Scenes:   scenesSeen,
		Files:    filesSeen,
		Gone:     goneScenes,
		Duration: time.Since(start),
	}
	return report, syncErr
}

// resolveSince decides full vs. incremental and, for incremental, the
// since filter. An empty high-water mark (a fresh ledger) behaves as full.
func resolveSince(ctx context.Context, l *ledger.Ledger, full bool) (kind, since string, err error) {
	if full {
		return "full", "", nil
	}
	newest, err := l.NewestUpdatedAt(ctx)
	if err != nil {
		return "", "", fmt.Errorf("mirror: reading newest updated_at: %w", err)
	}
	if newest == "" {
		return "full", "", nil
	}
	return "incremental", newest, nil
}

// syncPages pages through every scene matching filter, upserting each page
// in its own ledger transaction.
func syncPages(ctx context.Context, client *stash.Client, l *ledger.Ledger, runID int64,
	filter stash.SceneFilter, perPage int, progress func(done, total int)) (pages, scenes, files int, err error) {
	for page := 1; ; page++ {
		records, total, err := client.FindScenes(ctx, filter, page, perPage)
		if err != nil {
			return pages, scenes, files, fmt.Errorf("mirror: fetching scenes (page %d): %w", page, err)
		}
		pages++
		if len(records) == 0 {
			return pages, scenes, files, nil
		}

		for _, r := range records {
			files += len(r.Files)
		}
		if err := l.UpsertScenes(ctx, runID, records); err != nil {
			return pages, scenes, files, fmt.Errorf("mirror: upserting page %d: %w", page, err)
		}
		scenes += len(records)

		if progress != nil {
			progress(scenes, total)
		}
		if len(records) < perPage {
			return pages, scenes, files, nil
		}
		if err := ctx.Err(); err != nil {
			return pages, scenes, files, err
		}
	}
}

// tombstoneIfFull marks scenes and files unseen by this run as gone. Only
// meaningful for a full sync — an incremental one never asked about most of
// the library.
func tombstoneIfFull(ctx context.Context, l *ledger.Ledger, runID int64, kind string) (goneScenes int, notes string, err error) {
	if kind != "full" {
		return 0, "", nil
	}
	goneScenes, goneFiles, err := l.TombstoneUnseen(ctx, runID)
	if err != nil {
		return 0, "", fmt.Errorf("mirror: tombstoning unseen: %w", err)
	}
	return goneScenes, fmt.Sprintf("tombstoned %d scenes, %d files", goneScenes, goneFiles), nil
}
