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
//
// # SFX_INT_STASH_SEEDED
//
// Pointed at an *empty* Stash, a suite that skips whenever it finds nothing
// passes exactly as happily as one that found everything — which is a CI job
// that cannot go red. SFX_INT_STASH_SEEDED=1 is the promise that the fixture
// library is present, and under it every "nothing here, skip" below becomes a
// failure instead. The shared harness
// (Anastylosis/.github/.github/workflows/stash-integration.yml) sets it; a
// developer pointing these tests at their own library does not, and keeps the
// lenient behaviour.
//
// The fixtures the flag promises: three scenes (scene 1 is dupe-remux.mkv with
// one file; one scene holds two files), one phash-duplicate group at distance
// 0, one performer and one studio. Stash-boxes are deliberately not part of
// it — an empty list is a valid answer either way.

func integrationClient(t *testing.T) *Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return NewClient(url, os.Getenv("SFX_INT_STASH_API_KEY"))
}

// seeded reports whether the caller has promised the fixture library exists.
func seeded() bool {
	return os.Getenv("SFX_INT_STASH_SEEDED") == "1"
}

// requireSeeded turns "the library has nothing to look at" into a failure when
// the fixtures were promised, and into the historical skip when they were not.
// Everything it guards is a fixture the harness creates, so under
// SFX_INT_STASH_SEEDED=1 a missing one is a real defect — in the client, the
// harness, or the seed script — and not a library that happens to be empty.
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
	multi := 0
	for _, s := range page.Scenes {
		if len(s.Files) < 2 {
			t.Errorf("scene %s has %d files; filter is supposed to return file_count > 1", s.ID, len(s.Files))
			continue
		}
		multi++
	}
	requireSeeded(t, multi >= 1,
		"no scene with more than one file came back (count=%d); the fixture library has one",
		page.Count)
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
	requireSeeded(t, len(groups) >= 1,
		"no duplicate group at distance=0; the fixture library holds an mp4 and its remux")
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
	requireSeeded(t, scene != nil,
		"scene 1 not found; can't test full-scene shape against this stash")
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
		if seeded() {
			// The fixture is a real ffmpeg-encoded clip that Stash has
			// scanned, so every one of these is populated. A zero here means
			// the video-file fields are not being decoded, which no other
			// assertion would notice.
			if f.Width == 0 || f.Height == 0 {
				t.Errorf("file dimensions are %dx%d; the scanned fixture has both", f.Width, f.Height)
			}
			if f.VideoCodec == "" {
				t.Error("video codec is empty; the scanned fixture is h264")
			}
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
	requireSeeded(t, stats.SceneCount >= 2, "library has %d scenes; the fixture library has three", stats.SceneCount)
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
	requireSeeded(t, first.Count >= 2, "library has %d scenes; the fixture library has three", first.Count)
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
	requireSeeded(t, stats.SceneCount >= 2, "library has %d scenes; the fixture library has three", stats.SceneCount)
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
	// The fixtures are one performer and one studio, so a seeded library owes
	// this at least two names — one from each side of the union, which is what
	// makes a half-broken query (studios dropped, say) visible here.
	requireSeeded(t, len(names) >= 2,
		"got %d creator names; the fixture library has a performer and a studio", len(names))
	for _, n := range names {
		if n == "" {
			t.Fatal("an empty name reached the vocabulary — it would match everything")
		}
	}
	t.Logf("%d performer and studio names (with aliases)", len(names))
}

// Nothing to require under SFX_INT_STASH_SEEDED: the harness configures no
// stash-box, and an empty list is the correct answer rather than a missing
// fixture.
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
