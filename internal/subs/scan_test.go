package subs

import (
	"context"
	"testing"

	"github.com/Anastylosis/Custodian/internal/vfs"
)

// englishText has enough English stopwords for DetectLanguage to settle on
// "en" with real confidence.
const englishText = `1
00:00:01,000 --> 00:00:04,000
The and you that your have this with for not

2
00:00:05,000 --> 00:01:00,000
I know what you are and this is not what I have
`

func TestCollectFindsSubtitlesByExtension(t *testing.T) {
	m := vfs.NewMem("/lib/Ep01.srt", "/lib/Ep01.mkv", "/lib/readme.txt")
	m.Contents["/lib/Ep01.srt"] = englishText

	subs, problems, err := Collect(context.Background(), m, "/lib", nil, false)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none", problems)
	}
	if len(subs) != 1 {
		t.Fatalf("subs = %d, want 1", len(subs))
	}
	got := subs[0]
	if got.Path != "/lib/Ep01.srt" {
		t.Errorf("Path = %q", got.Path)
	}
	if got.Stem != "Ep01" {
		t.Errorf("Stem = %q, want Ep01", got.Stem)
	}
	if got.Lang != "en" {
		t.Errorf("Lang = %q, want en", got.Lang)
	}
	if got.Runtime == 0 {
		t.Error("Runtime = 0, want the last cue time")
	}
}

func TestCollectRecursesOnlyWhenAsked(t *testing.T) {
	m := vfs.NewMem("/lib/Show/S01/Ep01.srt", "/lib/loose.srt")
	m.Contents["/lib/Show/S01/Ep01.srt"] = englishText
	m.Contents["/lib/loose.srt"] = englishText

	flat, _, err := Collect(context.Background(), m, "/lib", nil, false)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(flat) != 1 || flat[0].Path != "/lib/loose.srt" {
		t.Errorf("non-recursive Collect = %+v, want only /lib/loose.srt", flat)
	}

	deep, _, err := Collect(context.Background(), m, "/lib", nil, true)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(deep) != 2 {
		t.Fatalf("recursive Collect = %d, want 2", len(deep))
	}
}

func TestCollectRecordsDirsRelativeToTheScanRoot(t *testing.T) {
	m := vfs.NewMem("/lib/Show/S01/Ep01.srt")
	m.Contents["/lib/Show/S01/Ep01.srt"] = englishText

	subs, _, err := Collect(context.Background(), m, "/lib", nil, true)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subs = %d, want 1", len(subs))
	}
	want := []string{"Show", "S01"}
	got := subs[0].Dirs
	if len(got) != len(want) {
		t.Fatalf("Dirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Dirs = %v, want %v", got, want)
		}
	}
}

func TestCollectRespectsExplicitExtensions(t *testing.T) {
	m := vfs.NewMem("/lib/a.srt", "/lib/b.vtt")
	m.Contents["/lib/a.srt"] = englishText
	m.Contents["/lib/b.vtt"] = englishText

	subs, _, err := Collect(context.Background(), m, "/lib", []string{"vtt"}, false)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(subs) != 1 || subs[0].Path != "/lib/b.vtt" {
		t.Errorf("subs = %+v, want only b.vtt", subs)
	}
}

// A subtitle with no readable text still has to appear in the results, with
// zero runtime and no language, rather than being silently dropped.
func TestCollectKeepsAFileWithNoDetectableContent(t *testing.T) {
	m := vfs.NewMem("/lib/a.srt")

	subs, problems, err := Collect(context.Background(), m, "/lib", nil, false)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("problems = %v, want none for an empty-but-present file", problems)
	}
	if len(subs) != 1 {
		t.Fatalf("subs = %d, want 1", len(subs))
	}
	if subs[0].Runtime != 0 || subs[0].Lang != "" {
		t.Errorf("got Runtime=%v Lang=%q for empty contents, want zero values", subs[0].Runtime, subs[0].Lang)
	}
}

