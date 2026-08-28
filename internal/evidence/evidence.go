// Package evidence turns ledger signals into duplicate-candidate groups.
// Generators emit evidence, not verdicts; a Policy reads the whole picture
// and judges (REBUILD.md §4.2).
package evidence

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// Evidence and Group are the ledger's persisted shape for a duplicate
// candidate; this package generates and judges them.
type Evidence = ledger.Evidence
type Group = ledger.Group

// Verdict values a policy can reach in phase 2. partial/mistag/probable/
// rejected arrive with later generators.
const (
	Certain    = ledger.VerdictCertain
	Conflicted = ledger.VerdictConflicted
	Dismissed  = ledger.VerdictDismissed
)

// Generator finds duplicate-candidate groups and the evidence behind them.
type Generator interface {
	Name() string
	Find(ctx context.Context, l *ledger.Ledger) ([]Group, error)
}

// Tolerance is how far two scene durations may disagree and still count as
// the same content: the smaller of a percentage of the longer one and a
// flat cap.
type Tolerance struct {
	Percent float64
	Seconds float64
}

// DefaultTolerance is REBUILD.md §4.2's starting point: min(2%, 120s).
var DefaultTolerance = Tolerance{Percent: 0.02, Seconds: 120}

// Policy judges a group's evidence into a verdict.
type Policy struct {
	Tolerance Tolerance
}

// Judge sets Verdict, and Reason for a conflicted group, from a group's
// duration_spread evidence: certain when every pair agrees, else conflicted
// quoting the worst-disagreeing pair. A group with no duration_spread
// evidence defaults to certain.
func (p Policy) Judge(g *Group) {
	var worstDetail string
	var worstSpread float64
	conflicted := false

	for _, e := range g.Evidence {
		if e.Kind != "duration_spread" {
			continue
		}
		a, b, ok := parsePairDurations(e.Detail)
		if !ok {
			continue
		}
		longest := math.Max(a, b)
		spread := math.Abs(a - b)
		limit := math.Min(p.Tolerance.Percent*longest, p.Tolerance.Seconds)
		if spread > limit {
			conflicted = true
			if spread > worstSpread {
				worstSpread = spread
				worstDetail = e.Detail
			}
		}
	}

	if conflicted {
		g.Verdict = Conflicted
		g.Reason = worstDetail
		return
	}
	g.Verdict = Certain
	g.Reason = "durations agree"
}

// parsePairDurations reads the two seconds values out of a duration_spread
// evidence Detail formatted as "245s vs 1137s".
func parsePairDurations(detail string) (a, b float64, ok bool) {
	left, right, found := strings.Cut(detail, " vs ")
	if !found {
		return 0, 0, false
	}
	av, err1 := strconv.ParseFloat(strings.TrimSuffix(left, "s"), 64)
	bv, err2 := strconv.ParseFloat(strings.TrimSuffix(right, "s"), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return av, bv, true
}

// Summary is what Run reports: groups and reclaimable bytes by verdict.
type Summary struct {
	Certain         int
	CertainBytes    int64
	Conflicted      int
	ConflictedBytes int64
	Dismissed       int
	DismissedBytes  int64
	Duration        time.Duration
}

// Run executes every generator, judges each group against p, applies any
// operator decision, and replaces the ledger's candidate groups for this
// run. Never talks to Stash — it reads only what sync already mirrored.
func Run(ctx context.Context, l *ledger.Ledger, gens []Generator, p Policy) (Summary, error) {
	start := time.Now()

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		return Summary{}, fmt.Errorf("evidence: beginning run: %w", err)
	}

	var groups []Group
	for _, gen := range gens {
		found, err := gen.Find(ctx, l)
		if err != nil {
			return Summary{}, fmt.Errorf("evidence: generator %s: %w", gen.Name(), err)
		}
		groups = append(groups, found...)
	}

	for i := range groups {
		p.Judge(&groups[i])
		if err := applyDecision(ctx, l, &groups[i]); err != nil {
			return Summary{}, err
		}
	}

	if err := l.ReplaceGroups(ctx, runID, groups); err != nil {
		return Summary{}, fmt.Errorf("evidence: replacing groups: %w", err)
	}

	summary := summarize(groups)
	summary.Duration = time.Since(start)

	if err := l.FinishRun(ctx, runID, ledger.RunSummary{
		Notes: fmt.Sprintf("certain %d, conflicted %d, dismissed %d",
			summary.Certain, summary.Conflicted, summary.Dismissed),
	}); err != nil {
		return Summary{}, fmt.Errorf("evidence: finishing run: %w", err)
	}

	return summary, nil
}

// applyDecision overrides a group's verdict to dismissed when the operator
// has already ruled on its signature.
func applyDecision(ctx context.Context, l *ledger.Ledger, g *Group) error {
	d, ok, err := l.GetDecision(ctx, g.Signature)
	if err != nil {
		return fmt.Errorf("evidence: reading decision for %s: %w", g.Signature, err)
	}
	if !ok || (d.Kind != "not_duplicate" && d.Kind != "dismiss") {
		return nil
	}
	g.Verdict = Dismissed
	reason := d.Notes
	if reason == "" {
		reason = d.Value
	}
	g.Reason = reason
	return nil
}

func summarize(groups []Group) Summary {
	var s Summary
	for _, g := range groups {
		switch g.Verdict {
		case Certain:
			s.Certain++
			s.CertainBytes += g.Reclaimable
		case Conflicted:
			s.Conflicted++
			s.ConflictedBytes += g.Reclaimable
		case Dismissed:
			s.Dismissed++
			s.DismissedBytes += g.Reclaimable
		}
	}
	return s
}
