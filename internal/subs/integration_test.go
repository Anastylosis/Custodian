package subs

import (
	"context"
	"os"
	"testing"
	"time"

	stash "github.com/Anastylosis/stash-go"
)

// The integration test in this file talks to a real Stash instance and only
// runs when SFX_INT_STASH_URL is set, so `go test ./...` stays hermetic.
// SFX_INT_STASH_SEEDED=1 turns a missing fixture into a failure instead of a
// skip — see internal/mirror/integration_test.go for the full explanation.
func integrationClient(t *testing.T) *stash.Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	return stash.NewClient(url)
}

// TestIntegrationCreatorNames checks the vocabulary `custodian captions`
// builds to tell "who made this" apart from "what it is called". It is a
// union of two paginated queries, so a half-broken one — studios dropped,
// say — still returns a plausible list; the fixture library has a performer
// and a studio, and only a count of at least two says both sides arrived.
func TestIntegrationCreatorNames(t *testing.T) {
	client := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	names, err := CreatorNames(ctx, client)
	if err != nil {
		t.Fatalf("CreatorNames: %v", err)
	}
	if os.Getenv("SFX_INT_STASH_SEEDED") == "1" && len(names) < 2 {
		t.Fatalf("got %d creator names; the fixture library has a performer and a studio", len(names))
	}
	for _, n := range names {
		if n == "" {
			t.Fatal("an empty name reached the vocabulary — it would match everything")
		}
	}
	t.Logf("%d performer and studio names (with aliases)", len(names))
}