func TestExtOf(t *testing.T) {
	cases := map[string]string{
		"clip.srt":    ".srt",
		"clip.en.srt": ".srt",
		"noext":       "",
		".hidden":     ".hidden",
	}
	for in, want := range cases {
		if got := extOf(in); got != want {
			t.Errorf("extOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtSetDefaultsWhenEmpty(t *testing.T) {
	got := extSet(nil)
	for _, want := range DefaultExtensions {
		if !got[want] {
			t.Errorf("extSet(nil) missing default extension %q", want)
		}
	}
}

func TestExtSetNormalisesCaseAndDots(t *testing.T) {
	got := extSet([]string{".SRT", " vtt ", ""})
	if !got["srt"] || !got["vtt"] {
		t.Errorf("extSet = %v, want srt and vtt", got)
	}
	if len(got) != 2 {
		t.Errorf("extSet = %v, want exactly 2 entries", got)
	}
}

func TestRelDirs(t *testing.T) {
	cases := []struct {
		root, dir string
		want      []string
	}{
		{"/lib", "/lib", nil},
		{"/lib", "/lib/Show", []string{"Show"}},
		{"/lib", "/lib/Show/S01", []string{"Show", "S01"}},
	}
	for _, c := range cases {
		got := relDirs(c.root, c.dir)
		if len(got) != len(c.want) {
			t.Errorf("relDirs(%q, %q) = %v, want %v", c.root, c.dir, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("relDirs(%q, %q) = %v, want %v", c.root, c.dir, got, c.want)
			}
		}
	}
}

func TestSplitLangSuffix(t *testing.T) {
	cases := []struct {
		in       string
		wantBase string
		wantTag  string
		wantOK   bool
	}{
		{"clip_1080p.en", "clip_1080p", "en", true},
		{"clip_1080p.EN", "clip_1080p", "en", true},
		{"clip_1080p.eng", "clip_1080p", "eng", true},
		{"clip_1080p", "clip_1080p", "", false},
		{"clip_1080p.xyz", "clip_1080p.xyz", "", false},
	}
	for _, c := range cases {
		base, tag, ok := splitLangSuffix(c.in)
		if base != c.wantBase || tag != c.wantTag || ok != c.wantOK {
			t.Errorf("splitLangSuffix(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, base, tag, ok, c.wantBase, c.wantTag, c.wantOK)
		}
	}
}

func TestAlreadyAttachedMatchesAVideoByStem(t *testing.T) {
	m := vfs.NewMem("/lib/Show/Ep01.mkv", "/lib/Show/Ep01.srt")
	sub := Subtitle{Path: "/lib/Show/Ep01.srt", Stem: "Ep01"}

	got, err := AlreadyAttached(context.Background(), m, sub, DefaultVideoExtensions)
	if err != nil {
		t.Fatalf("AlreadyAttached: %v", err)
	}
	if !got {
		t.Error("AlreadyAttached = false, want true (a video shares the stem)")
	}
}

func TestAlreadyAttachedStripsALanguageSuffixBeforeComparing(t *testing.T) {
	m := vfs.NewMem("/lib/Show/Ep01.mkv", "/lib/Show/Ep01.en.srt")
	sub := Subtitle{Path: "/lib/Show/Ep01.en.srt", Stem: "Ep01.en"}

	got, err := AlreadyAttached(context.Background(), m, sub, DefaultVideoExtensions)
	if err != nil {
		t.Fatalf("AlreadyAttached: %v", err)
	}
	if !got {
		t.Error("AlreadyAttached = false, want true once the lang tag is stripped")
	}
}

func TestAlreadyAttachedFalseWhenNoVideoSharesTheStem(t *testing.T) {
	m := vfs.NewMem("/lib/Show/Other.mkv", "/lib/Show/Ep01.srt")
	sub := Subtitle{Path: "/lib/Show/Ep01.srt", Stem: "Ep01"}

	got, err := AlreadyAttached(context.Background(), m, sub, DefaultVideoExtensions)
	if err != nil {
		t.Fatalf("AlreadyAttached: %v", err)
	}
	if got {
		t.Error("AlreadyAttached = true, want false — no matching video")
	}
}
