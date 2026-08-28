package mirror

import (
	"context"
	"os"
	"testing"
	"time"

	stash "github.com/Anastylosis/stash-go"
)

// Integration test in this file talks to a real Stash instance and only runs
// when SFX_INT_STASH_URL is set in the environment. Without that env var it
// is skipped, so `go test ./...` stays hermetic.
//
// # SFX_INT_STASH_SEEDED
//
// Pointed at an *empty* Stash, a suite that skips whenever it finds nothing
// passes exactly as happily as one that found everything — which is a CI job
// that cannot go red. SFX_INT_STASH_SEEDED=1 is the promise that the fixture
// library is present, and under it every "nothing here, skip" becomes a
// failure instead. The shared harness
// (Anastylosis/.github/.github/workflows/stash-integration.yml) sets it; a
// developer pointing these tests at their own library does not, and keeps
// the lenient behaviour. internal/evidence and internal/reclaim carry the
// same gate; each is a separate package, so each has its own copy.
//
// The fixtures the flag promises: three scenes (one holding two
// byte-identical files), one phash-duplicate pair sharing a fabricated
// stash_id, one performer and one studio.
func integrationClient(t *testing.T) *stash.Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return stash.NewClient(url)
}

// seeded reports whether the caller has promised the fixture library exists.
func seeded() bool { return os.Getenv("SFX_INT_STASH_SEEDED") == "1" }

func requireSeeded(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if ok {
		return
	}
	if seeded() {
		t.Fatalf("SFX_INT_STASH_SEEDED=1 but "+format, args...)
	}
	t.Skipf(format, args...)
}

// TestIntegrationSync runs a full Sync against the seeded fixture library
// (three scenes, one holding two files, two sharing a stash_id), then checks
// that an incremental Sync right after sees nothing new and a second full
// Sync tombstones nothing.
func TestIntegrationSync(t *testing.T) {
	client := integrationClient(t)
	l := newTestLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	report, err := Sync(ctx, client, l, Options{Full: true})
	if err != nil {
		t.Fatalf("Sync (full): %v", err)
	}
	if report.Kind != "full" {
		t.Errorf("Kind = %q, want full", report.Kind)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	requireSeeded(t, stats.ScenesLive == 3, "ScenesLive = %d, want 3", stats.ScenesLive)
	requireSeeded(t, stats.FilesLive == 4, "FilesLive = %d, want 4", stats.FilesLive)
	requireSeeded(t, stats.ScenesWithMultipleFiles == 1,
		"ScenesWithMultipleFiles = %d, want 1 (the byte-identical take1/take2 pair)", stats.ScenesWithMultipleFiles)
	requireSeeded(t, stats.ScenesWithStashID == 2,
		"ScenesWithStashID = %d, want 2 (the phash-duplicate pair shares one)", stats.ScenesWithStashID)

	incReport, err := Sync(ctx, client, l, Options{})
	if err != nil {
		t.Fatalf("Sync (incremental): %v", err)
	}
	if incReport.Kind != "incremental" {
		t.Errorf("Kind = %q, want incremental", incReport.Kind)
	}
	if incReport.Scenes != 0 {
		t.Errorf("incremental Scenes = %d, want 0 (nothing changed since the full sync)", incReport.Scenes)
	}

	full2, err := Sync(ctx, client, l, Options{Full: true})
	if err != nil {
		t.Fatalf("Sync (second full): %v", err)
	}
	if full2.Gone != 0 {
		t.Errorf("second full sync Gone = %d, want 0 (nothing left the library)", full2.Gone)
	}
}

// TestIntegrationSyncDecodesVideoFields covers what the ledger's counts
// cannot: the per-file video metadata every later step depends on. The
// keeper picks a survivor by resolution tier, then bitrate, then codec
// (REBUILD.md §4.3), so a field that silently decodes as zero would not
// fail anything here — it would quietly turn every comparison into a tie
// and push real duplicates into review.
func TestIntegrationSyncDecodesVideoFields(t *testing.T) {
	client := integrationClient(t)
	l := newTestLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := Sync(ctx, client, l, Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	paths, err := l.LiveFilePaths(ctx)
	if err != nil {
		t.Fatalf("LiveFilePaths: %v", err)
	}
	requireSeeded(t, len(paths) > 0, "the ledger holds no live files after a full sync")
	if len(paths) == 0 {
		return
	}

	sceneIDs := make([]string, 0, len(paths))
	for _, id := range paths {
		sceneIDs = append(sceneIDs, id)
	}
	primaries, err := l.PrimaryFiles(ctx, sceneIDs)
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}
	requireSeeded(t, len(primaries) > 0, "no primary file rows for %d scenes", len(sceneIDs))

	for id, f := range primaries {
		if f.Path == "" {
			t.Errorf("scene %s: file path is empty", id)
		}
		if f.Size == 0 {
			t.Errorf("scene %s: size is 0 — Int64 probably mis-decoded", id)
		}
		if !seeded() {
			continue
		}
		// The fixtures are real ffmpeg-encoded clips Stash has scanned, so
		// every one of these is populated on a healthy sync.
		if f.Width == 0 || f.Height == 0 {
			t.Errorf("scene %s: dimensions are %dx%d; the keeper's tier rule reads these", id, f.Width, f.Height)
		}
		if f.VideoCodec == "" {
			t.Errorf("scene %s: video codec is empty; the fixtures are h264", id)
		}
		if f.Duration <= 0 {
			t.Errorf("scene %s: duration is %v; every verdict compares runtimes", id, f.Duration)
		}
	}
}

// A whole-library walk that repeats or skips scenes is the failure mode
// paging has, and it only shows at a page size small enough to force several
// round trips. per-page 1 over the three fixtures does that.
func TestIntegrationSyncPaginatesStably(t *testing.T) {
	client := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	one := newTestLedger(t)
	small, err := Sync(ctx, client, one, Options{Full: true, PerPage: 1})
	if err != nil {
		t.Fatalf("Sync (per-page 1): %v", err)
	}
	smallStats, err := one.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	many := newTestLedger(t)
	big, err := Sync(ctx, client, many, Options{Full: true, PerPage: 500})
	if err != nil {
		t.Fatalf("Sync (per-page 500): %v", err)
	}
	bigStats, err := many.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	requireSeeded(t, small.Pages > 1, "per-page 1 fetched %d page(s); the fixture library has three scenes", small.Pages)
	if smallStats.ScenesLive != bigStats.ScenesLive || smallStats.FilesLive != bigStats.FilesLive {
		t.Errorf("page size changed the result: per-page 1 saw %d scenes/%d files, per-page 500 saw %d/%d",
			smallStats.ScenesLive, smallStats.FilesLive, bigStats.ScenesLive, bigStats.FilesLive)
	}
	if big.Scenes != small.Scenes {
		t.Errorf("scenes seen differ by page size: %d at 1, %d at 500", small.Scenes, big.Scenes)
	}
}
