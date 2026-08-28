// Package canonical fetches a scene's true runtime from an authority
// outside the local library — a stash-box or an FSS catalogue — and stores
// it as a ledger.Canonical row for evidence.Rejudge to classify against
// (REBUILD.md §4.2's canonical_duration generator).
package canonical

import (
	"context"
	"fmt"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// Source looks up canonical durations for a batch of scenes. A scene it
// cannot place a canonical for is simply absent from the result — that is
// not an error.
type Source interface {
	Name() string
	Lookup(ctx context.Context, scenes []ledger.SceneRef) ([]ledger.Canonical, error)
}

// ambiguityCounter is implemented by a Source that can tell "no candidate"
// apart from "found more than one and could not pick, skipped" — FSSSource
// is the one source where that distinction exists. Fetch reads it through
// this optional interface rather than widening Source, since every other
// source has no such state to report.
type ambiguityCounter interface {
	Ambiguous() int
}

// Options configures Fetch.
type Options struct {
	// Verdicts selects which live groups' scenes need a canonical. Empty
	// defaults to []string{ledger.VerdictConflicted}.
	Verdicts []string
	// Limit caps how many scenes are looked up (0 = no limit).
	Limit int
}

// SourceReport is one source's tally from a Fetch run.
type SourceReport struct {
	Source    string
	LookedUp  int
	Matched   int
	Unmatched int
	Ambiguous int
}

// Report is what Fetch found, one entry per source it ran.
type Report struct {
	Sources []SourceReport
}

// Summary is the one-line form recorded as the canon run's notes.
func (r Report) Summary() string {
	if len(r.Sources) == 0 {
		return "nothing needed a canonical duration"
	}
	parts := make([]string, len(r.Sources))
	for i, s := range r.Sources {
		parts[i] = fmt.Sprintf("%s: matched %d/%d", s.Source, s.Matched, s.LookedUp)
	}
	return strings.Join(parts, "; ")
}

// String renders the report as the plain text `custodian canon` prints.
func (r Report) String() string {
	var b strings.Builder
	if len(r.Sources) == 0 {
		b.WriteString("canon: nothing needed a canonical duration\n")
		return b.String()
	}
	b.WriteString("canon\n")
	for _, s := range r.Sources {
		fmt.Fprintf(&b, "  %-12s looked up %d, matched %d, unmatched %d, ambiguous %d\n",
			s.Source+":", s.LookedUp, s.Matched, s.Unmatched, s.Ambiguous)
	}
	return b.String()
}

// Fetch finds the scenes needing a canonical (belonging to a live group
// with one of opts.Verdicts, default `conflicted`, and carrying no
// canonical row yet), asks every source for them, and stores what each
// source found. Sources run in order; a scene already matched by an
// earlier source is still offered to later ones, since a stash-box match
// and an FSS match are different rows (source, origin) and
// evidence.Rejudge is what decides whether they agree.
func Fetch(ctx context.Context, l *ledger.Ledger, sources []Source, opts Options) (Report, error) {
	verdicts := opts.Verdicts
	if len(verdicts) == 0 {
		verdicts = []string{ledger.VerdictConflicted}
	}

	seen := map[string]bool{}
	var sceneIDs []string
	for _, v := range verdicts {
		ids, err := l.ScenesNeedingCanonical(ctx, v)
		if err != nil {
			return Report{}, fmt.Errorf("canonical: %w", err)
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				sceneIDs = append(sceneIDs, id)
			}
		}
	}
	if opts.Limit > 0 && len(sceneIDs) > opts.Limit {
		sceneIDs = sceneIDs[:opts.Limit]
	}

	var rep Report
	if len(sceneIDs) == 0 || len(sources) == 0 {
		return rep, nil
	}

	scenes, err := l.SceneRefs(ctx, sceneIDs)
	if err != nil {
		return Report{}, fmt.Errorf("canonical: %w", err)
	}

	runID, err := l.BeginRun(ctx, "canon", "")
	if err != nil {
		return Report{}, fmt.Errorf("canonical: beginning run: %w", err)
	}

	for _, src := range sources {
		canons, err := src.Lookup(ctx, scenes)
		if err != nil {
			return rep, fmt.Errorf("canonical: %s: %w", src.Name(), err)
		}

		sr := SourceReport{Source: src.Name(), LookedUp: len(scenes), Matched: len(canons)}
		if ac, ok := src.(ambiguityCounter); ok {
			sr.Ambiguous = ac.Ambiguous()
		}
		sr.Unmatched = sr.LookedUp - sr.Matched - sr.Ambiguous
		if sr.Unmatched < 0 {
			sr.Unmatched = 0
		}
		rep.Sources = append(rep.Sources, sr)

		for _, c := range canons {
			c.FetchedRun = runID
			if err := l.PutCanonical(ctx, c); err != nil {
				return rep, fmt.Errorf("canonical: %w", err)
			}
		}
	}

	if err := l.FinishRun(ctx, runID, ledger.RunSummary{Notes: rep.Summary()}); err != nil {
		return rep, fmt.Errorf("canonical: finishing run: %w", err)
	}
	return rep, nil
}
