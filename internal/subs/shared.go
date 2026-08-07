package subs

// The matcher itself now lives in github.com/Anastylosis/subtitlematch, shared
// with MoanSubs — the two repos had grown byte-identical copies of it. What stays
// here is the part that is genuinely Custodian's: walking a local library
// (scan.go), deciding where a subtitle file should land (placement.go), and
// rendering the outcome (report.go).
//
// These aliases keep the shared names visible as `subs.X` so the rest of
// Custodian did not have to change. Type aliases are exactly the tool for this
// — `subs.Match` and `submatch.Match` are the same type, not convertible
// lookalikes, so values cross the boundary freely.

import submatch "github.com/Anastylosis/subtitlematch"

// Core matcher types.
type (
	// Verdict expresses how much trust a match deserves.
	Verdict = submatch.Verdict
	// Match is one subtitle weighed against the library.
	Match = submatch.Match
	// Candidate is a single scene considered for a subtitle.
	Candidate = submatch.Candidate
	// Subtitle is a subtitle file under consideration.
	Subtitle = submatch.Subtitle
	// SceneRef identifies a scene in the caller's library.
	SceneRef = submatch.SceneRef
	// Index is the searchable view of the library.
	Index = submatch.Index
	// Vocab holds the token equivalences used when normalising titles.
	Vocab = submatch.Vocab
)

// Verdict values. Ambiguous is the load-bearing one: it exists so a caller can
// decline to act rather than guess, because moving a subtitle onto the wrong
// video is worse than leaving it alone.
const (
	Confirmed = submatch.Confirmed
	Likely    = submatch.Likely
	Ambiguous = submatch.Ambiguous
	Unmatched = submatch.Unmatched
)

// Entry points. Declared as vars rather than wrapper funcs so they stay a
// single indirection with no signature to drift out of sync.
var (
	NewIndex       = submatch.NewIndex
	NewVocab       = submatch.NewVocab
	DetectLanguage = submatch.DetectLanguage
	Runtime        = submatch.Runtime
	HMS            = submatch.HMS
)
