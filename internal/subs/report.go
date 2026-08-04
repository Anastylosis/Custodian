package subs

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/Anastylosis/StashJanitor/internal/pathmap"
)

// Summary counts matches by verdict.
type Summary struct {
	Total     int `json:"total"`
	Attached  int `json:"already_attached"`
	Confirmed int `json:"confirmed"`
	Likely    int `json:"likely"`
	Ambiguous int `json:"ambiguous"`
	Unmatched int `json:"unmatched"`
}

// Summarize tallies a set of matches.
func Summarize(matches []Match, attached int) Summary {
	s := Summary{Total: len(matches) + attached, Attached: attached}
	for _, m := range matches {
		switch m.Verdict {
		case Confirmed:
			s.Confirmed++
		case Likely:
			s.Likely++
		case Ambiguous:
			s.Ambiguous++
		default:
			s.Unmatched++
		}
	}
	return s
}

// PrintReport renders a human-readable report, strongest verdicts first.
//
// Every candidate carries its reasons and runtime delta, because the useful
// question when reviewing is not "what did it pick" but "why, and what did it
// pick over" — especially for the AMBIGUOUS rows a human has to settle.
func PrintReport(w io.Writer, matches []Match, sum Summary, maxCandidates int) error {
	fmt.Fprintf(w, "=== subtitle match report ===\n")
	fmt.Fprintf(w, "  subtitles found:  %d\n", sum.Total)
	if sum.Attached > 0 {
		fmt.Fprintf(w, "  already attached: %d  (Stash can already see these)\n", sum.Attached)
	}
	fmt.Fprintf(w, "  confirmed:        %d\n", sum.Confirmed)
	fmt.Fprintf(w, "  likely:           %d\n", sum.Likely)
	fmt.Fprintf(w, "  ambiguous:        %d\n", sum.Ambiguous)
	fmt.Fprintf(w, "  unmatched:        %d\n", sum.Unmatched)

	order := map[Verdict]int{Confirmed: 0, Likely: 1, Ambiguous: 2, Unmatched: 3}
	sorted := make([]Match, len(matches))
	copy(sorted, matches)
	sort.SliceStable(sorted, func(i, j int) bool {
		if order[sorted[i].Verdict] != order[sorted[j].Verdict] {
			return order[sorted[i].Verdict] < order[sorted[j].Verdict]
		}
		return sorted[i].Subtitle.Path < sorted[j].Subtitle.Path
	})

	var current Verdict = "-"
	for _, m := range sorted {
		if m.Verdict != current {
			current = m.Verdict
			fmt.Fprintf(w, "\n--- %s ---\n", current)
		}
		lang := m.Subtitle.Lang
		if lang == "" {
			lang = "??"
		}
		fmt.Fprintf(w, "\n%s\n", pathmap.Base(m.Subtitle.Path))
		fmt.Fprintf(w, "   runtime %s   lang %s\n", HMS(m.Subtitle.Runtime), lang)
		if len(m.Candidates) == 0 {
			fmt.Fprintf(w, "   (no candidate)\n")
			continue
		}
		n := maxCandidates
		if n <= 0 || n > len(m.Candidates) {
			n = len(m.Candidates)
		}
		for i, c := range m.Candidates[:n] {
			marker := "  "
			if i == 0 {
				marker = "->"
			}
			fmt.Fprintf(w, "   %s #%s  score %.0f  [%s]\n",
				marker, c.Scene.ID, c.Score, HMS(c.Scene.Duration))
			fmt.Fprintf(w, "        %s\n", c.Scene.Path)
			if len(c.Reasons) > 0 {
				fmt.Fprintf(w, "        why: %v\n", c.Reasons)
			}
		}
	}
	return nil
}

// jsonMatch is the machine-readable shape written by --json.
type jsonMatch struct {
	Subtitle string          `json:"subtitle"`
	Runtime  string          `json:"runtime"`
	Lang     string          `json:"lang"`
	Verdict  string          `json:"verdict"`
	Cands    []jsonCandidate `json:"candidates"`
}

type jsonCandidate struct {
	SceneID  string   `json:"scene_id"`
	Score    float64  `json:"score"`
	Path     string   `json:"path"`
	Title    string   `json:"title"`
	Duration string   `json:"duration"`
	DeltaSec int      `json:"delta_seconds"`
	Reasons  []string `json:"reasons"`
}

// WriteJSON emits the full result for scripting or later review.
func WriteJSON(w io.Writer, matches []Match, sum Summary) error {
	out := struct {
		Summary Summary     `json:"summary"`
		Matches []jsonMatch `json:"matches"`
	}{Summary: sum}

	for _, m := range matches {
		jm := jsonMatch{
			Subtitle: m.Subtitle.Path,
			Runtime:  HMS(m.Subtitle.Runtime),
			Lang:     m.Subtitle.Lang,
			Verdict:  string(m.Verdict),
		}
		for _, c := range m.Candidates {
			jm.Cands = append(jm.Cands, jsonCandidate{
				SceneID:  c.Scene.ID,
				Score:    float64(int(c.Score*10)) / 10,
				Path:     c.Scene.Path,
				Title:    c.Scene.Title,
				Duration: HMS(c.Scene.Duration),
				DeltaSec: int(c.Delta.Seconds()),
				Reasons:  c.Reasons,
			})
		}
		out.Matches = append(out.Matches, jm)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
