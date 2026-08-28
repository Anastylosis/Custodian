package canonical

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// seedConflictedGroup writes two scenes and one `conflicted` candidate_group
// holding them — the shape Fetch's default Verdicts option looks for.
func seedConflictedGroup(t *testing.T, l *ledger.Ledger, signature string, scenes ...stash.Scene) {
	t.Helper()
	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	ids := make([]string, len(scenes))
	for i, sc := range scenes {
		ids[i] = sc.ID
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: signature, SceneIDs: ids, Verdict: ledger.VerdictConflicted, Reason: "durations disagree"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
}

func fixtureScene(id, title, date string, stashIDs ...stash.StashID) stash.Scene {
	return stash.Scene{
		ID: id, Title: title, Date: date, UpdatedAt: "2024-01-01 00:00:00", StashIDs: stashIDs,
		Files: []stash.File{{ID: "f" + id, Duration: 600}},
	}
}

// fakeSource is a Source a test controls directly: it hands back whatever
// canonicals were configured for each scene id, and can report itself
// ambiguous like FSSSource does.
type fakeSource struct {
	name      string
	canonical map[string]ledger.Canonical // sceneID -> canonical (Duration 0 skips it, "no match")
	ambiguous int
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Lookup(_ context.Context, scenes []ledger.SceneRef) ([]ledger.Canonical, error) {
	var out []ledger.Canonical
	for _, sc := range scenes {
		if c, ok := f.canonical[sc.ID]; ok {
			c.SceneID = sc.ID
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeSource) Ambiguous() int { return f.ambiguous }

func TestFetchDefaultsToConflictedVerdict(t *testing.T) {
	l := newTestLedger(t)
	seedConflictedGroup(t, l, "sig-1",
		fixtureScene("1", "Scene One", "2024-01-01"),
		fixtureScene("2", "Scene Two", "2024-01-02"))

	src := &fakeSource{name: "test", canonical: map[string]ledger.Canonical{
		"1": {Source: "test", Origin: "o", Duration: 601, Title: "Scene One", MatchedBy: "x"},
	}}

	rep, err := Fetch(context.Background(), l, []Source{src}, Options{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rep.Sources) != 1 {
		t.Fatalf("Sources = %+v, want 1 entry", rep.Sources)
	}
	sr := rep.Sources[0]
	if sr.LookedUp != 2 || sr.Matched != 1 || sr.Unmatched != 1 || sr.Ambiguous != 0 {
		t.Errorf("SourceReport = %+v, want LookedUp 2, Matched 1, Unmatched 1", sr)
	}

	canons, err := l.Canonicals(context.Background(), []string{"1", "2"})
	if err != nil {
		t.Fatalf("Canonicals: %v", err)
	}
	if len(canons["1"]) != 1 || canons["1"][0].Duration != 601 {
		t.Errorf("canonical for scene 1 = %+v, want duration 601", canons["1"])
	}
	if len(canons["2"]) != 0 {
		t.Errorf("canonical for scene 2 = %+v, want none (unmatched)", canons["2"])
	}
}

func TestFetchSkipsScenesAlreadyCarryingACanonical(t *testing.T) {
	l := newTestLedger(t)
	seedConflictedGroup(t, l, "sig-1",
		fixtureScene("1", "Scene One", "2024-01-01"),
		fixtureScene("2", "Scene Two", "2024-01-02"))

	if err := l.PutCanonical(context.Background(), ledger.Canonical{
		SceneID: "1", Source: "stash-box", Origin: "https://sb", Duration: 600, FetchedRun: 1,
	}); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}

	src := &fakeSource{name: "test", canonical: map[string]ledger.Canonical{
		"1": {Source: "test", Origin: "o", Duration: 999},
		"2": {Source: "test", Origin: "o", Duration: 600},
	}}
	rep, err := Fetch(context.Background(), l, []Source{src}, Options{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if rep.Sources[0].LookedUp != 1 {
		t.Errorf("LookedUp = %d, want 1 (scene 1 already has a canonical)", rep.Sources[0].LookedUp)
	}
}

func TestFetchCountsAmbiguousFromOptionalInterface(t *testing.T) {
	l := newTestLedger(t)
	seedConflictedGroup(t, l, "sig-1",
		fixtureScene("1", "Scene One", "2024-01-01"),
		fixtureScene("2", "Scene Two", "2024-01-02"))

	src := &fakeSource{name: "fss", canonical: map[string]ledger.Canonical{}, ambiguous: 1}
	rep, err := Fetch(context.Background(), l, []Source{src}, Options{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	sr := rep.Sources[0]
	if sr.Ambiguous != 1 || sr.Matched != 0 || sr.Unmatched != 1 {
		t.Errorf("SourceReport = %+v, want Ambiguous 1, Matched 0, Unmatched 1 (2 looked up)", sr)
	}
}

func TestFetchRespectsLimit(t *testing.T) {
	l := newTestLedger(t)
	seedConflictedGroup(t, l, "sig-1",
		fixtureScene("1", "Scene One", "2024-01-01"),
		fixtureScene("2", "Scene Two", "2024-01-02"))

	src := &fakeSource{name: "test", canonical: map[string]ledger.Canonical{}}
	rep, err := Fetch(context.Background(), l, []Source{src}, Options{Limit: 1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if rep.Sources[0].LookedUp != 1 {
		t.Errorf("LookedUp = %d, want 1 (Limit)", rep.Sources[0].LookedUp)
	}
}

func TestFetchNothingNeededReturnsEmptyReport(t *testing.T) {
	l := newTestLedger(t)
	src := &fakeSource{name: "test"}
	rep, err := Fetch(context.Background(), l, []Source{src}, Options{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(rep.Sources) != 0 {
		t.Errorf("Sources = %+v, want none", rep.Sources)
	}
	if rep.String() != "canon: nothing needed a canonical duration\n" {
		t.Errorf("String() = %q", rep.String())
	}
}

func TestFetchPropagatesSourceError(t *testing.T) {
	l := newTestLedger(t)
	seedConflictedGroup(t, l, "sig-1", fixtureScene("1", "Scene One", "2024-01-01"))

	failing := failingSource{}
	if _, err := Fetch(context.Background(), l, []Source{failing}, Options{}); err == nil {
		t.Fatal("Fetch should propagate a source's error")
	}
}

type failingSource struct{}

func (failingSource) Name() string { return "failing" }
func (failingSource) Lookup(context.Context, []ledger.SceneRef) ([]ledger.Canonical, error) {
	return nil, errors.New("boom")
}

func TestReportStringFormatsCounts(t *testing.T) {
	rep := Report{Sources: []SourceReport{
		{Source: "stash-box:https://sb", LookedUp: 10, Matched: 6, Unmatched: 3, Ambiguous: 1},
	}}
	got := rep.String()
	for _, want := range []string{"stash-box:https://sb", "looked up 10", "matched 6", "unmatched 3", "ambiguous 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() missing %q:\n%s", want, got)
		}
	}
}
