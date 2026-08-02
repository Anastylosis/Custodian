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

	// tokens is every meaningful word from filename, title, studio and cast.
	// It drives candidate lookup; creator holds the handles split out of it.
	tokens  map[string]bool
	creator map[string]bool
	codes   map[string]bool

	// titleContent and stemContent are scored SEPARATELY rather than merged.
	//
	// Libraries carry the identifying information in one place or the other,
	// not both: in a measured 61k-scene library 52% of scenes had no title at
	// all and relied entirely on a structured filename, while a Stash-Box
	// tagged library is the reverse — good titles beside filenames like
	// "1080p.wmv". Merging them means a junk filename dilutes a good title and
	// a long filename drags similarity down through sheer token count. Taking
	// the better of the two lets each convention work on its own terms.
	titleContent map[string]bool
	stemContent  map[string]bool
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
	// Dirs are the folder names between the scan root and the file. People
	// file subtitles under a performer or studio folder, which names the
	// creator even when the filename does not — a strong hint that would
	// otherwise be discarded.
	Dirs []string
}

// Candidate is one scored possibility for a subtitle.
type Candidate struct {
	Scene SceneRef
	Score float64
	// Delta is sceneDuration - subtitleRuntime.
	Delta time.Duration
	// NameSim is the best of the title/filename similarity, kept so the
	// verdict can require real name agreement rather than trusting a score
	// that runtime alone could have inflated.
	NameSim float64
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
		_, s.creator = vocab.Split(s.tokens)
		// A folder hint names a performer or studio in words, so the scene's
		// creator set needs those words too, not only squashed handles.
		for t := range Tokens(strings.Join(append([]string{s.Studio}, s.Performers...), " ")) {
			s.creator[t] = true
		}
		s.titleContent, _ = vocab.Split(Tokens(s.Title))
		s.stemContent, _ = vocab.Split(Tokens(s.Stem))
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
	// Folder names contribute creator evidence only, never title evidence:
	// "Subs/Some Performer/Subs/clip.srt" says who, not what.
	for _, d := range sub.Dirs {
		for t := range Tokens(d) {
			subCreator[t] = true
		}
	}
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
		// Similarity deliberately ignores creator words. Two clips by one
		// creator share those regardless of content, so counting them here
		// would make every clip by that creator look alike.
		//
		// The scene's title and its filename are compared independently and
		// the better one wins, so a library that titles its scenes and one
		// that encodes everything in filenames both match well.
		titleSim := F1(subContent, sc.titleContent)
		stemSim := F1(subContent, sc.stemContent)
		sim, via := titleSim, "title"
		if stemSim > sim {
			sim, via = stemSim, "filename"
		}
		if sim < 0.2 && !hasCandidate(scores, i) {
			continue
		}
		c := get(i)
		c.Score += 70 * sim
		c.NameSim = sim
		if sim > 0 {
			c.Reasons = append(c.Reasons, via+" "+pct(sim))
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

	// Runtime alone is not identification. In a library of 61k scenes, plenty
	// of unrelated videos share any given length, so a weak name plus a lucky
	// duration must not confirm: measured over 1,432 real subtitles this
	// produced a tail of confident-looking nonsense, e.g. "Lacy stuffing"
	// matched to "Rubbing and Stuffing my Pussy" at 29% name similarity.
	// A code or an exact filename is identification on its own; otherwise the
	// name has to genuinely agree.
	hardID := hasReason(top, "code") || hasReason(top, "filename match")
	nameAgrees := top.NameSim >= 0.5

	switch {
	case runtimeContradicts && !hasReason(top, "filename match"):
		return Ambiguous
	case hasReason(top, "filename match") && !runtimeContradicts:
		return Confirmed
	case top.Score >= 90 && gap >= 20 && (hardID || nameAgrees):
		return Confirmed
	case runtimeAgrees && top.Score >= 60 && gap >= 15 && (hardID || nameAgrees):
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
