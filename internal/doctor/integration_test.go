package doctor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
	stash "github.com/Anastylosis/stash-go"
)

// Integration test in this file talks to a real Stash instance and only
// runs when SFX_INT_STASH_URL is set — see internal/reclaim/integration_test.go
// for the full explanation of this gate and SFX_INT_STASH_SEEDED.
//
// Unlike reclaim's integration tests, this one never requires
// SFX_INT_STASH_WRITABLE: the harness fixture seeds every scene with a
// phash already (stash-seed.sh runs metadataGenerate before handing back
// the URL), so GeneratePhashes finds nothing to do and — per its own doc
// comment — sends nothing to Stash when that's the case. The test asserts
// that emptiness first (skipping, or failing under SFX_INT_STASH_SEEDED=1,
// if the fixture ever stops being clean) before calling --commit, so a
// commit run here is only ever exercised as the true no-op its name says.
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

func TestIntegrationDoctorReportsCleanPhashGapAndGenerateIsANoOp(t *testing.T) {
	client := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("mirror.Sync: %v", err)
	}

	cfg := &config.Config{}
	cfg.Quarantine.Dir = "/data/_quarantine"

	rep, err := Check(ctx, client, l, cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	requireSeeded(t, rep.Phash.Count == 0,
		"expected the seeded fixture's scenes to already carry a phash, got %d without one", rep.Phash.Count)

	genRep, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: true})
	if err != nil {
		t.Fatalf("GeneratePhashes --commit --yes: %v", err)
	}
	if genRep.Count != 0 {
		t.Fatalf("GeneratePhashes.Count = %d, want 0", genRep.Count)
	}
	if genRep.Committed {
		t.Fatalf("GeneratePhashes started a job with nothing to generate: %+v", genRep)
	}
	if genRep.JobID != "" {
		t.Fatalf("GeneratePhashes.JobID = %q, want empty for a no-op commit", genRep.JobID)
	}
}
