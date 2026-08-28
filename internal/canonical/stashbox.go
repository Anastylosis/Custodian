package canonical

import (
	"context"
	"fmt"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// stashBoxChunkSize is the number of scene ids sent per scrapeMultiScenes
// call — REBUILD.md's "chunks of ≤ 25 scene ids".
const stashBoxChunkSize = 25

// stashBoxDelay is the pause between chunks when Delay is unset.
const stashBoxDelay = 200 * time.Millisecond

// StashBoxSource looks up canonical durations from one stash-box, matching
// scrapeMultiScenes' answer for a scene against the stash_id the scene
// already carries for this endpoint — anything else returned for that scene
// is a different candidate the operator hasn't linked, not the canonical.
type StashBoxSource struct {
	Client   *stash.Client
	Endpoint string
	// Delay overrides the pause between chunks. Zero uses stashBoxDelay;
	// set to a negative value in tests to skip the wait entirely.
	Delay time.Duration
}

// Name identifies this source in a Fetch report.
func (s StashBoxSource) Name() string { return "stash-box:" + s.Endpoint }

// Lookup asks the endpoint about scenes in chunks of stashBoxChunkSize,
// pausing between chunks and honouring ctx cancellation.
func (s StashBoxSource) Lookup(ctx context.Context, scenes []ledger.SceneRef) ([]ledger.Canonical, error) {
	var out []ledger.Canonical
	for i := 0; i < len(scenes); i += stashBoxChunkSize {
		end := min(i+stashBoxChunkSize, len(scenes))
		chunk := scenes[i:end]

		ids := make([]string, len(chunk))
		for j, sc := range chunk {
			ids[j] = sc.ID
		}
		results, err := s.Client.ScrapeMultiScenes(ctx, s.Endpoint, ids)
		if err != nil {
			return out, fmt.Errorf("stash-box %s: %w", s.Endpoint, err)
		}
		for j, sc := range chunk {
			if j >= len(results) {
				continue
			}
			if c, ok := matchStashBox(sc, s.Endpoint, results[j]); ok {
				out = append(out, c)
			}
		}

		if end >= len(scenes) {
			break
		}
		if err := s.wait(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s StashBoxSource) wait(ctx context.Context) error {
	delay := s.Delay
	if delay == 0 {
		delay = stashBoxDelay
	}
	if delay < 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// matchStashBox reports the one scraped candidate whose remote id equals
// the scene's own stash_id for endpoint — that equality is what makes it
// the canonical rather than merely a search hit.
func matchStashBox(sc ledger.SceneRef, endpoint string, results []stash.ScrapedScene) (ledger.Canonical, bool) {
	var want string
	for _, id := range sc.StashIDs {
		if id.Endpoint == endpoint {
			want = id.StashID
			break
		}
	}
	if want == "" {
		return ledger.Canonical{}, false
	}
	for _, r := range results {
		if r.RemoteSiteID == want {
			return ledger.Canonical{
				SceneID: sc.ID, Source: "stash-box", Origin: endpoint,
				Duration: float64(r.Duration), Title: r.Title, MatchedBy: "stash_id",
			}, true
		}
	}
	return ledger.Canonical{}, false
}
