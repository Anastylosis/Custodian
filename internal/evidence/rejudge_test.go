package evidence

import (
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// seedGroup writes the given scenes and one candidate_group holding them
// under verdict, in a single run.
func seedGroup(t *testing.T, l *ledger.Ledger, signature, verdict string, scenes ...stash.Scene) {
	t.Helper()
	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "scan", "")
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
		{Signature: signature, SceneIDs: ids, Verdict: verdict, Reason: "seed"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
}

func putCanonical(t *testing.T, l *ledger.Ledger, sceneID, source, origin string, duration float64) {
	t.Helper()
	if err := l.PutCanonical(context.Background(), ledger.Canonical{
		SceneID: sceneID, Source: source, Origin: origin, Duration: duration, FetchedRun: 1,
	}); err != nil {
		t.Fatalf("PutCanonical: %v", err)
	}
}

func TestRejudgeAllMatchMovesToCertain(t *testing.T) {
	l := newTestLedger(t)
	seedGroup(t, l, "sig-1", Conflicted,
		fixtureScene("1", 1000, 1_000_000, "h1"),
		fixtureScene("2", 1010, 900_000, "h2"))
	putCanonical(t, l, "1", "stash-box", "https://sb", 1005)
	putCanonical(t, l, "2", "stash-box", "https://sb", 1005)

	sum, err := Rejudge(context.Background(), l, DefaultTrimmed)
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if sum.ToCertain != 1 {
		t.Fatalf("ToCertain = %d, want 1: %+v", sum.ToCertain, sum)
	}

	groups, err := l.Groups(context.Background(), ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Verdict != Certain {
		t.Fatalf("group verdict = %+v, want certain", groups)
	}
}

func TestRejudgeOneMatchOthersShorterMovesToPartial(t *testing.T) {
	l := newTestLedger(t)
	seedGroup(t, l, "sig-1", Conflicted,
		fixtureScene("1", 1000, 1_000_000, "h1"), // matches canonical
		fixtureScene("2", 245, 900_000, "h2"))    // a clip
	putCanonical(t, l, "1", "stash-box", "https://sb", 1000)
	putCanonical(t, l, "2", "stash-box", "https://sb", 1000)

	sum, err := Rejudge(context.Background(), l, DefaultTrimmed)
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if sum.ToPartial != 1 {
		t.Fatalf("ToPartial = %d, want 1: %+v", sum.ToPartial, sum)
	}

	groups, err := l.Groups(context.Background(), ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	g := groups[0]
	if g.Verdict != ledger.VerdictPartial {
		t.Fatalf("Verdict = %q, want partial", g.Verdict)
	}
	if got := CompleteSceneIDs(g); len(got) != 1 || got[0] != "1" {
		t.Errorf("CompleteSceneIDs = %v, want [1]", got)
	}
	if len(sum.Examples) != 1 || sum.Examples[0].From != Conflicted || sum.Examples[0].To != ledger.VerdictPartial {
		t.Errorf("Examples = %+v", sum.Examples)
	}
}

func TestRejudgeLongerScenesMovesToMistag(t *testing.T) {
	l := newTestLedger(t)
	seedGroup(t, l, "sig-1", Conflicted,
		fixtureScene("1", 1000, 1_000_000, "h1"),
		fixtureScene("2", 1400, 900_000, "h2")) // suspiciously long
	putCanonical(t, l, "1", "stash-box", "https://sb", 1000)
	putCanonical(t, l, "2", "stash-box", "https://sb", 1000)

	sum, err := Rejudge(context.Background(), l, DefaultTrimmed)
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if sum.ToMistag != 1 {
		t.Fatalf("ToMistag = %d, want 1: %+v", sum.ToMistag, sum)
	}
	groups, err := l.Groups(context.Background(), ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	g := groups[0]
	if g.Verdict != ledger.VerdictMistag {
		t.Fatalf("Verdict = %q, want mistag", g.Verdict)
	}
	if !strings.Contains(g.Reason, "1400s vs canonical 1000s") {
		t.Errorf("Reason = %q, want it to quote the durations", g.Reason)
	}
}

func TestRejudgeDisagreeingSourcesMovesToMistag(t *testing.T) {
	l := newTestLedger(t)
	seedGroup(t, l, "sig-1", Conflicted,
		fixtureScene("1", 1000, 1_000_000, "h1"),
		fixtureScene("2", 600, 900_000, "h2"))
	putCanonical(t, l, "1", "stash-box", "https://sb", 1137)
	putCanonical(t, l, "1", "fss", "/path/to/fss.db", 600) // disagrees wildly with the stash-box row
	putCanonical(t, l, "2", "stash-box", "https://sb", 600)

	sum, err := Rejudge(context.Background(), l, DefaultTrimmed)
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if sum.ToMistag != 1 {
		t.Fatalf("ToMistag = %d, want 1: %+v", sum.ToMistag, sum)
	}
	groups, err := l.Groups(context.Background(), ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if groups[0].Reason != "sources disagree" {
		t.Errorf("Reason = %q, want %q", groups[0].Reason, "sources disagree")
	}
}

func TestRejudgeSkipsGroupsMissingAnyCanonical(t *testing.T) {
	l := newTestLedger(t)
	seedGroup(t, l, "sig-1", Conflicted,
		fixtureScene("1", 1000, 1_000_000, "h1"),
		fixtureScene("2", 1000, 900_000, "h2"))
	putCanonical(t, l, "1", "stash-box", "https://sb", 1000) // scene 2 has none

	sum, err := Rejudge(context.Background(), l, DefaultTrimmed)
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if sum.ToCertain != 0 || sum.ToPartial != 0 || sum.ToMistag != 0 {
		t.Errorf("summary = %+v, want no moves (scene 2 has no canonical)", sum)
	}
	groups, err := l.Groups(context.Background(), ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if groups[0].Verdict != Conflicted {
		t.Errorf("Verdict = %q, want it left as conflicted", groups[0].Verdict)
	}
}

func TestRejudgeNoOpWhenVerdictAlreadyMatches(t *testing.T) {
	l := newTestLedger(t)
	seedGroup(t, l, "sig-1", Certain,
		fixtureScene("1", 1000, 1_000_000, "h1"),
		fixtureScene("2", 1000, 900_000, "h2"))
	putCanonical(t, l, "1", "stash-box", "https://sb", 1000)
	putCanonical(t, l, "2", "stash-box", "https://sb", 1000)

	sum, err := Rejudge(context.Background(), l, DefaultTrimmed)
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if sum.ToCertain != 0 || len(sum.Examples) != 0 {
		t.Errorf("summary = %+v, want nothing counted (already certain)", sum)
	}
}

func TestCompleteSceneIDsParsesMatchingScenes(t *testing.T) {
	g := ledger.Group{Evidence: []ledger.Evidence{
		{Generator: "canonical_duration", Kind: "canonical_duration", Detail: "1: fss: 600s actual vs 601s canonical (match)"},
		{Generator: "canonical_duration", Kind: "canonical_duration", Detail: "2: fss: 245s actual vs 601s canonical (shorter)"},
		{Generator: "stash_id", Kind: "shared_stash_id", Detail: "irrelevant"},
	}}
	got := CompleteSceneIDs(g)
	if len(got) != 1 || got[0] != "1" {
		t.Errorf("CompleteSceneIDs = %v, want [1]", got)
	}
}

func TestClassifyBoundaryOfTrimmedThreshold(t *testing.T) {
	trimmed := Trimmed{Percent: 0.05, Seconds: 90}
	// canonical 1000s: limit = min(50, 90) = 50.
	if got := classify(950, 1000, trimmed); got != classMatch {
		t.Errorf("classify(950, 1000) = %v, want match (spread == limit)", got)
	}
	if got := classify(949.9, 1000, trimmed); got != classShorter {
		t.Errorf("classify(949.9, 1000) = %v, want shorter (just over limit)", got)
	}
	if got := classify(1050, 1000, trimmed); got != classMatch {
		t.Errorf("classify(1050, 1000) = %v, want match (spread == limit)", got)
	}
	if got := classify(1050.1, 1000, trimmed); got != classLonger {
		t.Errorf("classify(1050.1, 1000) = %v, want longer (just over limit)", got)
	}
}
