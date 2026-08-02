package subs

import (
	"sort"
	"strings"
	"time"
)

// Verdict expresses how much trust a match deserves.
type Verdict string

const (
	// Confirmed: name and runtime agree, and no rival comes close.
	Confirmed Verdict = "CONFIRMED"
	// Likely: good evidence, but weaker on one axis.
	Likely Verdict = "LIKELY"
	// Ambiguous: several candidates are too close to separate safely.
	Ambiguous Verdict = "AMBIGUOUS"
	// Unmatched: nothing credible in the library.
	Unmatched Verdict = "UNMATCHED"
)

// SceneRef is the subset of a Stash scene the matcher needs.
type SceneRef struct {
	ID         string
	Title      string
	Date       string
	Studio     string
	Performers []string
	// Path of the scene's primary file, as Stash reports it.
	Path string
	// Basename stem of that file, without extension.
	Stem     string
	Duration time.Duration
	// HasCaption is true when the scene already carries a caption track in
	// this subtitle's language; used to avoid proposing a duplicate.
	HasCaption bool

	// tokens is every meaningful word; content and creator are that set split
	// by the library's creator vocabulary.
	tokens  map[string]bool
	content map[string]bool
	creator map[string]bool
	codes   map[string]bool
}

// Subtitle is one loose subtitle file to place.
type Subtitle struct {
	// Path as reported by the backend that listed it.
	Path string
	// Stem is the basename without its extension.
	Stem string
	// Runtime is the last cue time; zero when unknown.
	Runtime time.Duration
	// Lang is the ISO-639-1 code detected from the file's text.
	Lang string
	// Size in bytes, used to break ties between duplicate transcripts.
	Size int64
}

// Candidate is one scored possibility for a subtitle.
type Candidate struct {
	Scene SceneRef
	Score float64
	// Delta is sceneDuration - subtitleRuntime.
	Delta time.Duration
	// Reasons explains the score in human terms, for the report.
	Reasons []string
}

// Match is the matcher's answer for a single subtitle.
type Match struct {
	Subtitle   Subtitle
	Verdict    Verdict
	Candidates []Candidate
}

// Best returns the winning candidate, or nil when there is none.
func (m Match) Best() *Candidate {
	if len(m.Candidates) == 0 {
		return nil
	}
	return &m.Candidates[0]
}

// Index holds a preprocessed library for repeated matching.
type Index struct {
	scenes  []SceneRef
	byCode  map[string][]int
	byToken map[string][]int
	vocab   *Vocab
}

// tokenDFCap ignores tokens appearing in more scenes than this: words like
// "mom" carry no discriminating power in a library where thousands of titles
// contain them, and scanning their postings dominates the cost.
const tokenDFCap = 4000

// NewIndex preprocesses scenes for matching.
//
// vocab may be nil, in which case every token counts as content — matching
// still works, it just loses the ability to tell "same creator" apart from
// "same scene".
func NewIndex(scenes []SceneRef, vocab *Vocab) *Index {
	idx := &Index{
		scenes:  make([]SceneRef, len(scenes)),
		byCode:  map[string][]int{},
		byToken: map[string][]int{},
		vocab:   vocab,
	}
	copy(idx.scenes, scenes)
	for i := range idx.scenes {
		s := &idx.scenes[i]
		blob := strings.Join(append([]string{s.Stem, s.Title, s.Studio}, s.Performers...), " ")
		s.tokens = Tokens(blob)
		s.content, s.creator = vocab.Split(s.tokens)
		s.codes = Codes(blob)
		for c := range s.codes {
			idx.byCode[c] = append(idx.byCode[c], i)
		}
		for t := range s.tokens {
			idx.byToken[t] = append(idx.byToken[t], i)
		}
	}
	return idx
}

// Len reports how many scenes are indexed.
func (ix *Index) Len() int { return len(ix.scenes) }

