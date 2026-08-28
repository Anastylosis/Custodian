package sidecar

import (
	"reflect"
	"testing"
)

func TestStemOf(t *testing.T) {
	tests := []struct{ in, want string }{
		{"movie.mp4", "movie"},
		{"a.b.en.srt", "a.b.en"},
		{"noext", "noext"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := stemOf(tc.in); got != tc.want {
			t.Errorf("stemOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsSidecarOf(t *testing.T) {
	tests := []struct {
		name       string
		video      string
		candidate  string
		wantSuffix string
		wantOK     bool
	}{
		{"plain srt", "Foo.mp4", "Foo.srt", ".srt", true},
		{"language srt", "Foo.mp4", "Foo.en.srt", ".en.srt", true},
		{"three letter lang", "Foo.mp4", "Foo.eng.srt", ".eng.srt", true},
		{"ass subtitle", "Foo.mp4", "Foo.ass", ".ass", true},
		{"case insensitive stem", "Foo.mp4", "FOO.srt", ".srt", true},
		// Extension case is preserved, not normalised: a Foo.SRT should land
		// as Bar.SRT rather than silently changing case on the destination.
		{"uppercase extension", "Foo.mp4", "Foo.SRT", ".SRT", true},
		{"dots in video name", "a.b.c_1080p.mp4", "a.b.c_1080p.en.srt", ".en.srt", true},
		{"apostrophe in name", "Mommy's Secret.mp4", "Mommy's Secret.srt", ".srt", true},

		{"prefix but not boundary", "Foo.mp4", "Foobar.srt", "", false},
		{"different name", "Foo.mp4", "Bar.srt", "", false},
		{"not a subtitle ext", "Foo.mp4", "Foo.nfo", "", false},
		{"the video itself", "Foo.mp4", "Foo.mp4", "", false},
		{"empty candidate", "Foo.mp4", "", "", false},
		{"exact stem no suffix", "Foo.mp4", "Foo", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSuffix, gotOK := IsSidecarOf(tc.video, tc.candidate, nil)
			if gotOK != tc.wantOK || gotSuffix != tc.wantSuffix {
				t.Errorf("IsSidecarOf(%q,%q) = (%q,%v), want (%q,%v)",
					tc.video, tc.candidate, gotSuffix, gotOK, tc.wantSuffix, tc.wantOK)
			}
		})
	}
}

func TestPlan_PreservesLanguageSuffix(t *testing.T) {
	siblings := []string{
		"2023-05-27_River.Stone-Nightshift_1080p.mp4",
		"2023-05-27_River.Stone-Nightshift_1080p.en.srt",
		"2023-05-27_River.Stone-Nightshift_1080p.es.srt",
		"unrelated.mp4",
		"unrelated.srt",
	}
	got := Plan(
		"2023-05-27_River.Stone-Nightshift_1080p.mp4",
		"River Stone/2024-01-01_New.Name_4k.mkv",
		siblings, nil,
	)
	// dstVideo is passed as a basename by callers; stemOf handles the ext only.
	want := []Move{
		{SrcBasename: "2023-05-27_River.Stone-Nightshift_1080p.en.srt",
			DstBasename: "River Stone/2024-01-01_New.Name_4k.en.srt", Suffix: ".en.srt"},
		{SrcBasename: "2023-05-27_River.Stone-Nightshift_1080p.es.srt",
			DstBasename: "River Stone/2024-01-01_New.Name_4k.es.srt", Suffix: ".es.srt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan() =\n %+v\nwant\n %+v", got, want)
	}
}

// The critical correctness case: a sidecar that suffix-matches two videos
// belongs to the more specific one, and must not be stolen.
func TestPlan_DoesNotStealMoreSpecificVideosSidecar(t *testing.T) {
	siblings := []string{
		"Show.mp4",
		"Show.srt",
		"Show.part2.mp4",
		"Show.part2.srt",
		"Show.part2.en.srt",
	}
	got := Plan("Show.mp4", "Renamed.mp4", siblings, nil)
	want := []Move{
		{SrcBasename: "Show.srt", DstBasename: "Renamed.srt", Suffix: ".srt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan() stole a sibling's sidecar.\n got %+v\nwant %+v", got, want)
	}
}

// ...and the more specific video still collects its own.
func TestPlan_MoreSpecificVideoKeepsItsOwn(t *testing.T) {
	siblings := []string{
		"Show.mp4", "Show.srt",
		"Show.part2.mp4", "Show.part2.srt", "Show.part2.en.srt",
	}
	got := Plan("Show.part2.mp4", "P2.mkv", siblings, nil)
	want := []Move{
		{SrcBasename: "Show.part2.en.srt", DstBasename: "P2.en.srt", Suffix: ".en.srt"},
		{SrcBasename: "Show.part2.srt", DstBasename: "P2.srt", Suffix: ".srt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan() =\n %+v\nwant\n %+v", got, want)
	}
}

func TestPlan_NoSidecars(t *testing.T) {
	if got := Plan("Foo.mp4", "Bar.mp4", []string{"Foo.mp4", "Other.srt"}, nil); got != nil {
		t.Errorf("Plan() = %+v, want nil", got)
	}
}

func TestPlan_CustomExtensions(t *testing.T) {
	siblings := []string{"Foo.mp4", "Foo.srt", "Foo.nfo"}
	got := Plan("Foo.mp4", "Bar.mp4", siblings, []string{"nfo"})
	want := []Move{{SrcBasename: "Foo.nfo", DstBasename: "Bar.nfo", Suffix: ".nfo"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Plan() = %+v, want %+v", got, want)
	}
}

// Real regression: these are the exact filenames that were orphaned in the
// wild when organize moved videos out of tmp2/ into performer folders.
func TestPlan_RealWorldOrphanCase(t *testing.T) {
	siblings := []string{
		"2020-07-03_Wren.Ashby-Flirting.with.Mom.Through.the.Decades_4k.mp4",
		"2020-07-03_Wren.Ashby-Flirting.with.Mom.Through.the.Decades_4k.srt",
	}
	got := Plan(
		"2020-07-03_Wren.Ashby-Flirting.with.Mom.Through.the.Decades_4k.mp4",
		"2020-07-03_Wren.Ashby-Flirting.with.Mom.Through.the.Decades_4k.mp4",
		siblings, nil,
	)
	if len(got) != 1 || got[0].Suffix != ".srt" {
		t.Fatalf("Plan() = %+v, want one .srt move", got)
	}
}
