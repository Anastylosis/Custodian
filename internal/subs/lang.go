package subs

import (
	"bufio"
	"io"
	"strings"
	"unicode"
)

// Language detection matters because Stash reads the code from the filename:
// <stem>.en.srt becomes an "en" caption, while a bare <stem>.srt is filed as
// language "00" (unknown). Unknown-language captions cannot coexist — a second
// track would collide on the same filename — so stamping the real code is what
// makes multi-language subtitles possible at all.
//
// Rather than trust the filename (which is frequently wrong or absent), the
// language is detected from the subtitle's own text.

// stopwordSets are high-frequency function words per language. Function words
// are used because they are short, extremely common, and largely disjoint
// across these languages, which makes them reliable on small samples.
var stopwordSets = map[string][]string{
	"en": {"the", "and", "you", "that", "your", "have", "this", "with", "for", "not",
		"are", "was", "what", "just", "like", "know", "don't", "i'm", "it's", "she",
		"they", "but", "get", "him", "her", "how", "why", "can't"},
	"es": {"que", "de", "la", "el", "los", "las", "un", "una", "por", "con", "para",
		"no", "es", "en", "mi", "tu", "su", "te", "se", "lo", "como", "pero", "muy",
		"está", "estoy", "eres", "quiero", "más", "bien", "sí"},
	"pt": {"que", "não", "de", "você", "para", "com", "uma", "meu", "minha", "isso",
		"está", "eu", "ele", "ela", "mas", "muito", "bem", "sim", "por", "como"},
	"fr": {"que", "de", "le", "la", "les", "un", "une", "pour", "avec", "pas", "je",
		"tu", "il", "elle", "est", "dans", "mon", "ma", "mais", "très", "oui", "vous"},
	"de": {"und", "der", "die", "das", "ich", "du", "nicht", "ist", "zu", "den",
		"mit", "ein", "eine", "auf", "für", "aber", "sehr", "ja", "was", "wie"},
	"it": {"che", "di", "il", "la", "un", "una", "per", "con", "non", "sono", "mio",
		"tuo", "ma", "molto", "sì", "come", "questo", "cosa"},
	"nl": {"het", "een", "van", "je", "niet", "dat", "en", "ik", "is", "met", "maar",
		"heel", "wat", "hoe", "voor"},
}

// DetectLanguage returns an ISO-639-1 code for a subtitle's text, plus a
// confidence margin over the runner-up. ok is false when no language could be
// determined with any confidence.
//
// Script is checked first: CJK, Hangul and Cyrillic are unambiguous and do not
// need word statistics.
func DetectLanguage(r io.Reader) (lang string, confidence float64, ok bool) {
	text := readSample(r, 64*1024)
	if strings.TrimSpace(text) == "" {
		return "", 0, false
	}
	if l, found := detectScript(text); found {
		return l, 1, true
	}

	words := tokenizeWords(text)
	if len(words) < 5 {
		return "", 0, false
	}
	counts := map[string]int{}
	for _, w := range words {
		counts[w]++
	}

	scores := map[string]float64{}
	for lang, set := range stopwordSets {
		var hits int
		for _, sw := range set {
			hits += counts[sw]
		}
		scores[lang] = float64(hits) / float64(len(words))
	}

	var best string
	var bestScore, second float64
	for l, s := range scores {
		if s > bestScore {
			second, bestScore, best = bestScore, s, l
		} else if s > second {
			second = s
		}
	}
	// Below this the sample is too thin or the text is not one of the known
	// languages; guessing would produce a wrong caption tag.
	if bestScore < 0.03 {
		return "", 0, false
	}
	return best, bestScore - second, true
}

// detectScript identifies languages by writing system.
func detectScript(text string) (string, bool) {
	var han, kana, hangul, cyrillic, total int
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsDigit(r) || unicode.IsPunct(r) {
			continue
		}
		total++
		switch {
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r):
			kana++
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		}
	}
	if total == 0 {
		return "", false
	}
	frac := func(n int) float64 { return float64(n) / float64(total) }
	switch {
	case frac(hangul) > 0.02:
		return "ko", true
	case frac(kana) > 0.02:
		return "ja", true // kana proves Japanese even when Han dominates
	case frac(han) > 0.02:
		return "zh", true
	case frac(cyrillic) > 0.02:
		return "ru", true
	}
	return "", false
}

// readSample reads up to n bytes, skipping cue numbers and timestamps so the
// statistics see dialogue rather than markup.
func readSample(r io.Reader, n int) string {
	var b strings.Builder
	sc := bufio.NewScanner(io.LimitReader(r, int64(n)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || isAllDigits(line) || strings.Contains(line, "-->") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// tokenizeWords lowercases and splits on non-letters, keeping apostrophes so
// English contractions ("don't", "it's") stay intact as signals.
func tokenizeWords(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || r == '\'' || r == '’' {
			if r == '’' {
				r = '\''
			}
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}
