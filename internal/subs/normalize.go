// Package subs matches loose subtitle files to Stash scenes.
//
// The problem it solves: subtitles that are not named after their video are
// invisible to Stash. It only attaches a caption when the file sits next to the
// video and is named <stem>.srt or <stem>.<lang>.srt, so a subtitle downloaded
// under the scraper's original filename — or left behind when the video was
// moved — is simply never seen.
//
// Matching them back is harder than string comparison, because the two names
// often share almost nothing. These are real pairs from a live library:
//
//	StudioHandleXXX_Mommy-and-Son-Breeding-Season+(transcribed+on+25-Jul-2024).srt
//	  → 2023-05-23_Some.Performer-stepMommy.and.stepSon.Breeding.Season_1080p.mp4
//
//	thehousenextdoor2024 - The Reluctant Dog Sitter - Compressed.srt
//	  → The-Reluctant-Pet-Sitter-Part-1.mp4
//
// Names alone cannot resolve those confidently, so the matcher combines three
// independent signals: studio/JAV codes, normalised title tokens, and — the
// decisive one — the subtitle's own runtime compared against the scene
// duration. A subtitle's last cue lands a few seconds before the video ends,
// which separates same-titled candidates that no amount of string similarity
// can.
package subs

import (
	"regexp"
	"strings"
)

