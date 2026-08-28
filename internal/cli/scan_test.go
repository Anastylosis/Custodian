package cli

import (
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/evidence"
	stash "github.com/Anastylosis/stash-go"
)

func TestFormatSummary(t *testing.T) {
	summary := evidence.Summary{
		Certain: 843, CertainBytes: 967307411456, // ~0.88 TiB
		Conflicted: 410, ConflictedBytes: 461611606016, // ~0.42 TiB
		Dismissed: 2, DismissedBytes: 0,
		Duration: 340 * time.Millisecond,
	}
	got := formatSummary(summary)
	want := "scan\n" +
		"  certain:     843 (900.88 GiB)\n" +
		"  conflicted:  410 (429.91 GiB)\n" +
		"  probable:    0 (0 B) — review only, plan does not choose a keeper yet\n" +
		"  rejected:    0 (0 B)\n" +
		"  dismissed:   2 (0 B)\n" +
		"  duration:    340ms\n"
	if got != want {
		t.Errorf("formatSummary =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatSummaryZero(t *testing.T) {
	got := formatSummary(evidence.Summary{})
	want := "scan\n" +
		"  certain:     0 (0 B)\n" +
		"  conflicted:  0 (0 B)\n" +
		"  probable:    0 (0 B) — review only, plan does not choose a keeper yet\n" +
		"  rejected:    0 (0 B)\n" +
		"  dismissed:   0 (0 B)\n" +
		"  duration:    0s\n"
	if got != want {
		t.Errorf("formatSummary(zero) =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatSummaryListsGeneratorsInOrder(t *testing.T) {
	summary := evidence.Summary{
		Generators: map[string]evidence.GeneratorSummary{
			"phash":      {Found: 11, New: 11, Known: 0},
			"stash_id":   {Found: 1253, New: 1200, Known: 53},
			"title_date": {Found: 19, New: 19, Known: 0},
		},
	}
	got := formatSummary(summary)
	want := "scan\n" +
		"  generators:\n" +
		"    stash_id   found 1253 (new 1200, known 53)\n" +
		"    phash      found 11 (new 11, known 0)\n" +
		"    title_date found 19 (new 19, known 0)\n" +
		"  certain:     0 (0 B)\n" +
		"  conflicted:  0 (0 B)\n" +
		"  probable:    0 (0 B) — review only, plan does not choose a keeper yet\n" +
		"  rejected:    0 (0 B)\n" +
		"  dismissed:   0 (0 B)\n" +
		"  duration:    0s\n"
	if got != want {
		t.Errorf("formatSummary(generators) =\n%q\nwant\n%q", got, want)
	}
}

func TestBuildGeneratorsSkipsPhashWithoutURL(t *testing.T) {
	gens, notes := buildGenerators([]string{"stash_id", "phash"}, &config.Config{}, nil)
	if len(gens) != 1 {
		t.Fatalf("gens = %d, want 1 (phash skipped)", len(gens))
	}
	if len(notes) != 1 || notes[0] != "skipping phash: no stash.url configured" {
		t.Errorf("notes = %v, want a note about phash", notes)
	}
}

func TestBuildGeneratorsIncludesPhashWithURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.Stash.URL = "http://example.test:9999"
	cfg.Scan.PhashDistance = 4
	cfg.Scan.DurationDiffSeconds = 1
	gens, notes := buildGenerators([]string{"phash"}, cfg, stash.NewClient(cfg.Stash.URL))
	if len(gens) != 1 {
		t.Fatalf("gens = %d, want 1", len(gens))
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
	ph, ok := gens[0].(evidence.PhashGenerator)
	if !ok {
		t.Fatalf("gens[0] = %T, want evidence.PhashGenerator", gens[0])
	}
	if ph.Distance != 4 || ph.DurationDiff != 1 {
		t.Errorf("PhashGenerator = %+v, want distance 4, duration_diff 1", ph)
	}
}

func TestBuildGeneratorsNotesUnknownName(t *testing.T) {
	gens, notes := buildGenerators([]string{"bogus"}, &config.Config{}, nil)
	if len(gens) != 0 {
		t.Errorf("gens = %d, want 0", len(gens))
	}
	if len(notes) != 1 || notes[0] != `skipping unknown generator "bogus"` {
		t.Errorf("notes = %v, want a note about the unknown generator", notes)
	}
}

func TestBuildGeneratorsDefaultListCoversEveryName(t *testing.T) {
	gens, notes := buildGenerators(allGenerators, &config.Config{}, nil)
	if len(notes) != 1 {
		t.Errorf("notes = %v, want just the phash skip note (no stash.url)", notes)
	}
	// stash_id, multifile, filename, title_date — phash skipped.
	if len(gens) != 4 {
		t.Errorf("gens = %d, want 4", len(gens))
	}
}
