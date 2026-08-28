package textmatch

import "testing"

// TestKey ports stash-catalog-sync/scripts/selftest.py's norm() cases and
// the PITFALLS.md "Matching" examples verbatim. Cases marked "MUST NOT
// MATCH" are as important as the ones that must.
func TestKey(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{
			name: "HTML entity decodes to plain apostrophe (PITFALLS.md: double-encoded &#039;)",
			a:    "You&#039;re the Lucky Winner",
			b:    "You're the Lucky Winner",
			want: true,
		},
		{
			name: "possessive strips to plain plural (PITFALLS.md: apostrophe stripped, not spaced)",
			a:    "Husband's Big Dick",
			b:    "Husbands Big Dick",
			want: true,
		},
		{
			name: "step-Mom folds to Mom",
			a:    "step-Mom Must Measure",
			b:    "Mom Must Measure",
			want: true,
		},
		{
			name: "step-Mom's folds and absorbs the possessive (PITFALLS.md: apostrophe fix collides with step-folding)",
			a:    "step-Mom's Cellphone Discovery",
			b:    "Mom's Cellphone Discovery",
			want: true,
		},
		{
			name: "concatenated Stepdads folds (PITFALLS.md: allow a missing separator)",
			a:    "Stepdads Monster Cock",
			b:    "step dads monster cock",
			want: true,
		},
		{
			name: "4K/HD suffix stripped",
			a:    "Auntie Needs Attention 4K",
			b:    "Auntie Needs Attention HD",
			want: true,
		},
		{
			name: "Part I != Part II (MUST NOT MATCH)",
			a:    "Mom is a Cougar Part I",
			b:    "Mom is a Cougar Part II",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ka, kb := Key(c.a), Key(c.b)
			if got := ka == kb; got != c.want {
				t.Errorf("Key(%q)=%q, Key(%q)=%q, equal=%v, want %v", c.a, ka, c.b, kb, got, c.want)
			}
		})
	}
}

// TestKeyStable checks Key is idempotent and produces the exact expected
// string for a few cases, catching accidental changes to normalisation
// order (PITFALLS.md: "order matters... changing it breaks things").
func TestKeyStable(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"You&#039;re the Lucky Winner", "youre the lucky winner"},
		{"Husband's Big Dick", "husbands big dick"},
		{"step-Mom Must Measure", "mom must measure"},
		{"Stepdads Monster Cock", "dads monster cock"},
		{"Auntie Needs Attention 4K", "auntie needs attention"},
	}
	for _, c := range cases {
		if got := Key(c.in); got != c.want {
			t.Errorf("Key(%q) = %q, want %q", c.in, got, c.want)
		}
		if got, again := Key(c.in), Key(Key(c.in)); got != again {
			t.Errorf("Key(%q) is not idempotent: %q vs %q", c.in, got, again)
		}
	}
}

// TestEquivalent covers the provable equivalence classes and the
// PITFALLS.md "MUST NOT MATCH" examples that sank character-ratio fuzzing:
// difflib at >=0.90 similarity paired each of these wrongly.
func TestEquivalent(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{
			name: "equal keys",
			a:    "The Sperm Addict",
			b:    "the sperm addict",
			want: true,
		},
		{
			name: "equal after de-spacing",
			a:    "Ice Cream Truck",
			b:    "Icecream Truck",
			want: true,
		},
		{
			name: "plural/singular confined to the last word",
			a:    "Two Naughty Cousins",
			b:    "Two Naughty Cousin",
			want: true,
		},
		{
			name: "truncation prefix, within 2 characters",
			a:    "Reluctant Mal",
			b:    "Reluctant Male",
			want: true,
		},
		{
			name: "truncation prefix, exceeds 2 characters (MUST NOT MATCH)",
			a:    "Cous",
			b:    "Cousine",
			want: false,
		},
		{
			name: "hopping vs Shopping (MUST NOT MATCH, PITFALLS.md difflib 0.90 false positive)",
			a:    "hopping",
			b:    "Shopping",
			want: false,
		},
		{
			name: "clamping vs Camping (MUST NOT MATCH, PITFALLS.md difflib 0.90 false positive)",
			a:    "clamping",
			b:    "Camping",
			want: false,
		},
		{
			name: "reluctant male vs reluctant female (MUST NOT MATCH, PITFALLS.md: inverts meaning)",
			a:    "reluctant male",
			b:    "reluctant female",
			want: false,
		},
		{
			name: "Part I vs Part II (MUST NOT MATCH)",
			a:    "Mom is a Cougar Part I",
			b:    "Mom is a Cougar Part II",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Equivalent(c.a, c.b); got != c.want {
				t.Errorf("Equivalent(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
			}
		})
	}
}

func TestDurationsAgree(t *testing.T) {
	cases := []struct {
		name   string
		a, b   float64
		spread float64
		want   bool
	}{
		{"identical durations", 1804, 1804, CollisionSpread, true},
		{"within spread", 1000, 1090, CollisionSpread, true},
		{"exactly at spread, inclusive", 100, 100 + CollisionSpread, CollisionSpread, true},
		{"just past spread", 100, 100 + CollisionSpread + 1, CollisionSpread, false},
		{"order does not matter", 1090, 1000, CollisionSpread, true},
		{"large spread, title collision (PITFALLS.md: two unrelated scenes)", 1804, 2500, CollisionSpread, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DurationsAgree(c.a, c.b, c.spread); got != c.want {
				t.Errorf("DurationsAgree(%v, %v, %v) = %v, want %v", c.a, c.b, c.spread, got, c.want)
			}
		})
	}
}

func TestCollisionSpreadValue(t *testing.T) {
	if CollisionSpread != 120 {
		t.Errorf("CollisionSpread = %v, want 120 (PITFALLS.md: caught 6 bad date corrections out of 73 candidates)", CollisionSpread)
	}
}