var (
	// Junk that download tools, transcribers and re-encoders bolt onto names.
	noisePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\+?\(transcribed\+?on[^)]*\)`),
		regexp.MustCompile(`(?i)\(\d+\)\s*$`),
		regexp.MustCompile(`(?i)\bcompressed\b`),
		regexp.MustCompile(`(?i)\bwhisper\b`),
		regexp.MustCompile(`(?i)\bsub[\s_]?merge\b`),
		regexp.MustCompile(`(?i)\b\d{3,4}p\b`),
		regexp.MustCompile(`(?i)\b[248]k\b`),
		regexp.MustCompile(`(?i)\b(fhd|uhd|hd)\b`),
		regexp.MustCompile(`(?i)\bx26[45]\b`),
		regexp.MustCompile(`(?i)\b[a-z0-9]+\.com@?`),
	}
	// Trailing 8-char opaque IDs, e.g. "-hwAFtAGB", "_hKlU6KJT".
	randomIDRe = regexp.MustCompile(`[-_]([A-Za-z0-9]{8})$`)
	// Percent-ish UTF-8 escapes some scrapers leave behind: _C3_A9 → é.
	hexEscapeRe = regexp.MustCompile(`_([0-9A-Fa-f]{2})_([0-9A-Fa-f]{2})`)
	// Studio/JAV code: 2-6 letters, optional separator, 2-5 digits.
	codeRe = regexp.MustCompile(`(?i)\b([A-Za-z]{2,6})[-_ ]?(\d{2,5})\b`)
	// Fallback for codes buried inside a run with no word boundary, as release
	// sites produce: "a1080hd.com@13gvg00367hhb" hides GVG-367. Only used when
	// the anchored pattern finds nothing, since it is far easier to fool.
	embeddedCodeRe = regexp.MustCompile(`(?i)([A-Za-z]{2,6})(\d{2,5})`)
	// A trailing language tag on the stem.
	langTailRe = regexp.MustCompile(`(?i)[-_. ]+(en|es|jp|ja|zh|cn|ko|fr|de|it|pt|ru|eng|spa|jpn|chi)$`)
	nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

// stopWords are too common to carry meaning in a title.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "with": true, "your": true,
	"you": true, "my": true, "me": true, "of": true, "to": true, "in": true,
	"on": true, "for": true, "is": true, "it": true, "at": true, "by": true,
	"from": true, "s": true, "t": true, "part": true, "pt": true, "vol": true,
}

// handleWords are channel/studio handles that appear in a subtitle filename but
// not in the scene title. Dropping them stops two unrelated clips from the same
// creator scoring as a match on the handle alone.
var handleWords = map[string]bool{
	"thehousenextdoor1": true, "thehousenextdoor2024": true,
	"studiohandlexxx": true, "somehandle": true, "sweethandle": true,
	"studioname26": true, "somehandle": true,
}

// synonyms fold the vocabulary that differs between a subtitle's name and the
// scene title for the same video. The "step" prefix is the important one:
// "stepMommy" and "Mommy" are the same word for matching purposes, and missing
// that caused real matches to be scored as unmatched.
var synonyms = map[string]string{
	"mommy": "mom", "mother": "mom", "momma": "mom", "mum": "mom", "stepmom": "mom",
	"sister": "sis", "sis": "sis",
	"dog":  "pet", // "Dog Sitter" and "Pet Sitter" are the same series
	"cant": "cant", "can": "cant",
}

// unescapeHex turns _C3_A9 style escapes back into their UTF-8 character.
func unescapeHex(s string) string {
	return hexEscapeRe.ReplaceAllStringFunc(s, func(m string) string {
		g := hexEscapeRe.FindStringSubmatch(m)
		if len(g) != 3 {
			return m
		}
		b := []byte{hexByte(g[1]), hexByte(g[2])}
		if r := string(b); isValidUTF8(r) {
			return r
		}
		return m
	})
}

func hexByte(s string) byte {
	var v byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= c - '0'
		case c >= 'a' && c <= 'f':
			v |= c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v |= c - 'A' + 10
		}
	}
	return v
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// StripNoise removes scraper and transcoder junk from a filename stem.
func StripNoise(stem string) string {
	s := unescapeHex(stem)
	s = strings.ReplaceAll(s, "+", " ")
	s = strings.ReplaceAll(s, "%20", " ")
	for _, re := range noisePatterns {
		s = re.ReplaceAllString(s, " ")
	}
	s = randomIDRe.ReplaceAllString(s, "")
	s = langTailRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// Tokens reduces a name to its comparable words: noise stripped, punctuation
// flattened, stop words and creator handles dropped, synonyms folded.
func Tokens(s string) map[string]bool {
	s = StripNoise(s)
	s = nonAlnumRe.ReplaceAllString(s, " ")
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		if len(w) < 2 || stopWords[w] || handleWords[w] {
			continue
		}
		if isAllDigits(w) {
			continue
		}
		w = strings.TrimPrefix(w, "step")
		if w == "" || stopWords[w] {
			continue
		}
		if syn, ok := synonyms[w]; ok {
			w = syn
		}
		out[w] = true
	}
	return out
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// nonCodePrefixes are letter runs that look like a studio code but aren't.
var nonCodePrefixes = map[string]bool{
	"mp": true, "av": true, "hd": true, "part": true, "pt": true,
	"vol": true, "cd": true, "ep": true, "x": true, "s": true,
}

// Codes extracts studio/JAV identifiers, normalised to ABC-123 with leading
// zeros removed so "venu00765" and "VENU-765" compare equal. These are the
// strongest single signal available when present.
func Codes(s string) map[string]bool {
	in := unescapeHex(s)
	out := collectCodes(codeRe.FindAllStringSubmatch(in, -1))
	if len(out) == 0 {
		out = collectCodes(embeddedCodeRe.FindAllStringSubmatch(in, -1))
	}
	return out
}

func collectCodes(matches [][]string) map[string]bool {
	out := map[string]bool{}
	for _, m := range matches {
		letters := strings.ToUpper(m[1])
		if nonCodePrefixes[strings.ToLower(letters)] || len(letters) < 2 {
			continue
		}
		digits := strings.TrimLeft(m[2], "0")
		if digits == "" {
			digits = "0"
		}
		out[letters+"-"+digits] = true
	}
	return out
}

// F1 is the harmonic mean of precision and recall between two token sets. It
// is symmetric-ish and penalises both missing and extra words, which suits
// comparing a filename against a title where either may carry more detail.
func F1(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	var inter int
	for w := range a {
		if b[w] {
			inter++
		}
	}
	if inter == 0 {
		return 0
	}
	p := float64(inter) / float64(len(b))
	r := float64(inter) / float64(len(a))
	return 2 * p * r / (p + r)
}

// Overlap counts tokens present in both sets.
func Overlap(a, b map[string]bool) int {
	var n int
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n
}
