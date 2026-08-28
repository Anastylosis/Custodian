package canonical

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// fakeStashBox answers scrapeMultiScenes with the candidates queued for
// each request in order, and counts how many requests it received.
type fakeStashBox struct {
	mu       sync.Mutex
	requests int
	sceneIDs [][]string
	answer   func(ids []string) [][]stash.ScrapedScene
}

func (f *fakeStashBox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				Input struct {
					SceneIDs []string `json:"scene_ids"`
				} `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		f.mu.Lock()
		f.requests++
		f.sceneIDs = append(f.sceneIDs, req.Variables.Input.SceneIDs)
		f.mu.Unlock()

		results := f.answer(req.Variables.Input.SceneIDs)
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"scrapeMultiScenes": results}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

func newFakeStashBox(t *testing.T, answer func(ids []string) [][]stash.ScrapedScene) (*fakeStashBox, *stash.Client) {
	t.Helper()
	f := &fakeStashBox{answer: answer}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, stash.NewClient(srv.URL)
}

func TestStashBoxSourceMatchesOnRemoteIDEqualToTheScenesOwnStashID(t *testing.T) {
	f, c := newFakeStashBox(t, func(ids []string) [][]stash.ScrapedScene {
		out := make([][]stash.ScrapedScene, len(ids))
		for i, id := range ids {
			switch id {
			case "1":
				// Two candidates: only "sid-1" is the scene's own stash_id.
				out[i] = []stash.ScrapedScene{
					{RemoteSiteID: "other", Title: "Wrong Match", Duration: 111},
					{RemoteSiteID: "sid-1", Title: "The Real Scene", Duration: 1137},
				}
			}
		}
		return out
	})

	src := StashBoxSource{Client: c, Endpoint: "https://sb.test/graphql", Delay: -1}
	scenes := []ledger.SceneRef{
		{ID: "1", StashIDs: []ledger.StashIDRef{{Endpoint: "https://sb.test/graphql", StashID: "sid-1"}}},
	}
	got, err := src.Lookup(context.Background(), scenes)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d canonicals, want 1", len(got))
	}
	c0 := got[0]
	if c0.SceneID != "1" || c0.Duration != 1137 || c0.Title != "The Real Scene" || c0.MatchedBy != "stash_id" {
		t.Errorf("canonical = %+v, want the sid-1 candidate", c0)
	}
	if c0.Source != "stash-box" || c0.Origin != "https://sb.test/graphql" {
		t.Errorf("canonical source/origin = %s/%s", c0.Source, c0.Origin)
	}
	if f.requests != 1 {
		t.Errorf("requests = %d, want 1", f.requests)
	}
}

func TestStashBoxSourceIgnoresAScrapeMissingTheScenesStashID(t *testing.T) {
	f, c := newFakeStashBox(t, func(ids []string) [][]stash.ScrapedScene {
		return [][]stash.ScrapedScene{{{RemoteSiteID: "not-linked", Title: "Someone Else", Duration: 500}}}
	})
	_ = f

	src := StashBoxSource{Client: c, Endpoint: "https://sb.test/graphql", Delay: -1}
	scenes := []ledger.SceneRef{
		{ID: "1", StashIDs: []ledger.StashIDRef{{Endpoint: "https://sb.test/graphql", StashID: "sid-1"}}},
	}
	got, err := src.Lookup(context.Background(), scenes)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want no match (remote id never equals the scene's own stash_id)", got)
	}
}

func TestStashBoxSourceSkipsAsceneWithNoStashIDForThisEndpoint(t *testing.T) {
	_, c := newFakeStashBox(t, func(ids []string) [][]stash.ScrapedScene {
		return [][]stash.ScrapedScene{{{RemoteSiteID: "sid-1", Duration: 500}}}
	})

	src := StashBoxSource{Client: c, Endpoint: "https://sb.test/graphql", Delay: -1}
	scenes := []ledger.SceneRef{{ID: "1"}} // no StashIDs at all
	got, err := src.Lookup(context.Background(), scenes)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want no match", got)
	}
}

func TestStashBoxSourceChunksAt25(t *testing.T) {
	f, c := newFakeStashBox(t, func(ids []string) [][]stash.ScrapedScene {
		out := make([][]stash.ScrapedScene, len(ids))
		for i, id := range ids {
			out[i] = []stash.ScrapedScene{{RemoteSiteID: "sid-" + id, Duration: 100}}
		}
		return out
	})

	var scenes []ledger.SceneRef
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("%d", i)
		scenes = append(scenes, ledger.SceneRef{
			ID: id, StashIDs: []ledger.StashIDRef{{Endpoint: "https://sb.test/graphql", StashID: "sid-" + id}},
		})
	}

	src := StashBoxSource{Client: c, Endpoint: "https://sb.test/graphql", Delay: -1}
	got, err := src.Lookup(context.Background(), scenes)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 30 {
		t.Fatalf("got %d canonicals, want 30", len(got))
	}
	if f.requests != 2 {
		t.Fatalf("requests = %d, want 2 (30 scenes chunked at 25)", f.requests)
	}
	if len(f.sceneIDs[0]) != 25 || len(f.sceneIDs[1]) != 5 {
		t.Errorf("chunk sizes = %d, %d, want 25, 5", len(f.sceneIDs[0]), len(f.sceneIDs[1]))
	}
}

func TestStashBoxSourceHonoursContextCancellation(t *testing.T) {
	_, c := newFakeStashBox(t, func(ids []string) [][]stash.ScrapedScene {
		return make([][]stash.ScrapedScene, len(ids))
	})

	var scenes []ledger.SceneRef
	for i := 0; i < 30; i++ {
		scenes = append(scenes, ledger.SceneRef{ID: fmt.Sprintf("%d", i)})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A non-negative Delay means Lookup will select on ctx.Done() between
	// chunks; since ctx is already cancelled, the second chunk must never
	// be sent.
	src := StashBoxSource{Client: c, Endpoint: "https://sb.test/graphql", Delay: 0}
	_, err := src.Lookup(ctx, scenes)
	if err == nil {
		t.Fatal("Lookup should fail once ctx is cancelled between chunks")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v, want a context.Canceled wrapped in it", err)
	}
}

func TestStashBoxSourceNameIncludesEndpoint(t *testing.T) {
	src := StashBoxSource{Endpoint: "https://sb.test/graphql"}
	if got := src.Name(); got != "stash-box:https://sb.test/graphql" {
		t.Errorf("Name() = %q", got)
	}
}
