package subs

import "testing"

func TestVocab_IsCreator(t *testing.T) {
	v := NewVocab([]string{"River Stone", "The House Next Door", "Quinn Marsh"})
	tests := []struct {
		tok  string
		want bool
	}{
		// Squashed handles, with and without scraper suffixes.
		{"riverstone", true},
		{"riverstonexxx", true},
		{"thehousenextdoor", true},
		{"thehousenextdoor2024", true},

		// Individual name words are NOT creator words: alias lists are full of
		// ordinary vocabulary, and suppressing those destroys title matching.
		{"river", false},
		{"stone", false},
		{"quinn", false},

		{"nesting", false},
		{"sitter", false},
		{"mom", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := v.IsCreator(tc.tok); got != tc.want {
			t.Errorf("IsCreator(%q) = %v, want %v", tc.tok, got, tc.want)
		}
	}
}

// Short names must not become prefixes that swallow unrelated words.
func TestVocab_ShortNamesDoNotOverMatch(t *testing.T) {
	v := NewVocab([]string{"CJ", "VIP"})
	for _, tok := range []string{"cjhospital", "vipers", "vipsomething"} {
		if v.IsCreator(tok) {
			t.Errorf("IsCreator(%q) = true; a 2-3 char name must not match as a prefix", tok)
		}
	}
}

// The reason creator words are split rather than deleted: real alias lists
// contain ordinary words, and dropping them would destroy genuine title words.
func TestVocab_SplitKeepsContentWords(t *testing.T) {
	v := NewVocab([]string{"Molly", "Vanity", "Sinful"}) // real-world alias shapes
	content, creator := v.Split(Tokens("Molly Catches Her Sinful Step-Son Watching"))

	for _, w := range []string{"molly", "catches", "sinful", "watching"} {
		if !content[w] {
			t.Errorf("expected %q to survive as a content word, got content=%v", w, keys(content))
		}
	}
	if len(creator) != 0 {
		t.Errorf("short single-word aliases must not become creator handles, got %v", keys(creator))
	}
}

// Two unrelated clips by one creator must not look alike on title similarity.
func TestVocab_CreatorWordsDoNotInflateTitleSimilarity(t *testing.T) {
	v := NewVocab([]string{"River Stone"})

	a, _ := v.Split(Tokens("RiverStoneXXX - Nesting Season"))
	b, _ := v.Split(Tokens("RiverStoneXXX - Prom Night"))
	if sim := F1(a, b); sim > 0 {
		t.Errorf("unrelated clips by one creator scored %.2f on content similarity, want 0", sim)
	}

	// Without the split they would share the handle token and look similar.
	if sim := F1(Tokens("RiverStoneXXX - Nesting Season"), Tokens("RiverStoneXXX - Prom Night")); sim == 0 {
		t.Error("expected the raw tokens to collide on the handle; the split is what removes it")
	}
}

func TestVocab_NilIsInert(t *testing.T) {
	var v *Vocab
	if v.IsCreator("anything") {
		t.Error("nil Vocab claimed a creator word")
	}
	if v.Len() != 0 {
		t.Errorf("nil Vocab Len = %d", v.Len())
	}
	content, creator := v.Split(Tokens("Some Title Here"))
	if len(creator) != 0 {
		t.Errorf("nil Vocab produced creator words: %v", keys(creator))
	}
	if len(content) == 0 {
		t.Error("nil Vocab dropped all content words")
	}
}
