// Package textmatch normalises scene titles for matching. The rules are
// ported from stash-catalog-sync (see its PITFALLS.md, "Matching"); each one
// exists because the obvious alternative produced wrong matches there.
package textmatch

import (
	"html"
	"regexp"
	"strings"
)

// kinship lists the relation words whose "step-" prefix folds away for
// matching; keep in sync with stash-catalog-sync/scripts/common.py _KIN.
const kinship = "mom|mommy|mother|son|daughter|dad|daddy|father|sis|sister|" +
	"bro|brother|aunt|uncle|grandma|granny|grandpa|niece|nephew|cousin"

// CollisionSpread is the duration difference, in seconds, above which two
// rows sharing a title key are two different scenes.
const CollisionSpread = 120

var (
	// formatToken matches a resolution/format marker anywhere in a title,
	// not only at the end — the source strips these wherever they appear.
	formatToken = regexp.MustCompile(`\b(full\s*hd|1080p|720p|480p|2160p|4k|uhd|sd|hd|wmv|mov|mp4|avi|mpg|m4v)\b`)
	punctuation = regexp.MustCompile(`[^a-z0-9]+`)
	whitespace  = regexp.MustCompile(`\s+`)

	// stepPrefix folds "step-Mom"/"step Mom"/"Stepdads" to the bare kinship
	// word. The `s?` absorbs a possessive-turned-plural: apostrophes are
	// stripped before this runs, so "step-Mom's" is already "step moms" by
	// the time this fires, and without `s?` the substitution would silently
	// stop matching (PITFALLS.md: this alone made one creator's match rate
	// worse, 426 -> 375).
	stepPrefix = regexp.MustCompile(`\bstep\s*((?:` + kinship + `)s?)\b`)

	quoteFold = strings.NewReplacer(
		"’", "'", "‘", "'",
		"“", `"`, "”", `"`,
		"…", "...", "–", "-", "—", "-",
	)

	// ordinalToken matches a part/sequel marker: a digit run, a roman
	// numeral I-X, or a bare single letter. Ported from common.py's _ORD,
	// which sibling_keys() uses to veto pairing titles that differ only by
	// such a marker ("a differing 'Part N' marker vetoes the pair").
	ordinalToken = regexp.MustCompile(`\b(\d+|i{1,3}|iv|vi{0,3}|ix|x{1,3}|[a-z])\b`)
)

// Key returns the normalised match key for a title: entities decoded, case
// and punctuation folded, apostrophes stripped, resolution tokens removed,
// and "step-" kinship prefixes folded with their possessive/plural.
//
// The key is for comparison only — never write it back as a title. Unlike
// the Python source it does no Unicode NFKD decomposition (stdlib only).
func Key(title string) string {
	s := html.UnescapeString(html.UnescapeString(title))
	s = quoteFold.Replace(s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "'", "")
	s = formatToken.ReplaceAllString(s, " ")
	s = punctuation.ReplaceAllString(s, " ")
	s = stepPrefix.ReplaceAllString(s, "$1")
	s = whitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// Equivalent reports whether two titles match under a provable class: equal
// keys, equal after de-spacing, plural/singular on the last word, or a
// truncation of at most two characters. A differing "Part N" ordinal vetoes
// the pair. It never fuzzy-matches by character ratio.
func Equivalent(a, b string) bool {
	ka, kb := Key(a), Key(b)
	if ka == kb {
		return true
	}
	if strings.ReplaceAll(ka, " ", "") == strings.ReplaceAll(kb, " ", "") {
		return true
	}
	if !sameOrdinals(ka, kb) {
		return false
	}
	if pluralLastWord(ka, kb) {
		return true
	}
	return truncationPrefix(ka, kb)
}

// sameOrdinals reports whether two keys carry the same set of ordinal
// tokens (see ordinalToken) — used to veto pairing titles that differ only
// by a part/sequel marker.
func sameOrdinals(a, b string) bool {
	oa := ordinalToken.FindAllString(a, -1)
	ob := ordinalToken.FindAllString(b, -1)
	set := make(map[string]int, len(oa))
	for _, o := range oa {
		set[o]++
	}
	for _, o := range ob {
		set[o]--
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return len(oa) == len(ob)
}

// pluralLastWord reports whether a and b have identical words except for
// the last, and the last words differ only by a trailing "s" or "es".
func pluralLastWord(a, b string) bool {
	aw, bw := strings.Fields(a), strings.Fields(b)
	if len(aw) == 0 || len(aw) != len(bw) {
		return false
	}
	for i := 0; i < len(aw)-1; i++ {
		if aw[i] != bw[i] {
			return false
		}
	}
	x, y := aw[len(aw)-1], bw[len(bw)-1]
	if len(x) > len(y) {
		x, y = y, x
	}
	return y == x+"s" || y == x+"es"
}

// truncationPrefix reports whether the shorter of a and b is a prefix of
// the longer, and the longer exceeds it by at most 2 characters — the
// source's exact truncation rule.
func truncationPrefix(a, b string) bool {
	x, y := a, b
	if len(x) > len(y) {
		x, y = y, x
	}
	return strings.HasPrefix(y, x) && len(y)-len(x) <= 2
}

// DurationsAgree reports whether two scene durations, in seconds, are close
// enough to corroborate a shared title key: |a-b| <= spread.
//
// Duration is never a match on its own — across ~1000 scenes in the source
// catalogue, unrelated titles collided on duration constantly (a
// "duration-only" pass produced 204 hits, all noise). It only corroborates
// or vetoes a match already established by Key or Equivalent: rows sharing
// a key whose durations differ by more than CollisionSpread are two scenes,
// not one.
func DurationsAgree(a, b float64, spread float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= spread
}
