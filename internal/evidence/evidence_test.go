package evidence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func durationEvidence(a, b float64) Evidence {
	return durationSpreadEvidence(a, b)
}

// sharedStashIDEvidence is what puts a group on the certain/conflicted side
// of Judge's switch rather than the probable/conflicted side.
func sharedStashIDEvidence() Evidence {
	return Evidence{Generator: "stash_id", Kind: "shared_stash_id", Detail: "https://sb uuid-1"}
}

func TestPolicyJudgeCertainWhenAllPairsAgree(t *testing.T) {
	g := Group{Evidence: []Evidence{
		sharedStashIDEvidence(),
		durationEvidence(100, 100.5),
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Certain {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Certain)
	}
}

func TestPolicyJudgeConflictedNamesTheWorstPair(t *testing.T) {
	g := Group{Evidence: []Evidence{
		sharedStashIDEvidence(),
		durationEvidence(1000, 1010), // agrees: spread 10 <= min(20.2, 120)
		durationEvidence(1000, 200),  // spread 800, violates
		durationEvidence(1010, 200),  // spread 810, violates — the worst
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Conflicted {
		t.Fatalf("Verdict = %q, want %q", g.Verdict, Conflicted)
	}
	if g.Reason != "1010s vs 200s" {
		t.Errorf("Reason = %q, want %q", g.Reason, "1010s vs 200s")
	}
}

func TestPolicyJudgeToleranceEdgeAgrees(t *testing.T) {
	// longest=1000, limit=min(2%*1000, 120)=20; spread exactly 20 agrees.
	g := Group{Evidence: []Evidence{sharedStashIDEvidence(), durationEvidence(1000, 980)}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Certain {
		t.Errorf("Verdict = %q, want %q (spread == limit should agree)", g.Verdict, Certain)
	}
}

func TestPolicyJudgeJustOverToleranceConflicts(t *testing.T) {
	g := Group{Evidence: []Evidence{sharedStashIDEvidence(), durationEvidence(1000, 979.9)}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Conflicted {
		t.Errorf("Verdict = %q, want %q (spread just over limit should conflict)", g.Verdict, Conflicted)
	}
}

func TestPolicyJudgeDefaultsToCertainWithNoDurationEvidence(t *testing.T) {
	g := Group{Evidence: []Evidence{
		{Generator: "stash_id", Kind: "shared_stash_id", Detail: "https://sb uuid-1"},
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Certain {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Certain)
	}
}

func TestPolicyJudgeIgnoresUnparsableDurationSpread(t *testing.T) {
	g := Group{Evidence: []Evidence{
		sharedStashIDEvidence(),
		{Generator: "stash_id", Kind: "duration_spread", Detail: "garbage"},
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Certain {
		t.Errorf("Verdict = %q, want %q for unparsable evidence", g.Verdict, Certain)
	}
}

// --- probable / rejected / multifile — phase 4 additions ---

func TestPolicyJudgeProbableWhenNoStashIDButDurationsAgree(t *testing.T) {
	g := Group{Evidence: []Evidence{
		{Generator: "phash", Kind: "phash_group", Detail: "distance 4"},
		durationSpreadEvidenceFor("phash", 100, 100.5),
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Probable {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Probable)
	}
}

func TestPolicyJudgeConflictedWithoutStashIDStaysConflicted(t *testing.T) {
	g := Group{Evidence: []Evidence{
		{Generator: "title_date", Kind: "title_date", Detail: "a scene / 2024-01-01"},
		durationSpreadEvidenceFor("title_date", 1000, 200),
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Conflicted {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Conflicted)
	}
}

func TestPolicyJudgeRejectedWinsOverAgreeingDurations(t *testing.T) {
	g := Group{Evidence: []Evidence{
		{Generator: "phash", Kind: "phash_group", Detail: "distance 4"},
		durationSpreadEvidenceFor("phash", 100, 100.5),
		{Generator: "phash", Kind: "stash_id_conflict", Detail: "https://sb a vs b"},
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Rejected {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Rejected)
	}
	if g.Reason != "https://sb a vs b" {
		t.Errorf("Reason = %q, want the stash_id_conflict detail", g.Reason)
	}
}

func TestPolicyJudgeRejectedWinsOverSharedStashID(t *testing.T) {
	// Contradictory as constructed (a group cannot really carry both a
	// shared stash_id and a conflicting one), but it pins down the stated
	// precedence: stash_id_conflict wins over everything else.
	g := Group{Evidence: []Evidence{
		sharedStashIDEvidence(),
		{Generator: "phash", Kind: "stash_id_conflict", Detail: "https://sb a vs b"},
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Rejected {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Rejected)
	}
}

func TestPolicyJudgeMultifileIsAlwaysCertain(t *testing.T) {
	g := Group{Evidence: []Evidence{
		{Generator: "multifile", Kind: "multifile", Detail: "/a.mp4; /b.mp4", Value: floatPtr(2)},
	}}
	Policy{Tolerance: DefaultTolerance}.Judge(&g)
	if g.Verdict != Certain {
		t.Errorf("Verdict = %q, want %q", g.Verdict, Certain)
	}
}

type failingGenerator struct{}

func (failingGenerator) Name() string { return "failing" }
func (failingGenerator) Find(context.Context, *ledger.Ledger) ([]Group, error) {
	return nil, errors.New("boom")
}

func TestRunPropagatesGeneratorError(t *testing.T) {
	l := newTestLedger(t)
	_, err := Run(context.Background(), l, []Generator{failingGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err == nil {
		t.Fatal("Run should fail when a generator fails")
	}
}

func TestRunAppliesDismissedDecision(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	sid := stash.StashID{Endpoint: "https://sb", ID: "uuid-1"}
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "d1", sid),
		fixtureScene("2", 100, 900, "d2", sid),
	})

	if err := l.PutDecision(ctx, ledger.Decision{
		Key: "d1+d2", Kind: "not_duplicate", Value: "operator call",
		DecidedAt: time.Now().UTC().Format(time.RFC3339), Notes: "confirmed distinct scenes",
	}); err != nil {
		t.Fatalf("PutDecision: %v", err)
	}

	summary, err := Run(ctx, l, []Generator{StashIDGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Dismissed != 1 {
		t.Fatalf("Summary.Dismissed = %d, want 1: %+v", summary.Dismissed, summary)
	}

	groups, err := l.Groups(ctx, ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("Groups = %d, want 1", len(groups))
	}
	if groups[0].Verdict != ledger.VerdictDismissed {
		t.Errorf("Verdict = %q, want dismissed", groups[0].Verdict)
	}
	if groups[0].Reason != "confirmed distinct scenes" {
		t.Errorf("Reason = %q, want the decision's notes", groups[0].Reason)
	}
}

func TestRunSummaryAcrossVerdicts(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	agreeing := stash.StashID{Endpoint: "https://sb", ID: "agree"}
	conflicting := stash.StashID{Endpoint: "https://sb", ID: "conflict"}
	seedScenes(t, l, []stash.Scene{
		fixtureScene("1", 100, 1000, "a1", agreeing),
		fixtureScene("2", 100.5, 900, "a2", agreeing),
		fixtureScene("3", 1000, 2000, "c1", conflicting),
		fixtureScene("4", 200, 1500, "c2", conflicting),
	})

	summary, err := Run(ctx, l, []Generator{StashIDGenerator{}}, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Certain != 1 {
		t.Errorf("Certain = %d, want 1", summary.Certain)
	}
	if summary.Conflicted != 1 {
		t.Errorf("Conflicted = %d, want 1", summary.Conflicted)
	}
	if summary.Dismissed != 0 {
		t.Errorf("Dismissed = %d, want 0", summary.Dismissed)
	}
	if summary.CertainBytes != 900 {
		t.Errorf("CertainBytes = %d, want 900", summary.CertainBytes)
	}
	if summary.ConflictedBytes != 1500 {
		t.Errorf("ConflictedBytes = %d, want 1500", summary.ConflictedBytes)
	}
	if summary.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", summary.Duration)
	}

	// The run is recorded.
	last, err := l.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun: %v", err)
	}
	if last.Kind != "scan" {
		t.Errorf("last run Kind = %q, want scan", last.Kind)
	}
	if !strings.Contains(last.Notes, "certain 1") || !strings.Contains(last.Notes, "conflicted 1") {
		t.Errorf("last run Notes = %q, want counts for certain and conflicted", last.Notes)
	}
}

func TestRunWithNoGeneratorsProducesNoGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	summary, err := Run(ctx, l, nil, Policy{Tolerance: DefaultTolerance})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary.Certain != 0 || summary.Conflicted != 0 || summary.Dismissed != 0 {
		t.Errorf("summary = %+v, want all zero", summary)
	}
}
