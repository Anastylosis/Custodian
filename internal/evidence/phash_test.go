package evidence

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// fakeDuplicateServer answers every request with a fixed findDuplicateScenes
// body — the same response shape stash-go's own client tests use.
func fakeDuplicateServer(t *testing.T, body string) *stash.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return stash.NewClient(srv.URL)
}

func TestPhashGeneratorMapsClusterToLedgerScenes(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "h1"),
		fixtureScene("2", 100.2, 900, "h2"),
	})

	client := fakeDuplicateServer(t, `{"data":{"findDuplicateScenes":[
		[{"id":"1","files":[{"id":"f1"}]},{"id":"2","files":[{"id":"f2"}]}]
	]}}`)

	gen := PhashGenerator{Client: client, Distance: 4, DurationDiff: 1.0}
	groups, err := gen.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}
	g := groups[0]
	if g.Signature != "h1+h2" {
		t.Errorf("Signature = %q, want h1+h2", g.Signature)
	}
	if len(g.SceneIDs) != 2 {
		t.Errorf("SceneIDs = %v, want 2 scenes", g.SceneIDs)
	}
	var found bool
	for _, e := range g.Evidence {
		if e.Kind == "phash_group" {
			found = true
			if e.Detail != "distance 4, duration_diff 1s" {
				t.Errorf("phash_group Detail = %q", e.Detail)
			}
		}
	}
	if !found {
		t.Error("no phash_group evidence on the group")
	}
	if g.Reclaimable != 900 {
		t.Errorf("Reclaimable = %d, want 900", g.Reclaimable)
	}
}

func TestPhashGeneratorDropsSceneNoLongerInLedger(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{fixtureScene("1", 100, 1000, "h1")})

	client := fakeDuplicateServer(t, `{"data":{"findDuplicateScenes":[
		[{"id":"1"},{"id":"999"}]
	]}}`)

	gen := PhashGenerator{Client: client, Distance: 4, DurationDiff: 1.0}
	groups, err := gen.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("Find = %d groups, want 0 (scene 999 isn't in the ledger, leaving only 1 live scene)", len(groups))
	}
}

func TestPhashGeneratorNilClientIsANoop(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	groups, err := (PhashGenerator{}).Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if groups != nil {
		t.Errorf("Find = %v, want nil", groups)
	}
}

func TestPhashGeneratorRejectedOnConflictingStashID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "h1", stash.StashID{Endpoint: "https://sb", ID: "aaa"}),
		fixtureScene("2", 100.2, 900, "h2", stash.StashID{Endpoint: "https://sb", ID: "bbb"}),
	})

	client := fakeDuplicateServer(t, `{"data":{"findDuplicateScenes":[
		[{"id":"1"},{"id":"2"}]
	]}}`)

	gen := PhashGenerator{Client: client, Distance: 4, DurationDiff: 1.0}
	groups, err := gen.Find(ctx, l)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Find = %d groups, want 1", len(groups))
	}

	g := groups[0]
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Rejected {
		t.Errorf("Verdict = %q, want rejected (scenes carry different stash_ids on the same endpoint)", g.Verdict)
	}
}

func TestPhashGeneratorMergesWithStashIDGeneratorBySignature(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	sid := stash.StashID{Endpoint: "https://sb", ID: "uuid-1"}
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "h1", sid),
		fixtureScene("2", 100.2, 900, "h2", sid),
	})

	client := fakeDuplicateServer(t, `{"data":{"findDuplicateScenes":[
		[{"id":"1"},{"id":"2"}]
	]}}`)

	summary, err := Run(ctx, l, []Generator{
		StashIDGenerator{},
		PhashGenerator{Client: client, Distance: 4, DurationDiff: 1.0},
	}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Certain != 1 {
		t.Fatalf("Certain = %d, want 1 (one merged group, stash_id rules win)", summary.Certain)
	}

	groups, err := l.Groups(ctx, ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	var kinds []string
	for _, e := range g.Evidence {
		kinds = append(kinds, e.Kind)
	}
	hasShared, hasPhash := false, false
	for _, k := range kinds {
		if k == "shared_stash_id" {
			hasShared = true
		}
		if k == "phash_group" {
			hasPhash = true
		}
	}
	if !hasShared || !hasPhash {
		t.Errorf("merged group evidence kinds = %v, want both shared_stash_id and phash_group", kinds)
	}
}
