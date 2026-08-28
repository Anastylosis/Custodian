// Package evidence turns ledger signals into duplicate-candidate groups.
// Generators emit evidence, not verdicts; a Policy reads the whole picture
// and judges (REBUILD.md §4.2).
package evidence

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// Evidence and Group are the ledger's persisted shape for a duplicate
// candidate; this package generates and judges them.
type Evidence = ledger.Evidence
type Group = ledger.Group
type Step = ledger.Step

// Verdict values a policy can reach. partial/mistag arrive from
// evidence.Rejudge (canonical durations), not from Judge.
const (
	Certain    = ledger.VerdictCertain
	Conflicted = ledger.VerdictConflicted
	Dismissed  = ledger.VerdictDismissed
	Probable   = ledger.VerdictProbable
	Rejected   = ledger.VerdictRejected
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

// Judge sets Verdict and Reason from the set of evidence kinds a group
// carries (REBUILD.md §4.2):
//
//   - stash_id_conflict (scenes carry different stash_ids for the same
//     endpoint) -> rejected, wins over everything else;
//   - multifile (files within one scene, already byte-equivalent by Stash's
//     own oshash/md5 attach rule) -> certain, no duration question;
//   - shared_stash_id -> certain/conflicted, from duration_spread agreement
//     (phase 2's original rule);
//   - else -> probable/conflicted from duration_spread agreement — a phash,
//     filename or title_date match with no stash_id corroboration yet.
//
// A group with no duration_spread evidence at all is never in conflict, so
// it lands on whichever side of the switch its other evidence puts it.
func (p Policy) Judge(g *Group) {
	if reason, ok := stashIDConflictReason(g); ok {
		g.Verdict = Rejected
		g.Reason = reason
		return
	}
	if hasEvidenceKind(g, "multifile") {
		g.Verdict = Certain
		g.Reason = "byte-equivalent files attached to one scene"
		return
	}

	conflicted, worstDetail := p.durationConflict(g)
	switch {
	case conflicted:
		g.Verdict = Conflicted
		g.Reason = worstDetail
	case hasEvidenceKind(g, "shared_stash_id"):
		g.Verdict = Certain
		g.Reason = "durations agree"
	default:
		g.Verdict = Probable
		g.Reason = "durations agree"
	}
}

// durationConflict reports whether any duration_spread pair in g exceeds
// p.Tolerance, and the Detail of the worst-disagreeing pair.
func (p Policy) durationConflict(g *Group) (conflicted bool, worstDetail string) {
	var worstSpread float64
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
	return conflicted, worstDetail
}

// hasEvidenceKind reports whether g carries any evidence of the given kind.
func hasEvidenceKind(g *Group, kind string) bool {
	for _, e := range g.Evidence {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// stashIDConflictReason returns the Detail of g's stash_id_conflict
// evidence, if any.
func stashIDConflictReason(g *Group) (string, bool) {
	for _, e := range g.Evidence {
		if e.Kind == "stash_id_conflict" {
			return e.Detail, true
		}
	}
	return "", false
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

// GeneratorSummary is one generator's yield within a scan: how many groups
// it found, and how many of those signatures the ledger had already seen
// from some earlier scan.
type GeneratorSummary struct {
	Found int
	New   int
	Known int
}

// Summary is what Run reports: groups and reclaimable bytes by verdict,
// plus each generator's own yield.
type Summary struct {
	Certain         int
	CertainBytes    int64
	Conflicted      int
	ConflictedBytes int64
	Dismissed       int
	DismissedBytes  int64
	Probable        int
	ProbableBytes   int64
	Rejected        int
	RejectedBytes   int64
	Generators      map[string]GeneratorSummary
	Duration        time.Duration
}

// Run executes every generator, merges their groups by signature so the
// same pair found by two generators accumulates evidence in one group
// instead of duplicating it, judges each merged group against p, applies
// any operator decision, and replaces the ledger's candidate groups for
// this run. Never talks to Stash on its own — generators that need the live
// server (phash) carry their own client; Run itself reads only what sync
// already mirrored.
func Run(ctx context.Context, l *ledger.Ledger, gens []Generator, p Policy) (Summary, error) {
	start := time.Now()

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		return Summary{}, fmt.Errorf("evidence: beginning run: %w", err)
	}

	existing, err := l.ExistingSignatures(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("evidence: reading existing signatures: %w", err)
	}

	index := map[string]*Group{}
	var order []string
	genSummaries := map[string]GeneratorSummary{}

	for _, gen := range gens {
		found, err := gen.Find(ctx, l)
		if err != nil {
			return Summary{}, fmt.Errorf("evidence: generator %s: %w", gen.Name(), err)
		}
		gs := GeneratorSummary{Found: len(found)}
		for _, fg := range found {
			if existing[fg.Signature] {
				gs.Known++
			} else {
				gs.New++
			}
			mergeGroup(index, &order, fg)
		}
		genSummaries[gen.Name()] = gs
	}

	groups := make([]Group, len(order))
	for i, sig := range order {
		groups[i] = *index[sig]
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
	summary.Generators = genSummaries
	summary.Duration = time.Since(start)

	if err := l.FinishRun(ctx, runID, ledger.RunSummary{
		Notes: fmt.Sprintf("certain %d, conflicted %d, dismissed %d, probable %d, rejected %d",
			summary.Certain, summary.Conflicted, summary.Dismissed, summary.Probable, summary.Rejected),
	}); err != nil {
		return Summary{}, fmt.Errorf("evidence: finishing run: %w", err)
	}

	return summary, nil
}

// mergeGroup folds g into index by signature: a first sighting is copied in
// as-is; a repeat sighting (a different generator finding the same scenes)
// appends its evidence and steps, unions its scenes, and keeps the larger
// Reclaimable — the same underlying scenes should agree, but larger is the
// safe pick if a generator's own accounting ever drifts.
func mergeGroup(index map[string]*Group, order *[]string, g Group) {
	existing, ok := index[g.Signature]
	if !ok {
		copyG := g
		index[g.Signature] = &copyG
		*order = append(*order, g.Signature)
		return
	}
	existing.Evidence = append(existing.Evidence, g.Evidence...)
	existing.Steps = append(existing.Steps, g.Steps...)
	existing.SceneIDs = unionSceneIDs(existing.SceneIDs, g.SceneIDs)
	if g.Reclaimable > existing.Reclaimable {
		existing.Reclaimable = g.Reclaimable
	}
}

// unionSceneIDs merges and sorts two scene id lists — signature equality
// already implies the same underlying scenes, so this is mostly a dedupe.
func unionSceneIDs(a, b []string) []string {
	set := make(map[string]bool, len(a)+len(b))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		set[id] = true
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
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
		case Probable:
			s.Probable++
			s.ProbableBytes += g.Reclaimable
		case Rejected:
			s.Rejected++
			s.RejectedBytes += g.Reclaimable
		}
	}
	return s
}
