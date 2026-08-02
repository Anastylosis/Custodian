package subs

import (
	"strings"
	"testing"
	"time"
)

func mins(m, s int) time.Duration {
	return time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}

func scene(id, stem, title string, d time.Duration, performers ...string) SceneRef {
	return SceneRef{
		ID: id, Stem: stem, Title: title, Duration: d,
		Performers: performers, Path: "/data/x/" + stem + ".mp4",
	}
}

func TestRuntime(t *testing.T) {
	srt := `1
00:00:01,000 --> 00:00:02,500
Hello.

2
01:41:47,100 --> 01:41:48,980
Goodbye.
`
	got, ok := Runtime(strings.NewReader(srt))
	if !ok {
		t.Fatal("Runtime returned ok=false")
	}
	if want := time.Hour + mins(41, 48) + 980*time.Millisecond; got != want {
		t.Errorf("Runtime = %v, want %v", got, want)
	}
}

// Cues are not always in order; the maximum must win, not the last line.
func TestRuntime_UnorderedCues(t *testing.T) {
	srt := `1
00:20:00,000 --> 00:20:01,000
later

2
00:00:06,000 --> 00:00:07,000
credits
`
	got, ok := Runtime(strings.NewReader(srt))
	if !ok || got != mins(20, 1) {
		t.Errorf("Runtime = (%v, %v), want (20m1s, true)", got, ok)
	}
}

func TestRuntime_NoTimestamps(t *testing.T) {
	if _, ok := Runtime(strings.NewReader("not a subtitle")); ok {
		t.Error("expected ok=false for a file with no cues")
	}
}

func TestCodes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"venu00765", "VENU-765"},
		{"VENU-765 Chinese", "VENU-765"},
		{"RCTD-590-en", "RCTD-590"},
		{"jul-256 Toyosaki Misato saliva", "JUL-256"},
		{"a1080hd.com@13gvg00367hhb", "GVG-367"},
	} {
		got := Codes(tc.in)
		if !got[tc.want] {
			t.Errorf("Codes(%q) = %v, want it to contain %q", tc.in, keys(got), tc.want)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// "stepMommy" must fold to "mommy" -> "mom". Missing this caused two real
// matches to be reported as unmatched.
func TestTokens_FoldsStepPrefix(t *testing.T) {
	a := Tokens("StudioHandleXXX_Mommy-and-Son-Breeding-Season")
	b := Tokens("2023-05-23_Some.Performer-stepMommy.and.stepSon.Breeding.Season_1080p")
	if !a["mom"] || !b["mom"] {
		t.Fatalf("step prefix not folded: a=%v b=%v", keys(a), keys(b))
	}
	if sim := F1(a, b); sim < 0.6 {
		t.Errorf("similarity = %.2f, want >= 0.60 (a=%v b=%v)", sim, keys(a), keys(b))
	}
}

// The regression that started this: names barely overlap, runtime decides.
func TestMatch_BreedingSeason(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("54548", "2023-05-23_Some.Performer-stepMommy.and.stepSon.Breeding.Season_1080p",
			"stepMommy and stepSon Breeding Season", mins(23, 52), "Some Performer"),
		scene("73661", "Quinn Marsh - Mommy & Son Go Live (Extreme Taboo Breeding) (x265)",
			"", mins(31, 51), "Quinn Marsh"),
		scene("73662", "Quinn Marsh - Mommy & Son Storm Breeding (x265)",
			"", mins(20, 59), "Quinn Marsh"),
	})
	m := ix.Match(Subtitle{
		Stem:    "StudioHandleXXX_Mommy-and-Son-Breeding-Season+(transcribed+on+25-Jul-2024+00-23-01)",
		Runtime: mins(23, 45),
	}, 5)

	best := m.Best()
	if best == nil || best.Scene.ID != "54548" {
		t.Fatalf("best = %+v, want scene 54548 (all: %+v)", best, m.Candidates)
	}
	if m.Verdict != Confirmed {
		t.Errorf("verdict = %s, want CONFIRMED (reasons: %v)", m.Verdict, best.Reasons)
	}
}

// A same-titled decoy 23 minutes away must lose to the one 2 seconds away.
func TestMatch_RuntimeSeparatesIdenticalTitles(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("55739", "2022-10-13_Some.Performer-Nurse.River.Soothes.The.Aching.Patient_1080p",
			"Nurse River Soothes The Aching Patient", mins(10, 54), "Some Performer"),
		scene("75175", "2022-10-13_Some.Performer-Nurse.River.Soothes.The.Aching.Patient_1080p_1",
			"Nurse River Soothes The Aching Patient", mins(33, 49), "Some Performer"),
	})
	m := ix.Match(Subtitle{
		Stem:    "Nurse+River+Soothes+the+Aching+Patient+WM+(transcribed+on+24-Jul-2024+20-55-00)",
		Runtime: mins(10, 52),
	}, 5)
	if b := m.Best(); b == nil || b.Scene.ID != "55739" {
		t.Fatalf("best = %+v, want 55739", b)
	}
	if m.Verdict == Ambiguous || m.Verdict == Unmatched {
		t.Errorf("verdict = %s, want a decisive verdict", m.Verdict)
	}
}

