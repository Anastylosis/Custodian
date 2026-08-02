package subs

import "strings"

// Vocab is the set of words that name a creator — performers and studios in
// the library, plus their aliases.
//
// It replaces a hand-maintained list of channel handles, which could only ever
// grow and had to be edited whenever a new creator appeared. Building it from
// Stash means it is always complete, always specific to this library, and
// contains no personal names in committed source.
//
// Creator words are NOT discarded. Some aliases are ordinary words — real
// examples from a live library include "Molly", "Tamara" and "Vanity" — and
// dropping those would destroy genuine title words like "Molly Catches Her
// Step-Son". Instead the vocabulary SPLITS a name into two token sets, applied
// identically to subtitles and scenes:
//
//	content tokens  what the scene is called      → title similarity
//	creator tokens  who made or appears in it     → a separate, bounded signal
//
// The split is what stops a creator handle from inflating similarity between
// two unrelated clips by the same creator, without throwing the information
// away.
// It holds only SQUASHED names — the whole name with separators removed, as
// scrapers write handles ("riverstone", "thehousenextdoor"). Individual words
// are deliberately not included.
//
// That restriction was found empirically. Taking every word from every alias in
// a real 4,298-performer library produced ~9,500 creator words, which swept up
// ordinary vocabulary — "sinful", "aunt", "slut" all appear in someone's alias
// list — and gutted the content tokens that title matching depends on. Measured
// against the live library, it pushed a correct candidate (runtime +1s) out of
// the top three entirely.
//
// Squashed handles are what actually needed suppressing, and they are
// inherently distinctive, so they carry the benefit without the collateral
// damage.
type Vocab struct {
	squashed map[string]bool
}

// NewVocab builds a vocabulary from performer and studio names (and aliases).
func NewVocab(names []string) *Vocab {
	v := &Vocab{squashed: map[string]bool{}}
	for _, n := range names {
		words := strings.Fields(nonAlnumRe.ReplaceAllString(strings.ToLower(n), " "))
		if len(words) == 0 {
			continue
		}
		s := strings.Join(words, "")
		// A multi-word name is distinctive once it is long enough. A
		// single-word one has to be longer still, because a short common word
		// ("molly", "vanity") would otherwise capture every token starting
		// with it.
		min := 6
		if len(words) == 1 {
			min = 8
		}
		if len(s) >= min {
			v.squashed[s] = true
		}
	}
	return v
}

// Len reports how many distinct creator handles are known.
func (v *Vocab) Len() int {
	if v == nil {
		return 0
	}
	return len(v.squashed)
}

// IsCreator reports whether a token names a creator.
//
// Scrapers concatenate handles and bolt suffixes on — "riverstonexxx",
// "thehousenextdoor2024" — so a token also counts when it starts with a known
// squashed name. Trailing digits are ignored for the same reason.
func (v *Vocab) IsCreator(tok string) bool {
	if v == nil || tok == "" {
		return false
	}
	if v.squashed[tok] {
		return true
	}
	if bare := strings.TrimRight(tok, "0123456789"); bare != tok && v.squashed[bare] {
		return true
	}
	// A handle with a suffix bolted on: "riverstonexxx". Only prefixes of the
	// token are considered, so a creator name cannot match mid-word.
	for n := len(tok) - 1; n >= 6; n-- {
		if v.squashed[tok[:n]] {
			return true
		}
	}
	return false
}

// Split partitions tokens into content and creator sets.
func (v *Vocab) Split(toks map[string]bool) (content, creator map[string]bool) {
	content = map[string]bool{}
	creator = map[string]bool{}
	for t := range toks {
		if v.IsCreator(t) {
			creator[t] = true
		} else {
			content[t] = true
		}
	}
	return content, creator
}
