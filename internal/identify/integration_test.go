package identify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
	stash "github.com/Anastylosis/stash-go"
)

// Integration test in this file talks to a real Stash instance and only runs
// when SFX_INT_STASH_URL is set — see internal/mirror/integration_test.go for
// the full explanation of SFX_INT_STASH_SEEDED and why a lenient skip is the
// right default. internal/evidence, internal/reclaim and this package each
// carry their own copy of the gate, one per package.
//
// The seed fixtures (three scenes, none carrying a URL) mean --via url has
// nothing to do against them — that emptiness is exactly what this test
// pins. --via stash-box needs a real stash-box API key the CI harness does
// not have, so it is not exercised here at all.
func integrationClient(t *testing.T) *stash.Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return stash.NewClient(url)
}

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

func TestIntegrationIdentifyByURLDryRunIsEmptyOnTheSeededFixtures(t *testing.T) {
	client := integrationClient(t)
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	rep, err := Identify(ctx, client, l, Options{Via: "url"})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if !rep.DryRun {
		t.Error("DryRun = false, want true (no --commit)")
	}

	requireSeeded(t, rep.Considered == 0, "identify --via url considered %d scenes, want 0 (the fixtures carry no urls)", rep.Considered)
	if rep.SelectedCount != 0 {
		t.Errorf("SelectedCount = %d, want 0", rep.SelectedCount)
	}
	if len(rep.URLResults) != 0 {
		t.Errorf("URLResults = %+v, want none", rep.URLResults)
	}
}
