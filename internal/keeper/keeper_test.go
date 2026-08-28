package keeper

import (
	"reflect"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

func group(ids ...string) ledger.Group {
	return ledger.Group{Signature: "sig", SceneIDs: ids, Verdict: ledger.VerdictCertain}
}

func TestChooseTierDecides(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"9904": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000},
		"9905": {Width: 1280, Height: 720, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000},
	}
	got := Choose(group("9904", "9905"), files, DefaultRules())
	if got.Keeper != "9904" {
		t.Fatalf("Keeper = %q, want 9904", got.Keeper)
	}
	if !reflect.DeepEqual(got.Losers, []string{"9905"}) {
		t.Fatalf("Losers = %v, want [9905]", got.Losers)
	}
	want := []Step{{Rule: "tier", Detail: "1080p vs 720p", Outcome: "9904 wins"}}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("Steps = %+v, want %+v", got.Steps, want)
	}
}

// TestChooseBitrateTieFallsThroughToCodec pins Steps for a case where tier
// and bitrate tie and codec preference decides — REBUILD.md §4.3's example
// trail.
func TestChooseBitrateTieFallsThroughToCodec(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"9904": {Width: 1920, Height: 1080, BitRate: 14_500_000, VideoCodec: "hevc", Size: 1_000_000},
		"9905": {Width: 1920, Height: 1080, BitRate: 14_400_000, VideoCodec: "h264", Size: 1_000_000},
	}
	got := Choose(group("9904", "9905"), files, DefaultRules())
	if got.Keeper != "9904" {
		t.Fatalf("Keeper = %q, want 9904", got.Keeper)
	}
	want := []Step{
		{Rule: "tier", Detail: "1080p vs 1080p", Outcome: "tie"},
		{Rule: "bitrate", Detail: "14.5 vs 14.4 Mbps", Outcome: "tie under 10%"},
		{Rule: "codec", Detail: "hevc vs h264", Outcome: "9904 wins"},
	}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("Steps =\n%+v\nwant\n%+v", got.Steps, want)
	}
}

func TestChooseBitrateDecidesAboveSignificance(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, BitRate: 16_000_000, VideoCodec: "hevc", Size: 1_000_000},
		"2": {Width: 1920, Height: 1080, BitRate: 10_000_000, VideoCodec: "hevc", Size: 1_000_000},
	}
	got := Choose(group("1", "2"), files, DefaultRules())
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1 (higher bitrate)", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "bitrate" || last.Outcome != "1 wins" {
		t.Errorf("deciding step = %+v, want bitrate/1 wins", last)
	}
}

func TestChooseSizeDecides(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 2_000_000_000},
		"2": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000_000},
	}
	got := Choose(group("1", "2"), files, DefaultRules())
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1 (larger file)", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "size" || last.Outcome != "1 wins" {
		t.Errorf("deciding step = %+v, want size/1 wins", last)
	}
}

func TestChooseSizeTiesUnderSignificance(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000_000, Path: "/data/organized/a.mp4"},
		"2": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_050_000_000, Path: "/data/tmp/b.mp4"},
	}
	got := Choose(group("1", "2"), files, DefaultRules())
	// Size ties (5% apart, under the 10% significance); location decides:
	// scene 1 is filed, scene 2 is staging.
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1 (filed beats staging once size ties)", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "location" || last.Outcome != "1 wins" {
		t.Errorf("deciding step = %+v, want location/1 wins", last)
	}
}

func TestChooseFiledBeatsStaging(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/data/organized/a.mp4"},
		"2": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/data/tmp3/b.mp4"},
	}
	got := Choose(group("1", "2"), files, DefaultRules())
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1 (filed)", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "location" || last.Detail != "filed vs staging" || last.Outcome != "1 wins" {
		t.Errorf("deciding step = %+v, want location/filed vs staging/1 wins", last)
	}
}

