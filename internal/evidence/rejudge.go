package evidence

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// Trimmed is how far a copy may fall short of its canonical duration and
// still count as the same scene — a cut intro/outro, not a clip
// (REBUILD.md §0, "Trimmed copies"): match if |d-c| <= min(pct*c, seconds).
type Trimmed struct {
	Percent float64
	Seconds float64
}

// DefaultTrimmed is REBUILD.md §0's proposed threshold: <= 5% and <= 90s.
var DefaultTrimmed = Trimmed{Percent: 0.05, Seconds: 90}

// class is one scene's classification against its canonical duration.
type class int

const (
	classMatch class = iota
	classShorter
	classLonger
)

func (c class) label() string {
	switch c {
	case classMatch:
		return "match"
	case classShorter:
		return "shorter"
	default:
		return "longer"
	}
}

// RejudgeExample is one group Rejudge moved, kept for a short human-readable
// summary.
type RejudgeExample struct {
	Signature string
	From      string
	To        string
	Reason    string
}

// RejudgeSummary counts what Rejudge moved groups to, and a handful of
// examples.
type RejudgeSummary struct {
	ToCertain int
	ToPartial int
	ToMistag  int
	Examples  []RejudgeExample
	Duration  time.Duration
}

// maxExamples caps RejudgeSummary.Examples.
const maxExamples = 5

// Rejudge re-classifies every live `conflicted` and `certain` group that
// now has a canonical duration for every one of its scenes — the
// canonical_duration generator from REBUILD.md §4.2, run as a re-judge
// rather than a group generator: it changes the verdict `scan` already
// reached, it doesn't find new groups. Groups without a canonical for every
// scene are left untouched.
func Rejudge(ctx context.Context, l *ledger.Ledger, trimmed Trimmed) (RejudgeSummary, error) {
	start := time.Now()
	var sum RejudgeSummary

	var groups []ledger.Group
	for _, v := range []string{Conflicted, Certain} {
		gs, err := l.Groups(ctx, ledger.GroupFilter{Verdict: v})
		if err != nil {
			return RejudgeSummary{}, fmt.Errorf("evidence: rejudge: %w", err)
		}
		groups = append(groups, gs...)
	}

	for _, g := range groups {
		canons, err := l.Canonicals(ctx, g.SceneIDs)
		if err != nil {
			return RejudgeSummary{}, fmt.Errorf("evidence: rejudge: %w", err)
		}
		if !everySceneHasCanonical(g.SceneIDs, canons) {
			continue
		}

		files, err := l.PrimaryFiles(ctx, g.SceneIDs)
		if err != nil {
			return RejudgeSummary{}, fmt.Errorf("evidence: rejudge: %w", err)
		}

		newVerdict, reason, judgements := judgeGroup(g, files, canons, trimmed)
		if newVerdict == g.Verdict {
			continue
		}

		evs := make([]Evidence, 0, len(judgements))
		for _, j := range judgements {
			evs = append(evs, canonicalDurationEvidence(j))
		}
		if err := l.UpdateVerdict(ctx, g.Signature, newVerdict, reason, evs); err != nil {
			return RejudgeSummary{}, fmt.Errorf("evidence: rejudge: %w", err)
		}

		switch newVerdict {
		case Certain:
			sum.ToCertain++
		case ledger.VerdictPartial:
			sum.ToPartial++
		case ledger.VerdictMistag:
			sum.ToMistag++
		}
		if len(sum.Examples) < maxExamples {
			sum.Examples = append(sum.Examples, RejudgeExample{
				Signature: g.Signature, From: g.Verdict, To: newVerdict, Reason: reason,
			})
		}
	}

	sum.Duration = time.Since(start)
	return sum, nil
}

func everySceneHasCanonical(ids []string, canons map[string][]ledger.Canonical) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if len(canons[id]) == 0 {
			return false
		}
	}
	return true
}

// sceneJudgement is one scene's classification against its canonical
// duration.
type sceneJudgement struct {
	SceneID string
	Actual  float64
	Canon   float64
	Source  string
	Class   class
}

