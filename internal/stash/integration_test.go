package stash

import (
	"context"
	"os"
	"testing"
	"time"
)

// Integration tests in this file talk to a real Stash instance and only run
// when SFX_INT_STASH_URL is set in the environment. Without that env var
// they're skipped, so `go test ./...` stays hermetic.
//
// To run locally:
//
//	SFX_INT_STASH_URL=http://localhost:9999 \
//	  go test ./internal/stash/... -run Integration -v
//
// All operations are read-only.

func integrationClient(t *testing.T) *Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return NewClient(url, os.Getenv("SFX_INT_STASH_API_KEY"))
}

func TestIntegrationVersion(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, _, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version == "" {
		t.Error("expected non-empty version")
	}
	t.Logf("stash version: %s", version)
}

func TestIntegrationFindMultiFileScenesPage(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page, err := c.FindMultiFileScenesPage(ctx, 1, 5)
	if err != nil {
		t.Fatalf("FindMultiFileScenesPage: %v", err)
	}
	t.Logf("multi-file scenes total count: %d (got %d on page 1)", page.Count, len(page.Scenes))
	for _, s := range page.Scenes {
		if len(s.Files) < 2 {
			t.Errorf("scene %s has %d files; filter is supposed to return file_count > 1", s.ID, len(s.Files))
		}
	}
}

func TestIntegrationFindDuplicateScenesIdentical(t *testing.T) {
	c := integrationClient(t)
	// distance=0 keeps the response small (only byte-equivalent phashes).
	// duration_diff=1.0 matches our default.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	groups, err := c.FindDuplicateScenes(ctx, 0, 1.0)
	if err != nil {
		t.Fatalf("FindDuplicateScenes: %v", err)
	}
	t.Logf("found %d duplicate groups at distance=0", len(groups))
	for i, g := range groups {
		if len(g) < 2 {
			t.Errorf("group %d has %d scenes; expected >= 2", i, len(g))
		}
		// Spot-check that we actually got rich data, not just IDs.
		for _, s := range g {
			if s.ID == "" {
				t.Errorf("group %d: scene with empty ID", i)
			}
		}
	}
}

func TestIntegrationFindScene(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pick any scene id we know exists. We use scene 1 because every Stash
	// install has at least one scene.
	scene, err := c.FindScene(ctx, "1")
	if err != nil {
		t.Fatalf("FindScene(1): %v", err)
	}
	if scene == nil {
		t.Skip("scene 1 not found; can't test full-scene shape against this stash")
	}
	if scene.ID != "1" {
		t.Errorf("scene.ID = %q, want 1", scene.ID)
	}
	if len(scene.Files) == 0 {
		t.Error("expected scene to have at least one file")
	} else {
		f := scene.Files[0]
		if f.Path == "" {
			t.Error("file path is empty")
		}
		if f.Size == 0 {
			t.Error("file size is 0; the JSON unmarshal probably mis-decoded Int64")
		}
		t.Logf("scene 1 primary file: %s (%d bytes, %dx%d, %s)", f.Basename, f.Size, f.Width, f.Height, f.VideoCodec)
	}
}

// The orphan filter changed shape when this moved onto the shared client:
// Custodian asked for stash_id_count == 0, the shared filter asks whether a
// stash_id_endpoint is null. They agree on this library, and this is what
// says so if that ever stops being true.
func TestIntegrationOrphanCountAgreesWithStats(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stats, err := c.LibraryStats(ctx)
	if err != nil {
		t.Fatalf("LibraryStats: %v", err)
	}
	orphans, err := c.FindOrphanScenesPage(ctx, 1, 1)
	if err != nil {
		t.Fatalf("FindOrphanScenesPage: %v", err)
	}
	if stats.SceneCount == 0 {
		t.Skip("empty library")
	}
	// Every scene either carries a stash id or does not.
	if orphans.Count+stats.WithMetadata != stats.SceneCount {
		t.Errorf("orphans(%d) + with-metadata(%d) = %d, want the library total %d",
			orphans.Count, stats.WithMetadata, orphans.Count+stats.WithMetadata, stats.SceneCount)
	}
	for _, s := range orphans.Scenes {
		if len(s.StashIDs) != 0 {
			t.Errorf("scene %s came back as an orphan with %d stash id(s)", s.ID, len(s.StashIDs))
		}
	}
	t.Logf("%d orphans, %d with metadata, %d total",
		orphans.Count, stats.WithMetadata, stats.SceneCount)
}

func TestIntegrationFindAllScenesPaginates(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first, err := c.FindAllScenesPage(ctx, 1, 5)
	if err != nil {
		t.Fatalf("FindAllScenesPage(1): %v", err)
	}
	if first.Count == 0 {
		t.Skip("empty library")
	}
	if len(first.Scenes) == 0 {
		t.Fatal("page 1 of a non-empty library returned no scenes")
	}
	second, err := c.FindAllScenesPage(ctx, 2, 5)
	if err != nil {
		t.Fatalf("FindAllScenesPage(2): %v", err)
	}
	if second.Count != first.Count {
		t.Errorf("total changed between pages: %d then %d", first.Count, second.Count)
	}
	// Paging has to be stable, or a whole-library walk sees some scenes twice
	// and misses others.
	seen := map[string]bool{}
	for _, s := range first.Scenes {
		seen[s.ID] = true
	}
	for _, s := range second.Scenes {
		if seen[s.ID] {
			t.Errorf("scene %s appeared on both page 1 and page 2", s.ID)
		}
	}
}

func TestIntegrationLibraryStats(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stats, err := c.LibraryStats(ctx)
	if err != nil {
		t.Fatalf("LibraryStats: %v", err)
	}
	if stats.SceneCount == 0 {
		t.Skip("empty library")
	}
	// Size arrives as bytes and is divided down here; a library of this size
	// reading as zero GiB would mean the conversion or the decode is wrong.
	if stats.ScenesSizeGB <= 0 || stats.ScenesDuration <= 0 {
		t.Errorf("size = %v GiB, duration = %v s", stats.ScenesSizeGB, stats.ScenesDuration)
	}
	t.Logf("%d scenes, %.1f GiB, %.0f hours, %d with metadata",
		stats.SceneCount, stats.ScenesSizeGB, stats.ScenesDuration/3600, stats.WithMetadata)
}

func TestIntegrationCreatorNames(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	names, err := c.CreatorNames(ctx)
	if err != nil {
		t.Fatalf("CreatorNames: %v", err)
	}
	if len(names) == 0 {
		t.Skip("no performers or studios")
	}
	for _, n := range names {
		if n == "" {
			t.Fatal("an empty name reached the vocabulary — it would match everything")
		}
	}
	t.Logf("%d performer and studio names (with aliases)", len(names))
}

func TestIntegrationStashBoxes(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	boxes, err := c.StashBoxes(ctx)
	if err != nil {
		t.Fatalf("StashBoxes: %v", err)
	}
	for _, b := range boxes {
		if b.Endpoint == "" {
			t.Errorf("configured stash-box %q has no endpoint", b.Name)
		}
	}
	t.Logf("%d stash-box endpoint(s) configured", len(boxes))
}