func TestChooseOlderSceneIDIsFinal(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"200": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/data/organized/a.mp4"},
		"100": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/data/organized/b.mp4"},
	}
	got := Choose(group("100", "200"), files, DefaultRules())
	if got.Keeper != "100" {
		t.Fatalf("Keeper = %q, want 100 (older, lower id)", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "scene_id" || last.Outcome != "100 wins (final)" {
		t.Errorf("deciding step = %+v, want scene_id/100 wins (final)", last)
	}
}

func TestChooseThreeSceneGroup(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		// 1080p, best.
		"1": {Width: 1920, Height: 1080, BitRate: 14_000_000, VideoCodec: "hevc", Size: 3_000_000_000},
		// 720p, loses on tier.
		"2": {Width: 1280, Height: 720, BitRate: 14_000_000, VideoCodec: "hevc", Size: 3_000_000_000},
		// 1080p but lower bitrate, loses to scene 1.
		"3": {Width: 1920, Height: 1080, BitRate: 8_000_000, VideoCodec: "hevc", Size: 3_000_000_000},
	}
	got := Choose(group("1", "2", "3"), files, DefaultRules())
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1", got.Keeper)
	}
	if len(got.Losers) != 2 {
		t.Fatalf("Losers = %v, want 2 entries", got.Losers)
	}
	for _, want := range []string{"2", "3"} {
		found := false
		for _, l := range got.Losers {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Losers %v missing %q", got.Losers, want)
		}
	}
}

func TestChooseSingleSceneGroup(t *testing.T) {
	got := Choose(group("42"), map[string]ledger.PrimaryFile{"42": {}}, DefaultRules())
	if got.Keeper != "42" {
		t.Fatalf("Keeper = %q, want 42", got.Keeper)
	}
	if len(got.Losers) != 0 {
		t.Errorf("Losers = %v, want none", got.Losers)
	}
}

func TestChooseEmptyGroup(t *testing.T) {
	got := Choose(group(), nil, DefaultRules())
	if got.Keeper != "" || got.Losers != nil || got.Steps != nil {
		t.Errorf("Choose(empty) = %+v, want zero value", got)
	}
}

func TestChooseUsesCustomStagingPrefixesWithoutFiled(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/mnt/library/a.mp4"},
		"2": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/mnt/incoming/b.mp4"},
	}
	rules := DefaultRules()
	rules.Filed = nil
	rules.StagingPrefixes = []string{"/mnt/incoming"}
	got := Choose(group("1", "2"), files, rules)
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1 (custom staging prefix honoured)", got.Keeper)
	}
}

func TestChooseUnknownCodecRanksWorst(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000},
		"2": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "", Size: 1_000_000},
	}
	got := Choose(group("1", "2"), files, DefaultRules())
	if got.Keeper != "1" {
		t.Fatalf("Keeper = %q, want 1 (known codec beats unknown)", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "codec" || last.Detail != "hevc vs unknown" {
		t.Errorf("deciding step = %+v, want codec/hevc vs unknown", last)
	}
}

func TestChooseBothStagingFallsThroughToSceneID(t *testing.T) {
	files := map[string]ledger.PrimaryFile{
		"200": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/data/tmp/a.mp4"},
		"100": {Width: 1920, Height: 1080, BitRate: 5_000_000, VideoCodec: "hevc", Size: 1_000_000, Path: "/data/nosort/b.mp4"},
	}
	got := Choose(group("100", "200"), files, DefaultRules())
	if got.Keeper != "100" {
		t.Fatalf("Keeper = %q, want 100", got.Keeper)
	}
	last := got.Steps[len(got.Steps)-1]
	if last.Rule != "scene_id" {
		t.Errorf("deciding step = %+v, want scene_id (both staging tied)", last)
	}
	// The location rule immediately before it should record the tie.
	loc := got.Steps[len(got.Steps)-2]
	if loc.Rule != "location" || loc.Detail != "both staging" || loc.Outcome != "tie" {
		t.Errorf("location step = %+v, want both staging/tie", loc)
	}
}

func TestOlderSceneIDNonNumericFallback(t *testing.T) {
	winner, _ := olderSceneID("abc", "abd")
	if winner != "abc" {
		t.Errorf("olderSceneID(abc, abd) = %q, want abc", winner)
	}
}

func TestNormalizeCodecMapsH265ToHEVC(t *testing.T) {
	if got := normalizeCodec("H.265"); got != "hevc" {
		t.Errorf("normalizeCodec(H.265) = %q, want hevc", got)
	}
	if got := normalizeCodec("HEVC"); got != "hevc" {
		t.Errorf("normalizeCodec(HEVC) = %q, want hevc", got)
	}
}