// judgeGroup classifies every scene in g against its canonical(s) and folds
// the classifications into a group verdict, per REBUILD.md §4.2:
//
//   - a scene whose own canonical rows disagree with each other -> the
//     whole group is `mistag`, reason "sources disagree";
//   - all scenes match -> `certain` (quality decides from here);
//   - at least one match and every other is shorter -> `partial`;
//   - any scene longer than its canonical -> `mistag`, reason quoting the
//     worst-disagreeing scene's durations.
func judgeGroup(g ledger.Group, files map[string]ledger.PrimaryFile, canons map[string][]ledger.Canonical, t Trimmed) (verdict, reason string, judgements []sceneJudgement) {
	for _, id := range g.SceneIDs {
		value, source, disagree := sceneCanonical(canons[id], t)
		if disagree {
			return ledger.VerdictMistag, "sources disagree", nil
		}
		actual := files[id].Duration
		judgements = append(judgements, sceneJudgement{
			SceneID: id, Actual: actual, Canon: value, Source: source,
			Class: classify(actual, value, t),
		})
	}

	anyMatch, anyLonger := false, false
	var worst sceneJudgement
	worstDiff := -1.0
	for _, j := range judgements {
		switch j.Class {
		case classMatch:
			anyMatch = true
		case classLonger:
			anyLonger = true
		}
		if diff := math.Abs(j.Actual - j.Canon); diff > worstDiff {
			worstDiff, worst = diff, j
		}
	}

	switch {
	case allMatch(judgements):
		return Certain, "matches canonical duration", judgements
	case anyMatch && !anyLonger:
		return ledger.VerdictPartial, fmt.Sprintf("scene %s matches canonical; other(s) shorter", matchedSceneIDs(judgements)), judgements
	default:
		return ledger.VerdictMistag,
			fmt.Sprintf("scene %s: %.0fs vs canonical %.0fs", worst.SceneID, worst.Actual, worst.Canon),
			judgements
	}
}

func allMatch(js []sceneJudgement) bool {
	for _, j := range js {
		if j.Class != classMatch {
			return false
		}
	}
	return true
}

func matchedSceneIDs(js []sceneJudgement) string {
	var ids []string
	for _, j := range js {
		if j.Class == classMatch {
			ids = append(ids, j.SceneID)
		}
	}
	return strings.Join(ids, ", ")
}

// classify compares a scene's actual duration d against its canonical c:
// match within the trimmed tolerance, shorter or longer beyond it.
func classify(d, c float64, t Trimmed) class {
	diff := math.Abs(d - c)
	limit := math.Min(t.Percent*c, t.Seconds)
	switch {
	case diff <= limit:
		return classMatch
	case d < c:
		return classShorter
	default:
		return classLonger
	}
}

// sceneCanonical picks one scene's canonical value from its (possibly
// several) canonical rows. More than one row disagreeing by more than the
// trimmed tolerance is reported as such — REBUILD.md §4.2's "sources
// disagree" example (stash-box 1137s vs FSS 600s).
func sceneCanonical(cs []ledger.Canonical, t Trimmed) (value float64, source string, disagree bool) {
	if len(cs) == 0 {
		return 0, "", false
	}
	value, source = cs[0].Duration, cs[0].Source
	for _, c := range cs[1:] {
		limit := math.Min(t.Percent*math.Max(c.Duration, value), t.Seconds)
		if math.Abs(c.Duration-value) > limit {
			return 0, "", true
		}
	}
	return value, source, false
}

// canonicalDurationEvidence is the per-scene evidence.Rejudge stores
// (REBUILD.md §4.2: "Evidence rows canonical_duration per scene with detail
// 'source: c s'"). The scene id leads Detail because candidate_evidence has
// no scene_id column; plan.go's completeSceneIDs parses this same prefix to
// build a `partial` group's Eligible set.
func canonicalDurationEvidence(j sceneJudgement) Evidence {
	val := j.Canon
	return Evidence{
		Generator: "canonical_duration",
		Kind:      "canonical_duration",
		Detail: fmt.Sprintf("%s: %s: %.0fs actual vs %.0fs canonical (%s)",
			j.SceneID, orUnknown(j.Source), j.Actual, j.Canon, j.Class.label()),
		Value: &val,
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// CompleteSceneIDs returns the scene ids a `partial` group's
// canonical_duration evidence classified "match" — the Eligible set `plan`
// restricts internal/keeper.Choose to for that group.
func CompleteSceneIDs(g ledger.Group) []string {
	var out []string
	for _, e := range g.Evidence {
		if e.Generator != "canonical_duration" || !strings.HasSuffix(e.Detail, "(match)") {
			continue
		}
		id, _, ok := strings.Cut(e.Detail, ":")
		if !ok {
			continue
		}
		out = append(out, strings.TrimSpace(id))
	}
	return out
}
