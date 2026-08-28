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
// is skipped, so `go test ./...` stays hermetic. SFX_INT_STASH_SEEDED=1 turns
// a missing fixture into a failure instead of a skip — see
// internal/stash/integration_test.go for the full explanation; this copies
// the same tiny gate since the two aren't in the same package.
func integrationClient(t *testing.T) *stash.Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return stash.NewClient(url)
}

func requireSeeded(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if ok {
		return
	}
	if os.Getenv("SFX_INT_STASH_SEEDED") == "1" {
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