// Dog -> Pet is a real rename in this library; runtime carries the match.
func TestMatch_DogPetSitterRename(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("76466", "The-Reluctant-Pet-Sitter-Part-1", "", mins(11, 40)),
		scene("6278", "Emmas Secret Life in The Dog Sitter", "", mins(14, 54)),
	})
	m := ix.Match(Subtitle{
		Stem:    "thehousenextdoor2024 - The Reluctant Dog Sitter Part 1 - Compressed",
		Runtime: mins(11, 38),
	}, 5)
	if b := m.Best(); b == nil || b.Scene.ID != "76466" {
		t.Fatalf("best = %+v, want 76466", b)
	}
}

// A JAV code should dominate even when the titles share nothing.
func TestMatch_JAVCode(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("60771", "2024-06-20_Hana.Kirsawa_Yui.Tomita_Mei.Arano-RCTD-590_1080p",
			"RCTD-590", mins(132, 1), "Hana Kirsawa"),
		scene("99999", "some other clip", "Unrelated", mins(20, 0)),
	})
	m := ix.Match(Subtitle{Stem: "RCTD-590-en"}, 5)
	if b := m.Best(); b == nil || b.Scene.ID != "60771" {
		t.Fatalf("best = %+v, want 60771", b)
	}
}

// Two identical copies of the same film must not be auto-assigned.
func TestMatch_TrueTieIsAmbiguous(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("52322", "La novia celosa", "", mins(36, 46)),
		scene("69705", "La Novia Celosa", "", mins(36, 50)),
	})
	m := ix.Match(Subtitle{Stem: "La_novia_celosa_1", Runtime: mins(36, 36)}, 5)
	if m.Verdict != Ambiguous {
		t.Errorf("verdict = %s, want AMBIGUOUS (candidates %+v)", m.Verdict, m.Candidates)
	}
}

// Nothing in the library resembles it: say so rather than guessing.
func TestMatch_Unmatched(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("1", "completely different content", "Nothing Alike", mins(9, 0)),
	})
	m := ix.Match(Subtitle{
		Stem:    "thehousenextdoor1 - Your Sinful Aunt Bringing Out Mommys Inner Slut",
		Runtime: mins(18, 3),
	}, 5)
	if m.Verdict != Unmatched && m.Verdict != Ambiguous {
		t.Errorf("verdict = %s, want UNMATCHED/AMBIGUOUS", m.Verdict)
	}
}

// A subtitle that clearly outlives its supposed video cannot belong to it.
//
// A few seconds over is normal noise — Runtime takes the maximum timestamp,
// which includes cue END times — so the tolerance is deliberately generous and
// only a substantial overrun counts as evidence against.
func TestMatch_SubtitleClearlyLongerThanVideoIsRejected(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("77855", "Wren Ashby - Mommy Has a Little Obsession", "", mins(20, 0)),
	})
	m := ix.Match(Subtitle{Stem: "Mommy Has a Little Obsession", Runtime: mins(30, 0)}, 5)
	if m.Verdict == Confirmed || m.Verdict == Likely {
		t.Errorf("verdict = %s for a subtitle 10min longer than the video; want AMBIGUOUS/UNMATCHED", m.Verdict)
	}
}

// A small overrun must NOT disqualify an otherwise good match.
func TestMatch_SmallOverrunIsTolerated(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("1", "2021-06-18_Sable.Vance-Look.into.My.Eyes_1080p",
			"Look into My Eyes", mins(24, 59), "Sable Vance"),
	})
	m := ix.Match(Subtitle{
		Stem:    "2021-06-18_Sable.Vance-Look.into.My.Eyes_1080p",
		Runtime: mins(25, 6),
	}, 5)
	if m.Verdict != Confirmed {
		t.Errorf("verdict = %s, want CONFIRMED (a 7s overrun is within tolerance)", m.Verdict)
	}
}

// An exact filename match is the easy case and must be confirmed outright.
func TestMatch_ExactFilename(t *testing.T) {
	ix := NewIndex([]SceneRef{
		scene("70051", "2021-06-18_Sable.Vance-Look.into.My.Eyes_1080p",
			"Look into My Eyes", mins(11, 50), "Sable Vance"),
	})
	m := ix.Match(Subtitle{
		Stem:    "2021-06-18_Sable.Vance-Look.into.My.Eyes_1080p",
		Runtime: mins(11, 49),
	}, 5)
	if m.Verdict != Confirmed {
		t.Errorf("verdict = %s, want CONFIRMED", m.Verdict)
	}
}

func TestRuntimeFit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sub, scn time.Duration
		wantPos  bool
	}{
		{"just before the end", mins(10, 52), mins(10, 54), true},
		{"way short", mins(10, 0), mins(40, 0), false},
		{"small overrun tolerated", mins(25, 6), mins(24, 59), true},
		{"large overrun rejected", mins(30, 0), mins(20, 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := RuntimeFit(tc.sub, tc.scn)
			if (got > 0) != tc.wantPos {
				t.Errorf("RuntimeFit = %v, wantPositive=%v", got, tc.wantPos)
			}
		})
	}
}