// Match scores a subtitle against the library and returns a verdict with up to
// maxCandidates ranked possibilities.
//
// Scoring is additive across independent signals, so agreement between two weak
// signals can outweigh one strong-looking name match:
//
//	studio/JAV code    +60   (near-decisive when present)
//	exact stem match   +100  (the subtitle is already named after the video)
//	title similarity   +0..70 (token F1)
//	runtime agreement  +0..45, and a contradiction subtracts 30
//	matching date      +25
func (ix *Index) Match(sub Subtitle, maxCandidates int) Match {
	if maxCandidates <= 0 {
		maxCandidates = 5
	}
	subTokens := Tokens(sub.Stem)
	subContent, subCreator := ix.vocab.Split(subTokens)
	subCodes := Codes(sub.Stem)
	subStemNorm := normKey(sub.Stem)

	scores := map[int]*Candidate{}
	get := func(i int) *Candidate {
		c, ok := scores[i]
		if !ok {
			c = &Candidate{Scene: ix.scenes[i]}
			scores[i] = c
		}
		return c
	}

	for code := range subCodes {
		for _, i := range ix.byCode[code] {
			c := get(i)
			c.Score += 60
			c.Reasons = append(c.Reasons, "code "+code)
		}
	}

	// Candidate set from shared discriminating tokens.
	hits := map[int]int{}
	for t := range subTokens {
		postings := ix.byToken[t]
		if len(postings) == 0 || len(postings) > tokenDFCap {
			continue
		}
		for _, i := range postings {
			hits[i]++
		}
	}
	for i, n := range hits {
		if n < 1 {
			continue
		}
		sc := &ix.scenes[i]
		// Title similarity deliberately ignores creator words. Two clips by
		// one creator share those regardless of content, so counting them
		// here would make every clip by that creator look alike.
		sim := F1(subContent, sc.content)
		if sim < 0.2 && !hasCandidate(scores, i) {
			continue
		}
		c := get(i)
		c.Score += 70 * sim
		if sim > 0 {
			c.Reasons = append(c.Reasons, "title "+pct(sim))
		}
		// Creator agreement is real but weak evidence: it narrows the field
		// without ever deciding on its own.
		if len(subCreator) > 0 && len(sc.creator) > 0 {
			if Overlap(subCreator, sc.creator) > 0 {
				c.Score += 20
				c.Reasons = append(c.Reasons, "creator")
			}
		}
	}

	for i, c := range scores {
		sc := &ix.scenes[i]
		if normKey(sc.Stem) == subStemNorm && subStemNorm != "" {
			c.Score += 100
			c.Reasons = append(c.Reasons, "filename match")
		}
		if sub.Runtime > 0 && sc.Duration > 0 {
			fit, delta := RuntimeFit(sub.Runtime, sc.Duration)
			c.Delta = delta
			if fit > 0 {
				c.Score += 45 * fit
				c.Reasons = append(c.Reasons, "runtime "+signedSeconds(delta))
			} else {
				c.Score -= 30
				c.Reasons = append(c.Reasons, "runtime mismatch "+signedSeconds(delta))
			}
		}
		if d := subDate(sub.Stem); d != "" && d == sc.Date {
			c.Score += 25
			c.Reasons = append(c.Reasons, "date "+d)
		}
	}

	ranked := make([]Candidate, 0, len(scores))
	for _, c := range scores {
		ranked = append(ranked, *c)
	}
	sort.Slice(ranked, func(a, b int) bool {
		if ranked[a].Score != ranked[b].Score {
			return ranked[a].Score > ranked[b].Score
		}
		return ranked[a].Scene.ID < ranked[b].Scene.ID
	})
	if len(ranked) > maxCandidates {
		ranked = ranked[:maxCandidates]
	}

	return Match{
		Subtitle:   sub,
		Verdict:    decide(sub, ranked),
		Candidates: ranked,
	}
}

func hasCandidate(m map[int]*Candidate, i int) bool { _, ok := m[i]; return ok }

// decide turns scores into a verdict. The gap to the runner-up matters as much
// as the absolute score: a high score with a near-tie is exactly the case where
// an automatic choice would be wrong.
func decide(sub Subtitle, ranked []Candidate) Verdict {
	if len(ranked) == 0 {
		return Unmatched
	}
	top := ranked[0]
	if top.Score < 25 {
		return Unmatched
	}
	var second float64
	if len(ranked) > 1 {
		second = ranked[1].Score
	}
	gap := top.Score - second

	runtimeKnown := sub.Runtime > 0 && top.Scene.Duration > 0
	runtimeAgrees := runtimeKnown && top.Delta >= -20*time.Second && top.Delta <= 60*time.Second
	runtimeContradicts := runtimeKnown && !runtimeAgrees

	switch {
	case runtimeContradicts && !hasReason(top, "filename match"):
		return Ambiguous
	case hasReason(top, "filename match") && !runtimeContradicts:
		return Confirmed
	case top.Score >= 90 && gap >= 20:
		return Confirmed
	case runtimeAgrees && top.Score >= 60 && gap >= 15:
		return Confirmed
	case top.Score >= 45 && gap >= 15:
		return Likely
	case gap < 15:
		return Ambiguous
	default:
		return Likely
	}
}

func hasReason(c Candidate, want string) bool {
	for _, r := range c.Reasons {
		if strings.HasPrefix(r, want) {
			return true
		}
	}
	return false
}
