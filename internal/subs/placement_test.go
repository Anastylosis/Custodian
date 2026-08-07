package subs

import (
	"testing"

	"github.com/Anastylosis/Custodian/internal/pathmap"
)

// BuildPlacement decides where a subtitle file is moved to. Every decision it
// makes is destructive in the sense that matters: the file leaves where the
// user put it. These tests pin the refusals as hard as the moves, because a
// wrong placement is worse than no placement.

func mapper(t *testing.T) *pathmap.Mapper {
	t.Helper()
	mp, err := pathmap.New([]pathmap.Rule{{StashPrefix: "/stash/video", LocalPrefix: "/mnt/video"}})
	if err != nil {
		t.Fatalf("pathmap.New: %v", err)
	}
	return mp
}

// match builds a Match whose best candidate points at scenePath.
func match(subPath, lang string, size int64, verdict Verdict, sceneID, scenePath string) Match {
	return Match{
		Subtitle: Subtitle{Path: subPath, Lang: lang, Size: size},
		Verdict:  verdict,
		Candidates: []Candidate{{
			Scene: SceneRef{ID: sceneID, Path: scenePath},
		}},
	}
}

func TestBuildPlacementNamesDestinationAfterTheVideo(t *testing.T) {
	m := match("/mnt/loose/whatever.srt", "en", 100, Confirmed, "42", "/stash/video/Show/Ep01.1080p.mkv")

	plan := BuildPlacement([]Match{m}, mapper(t), false)

	if len(plan.Moves) != 1 {
		t.Fatalf("moves = %d, want 1 (skipped: %+v)", len(plan.Moves), plan.Skipped)
	}
	got := plan.Moves[0]

	// <video-stem>.<lang>.<ext>. The language is what allows a second track on
	// the same scene: a bare <stem>.srt is filed by Stash as language "00", so
	// two undetected tracks would collide on one filename.
	if want := "/stash/video/Show/Ep01.1080p.en.srt"; got.DstStash != want {
		t.Errorf("DstStash = %q, want %q", got.DstStash, want)
	}
	if got.SceneID != "42" {
		t.Errorf("SceneID = %q, want 42", got.SceneID)
	}
}

// The destination comes from Stash and has to be translated before anything
// touches the disk. Getting this wrong writes outside the library.
func TestBuildPlacementTranslatesDestinationToALocalPath(t *testing.T) {
	m := match("/mnt/loose/x.srt", "en", 100, Confirmed, "1", "/stash/video/A/B.mkv")

	plan := BuildPlacement([]Match{m}, mapper(t), false)

	if want := "/mnt/video/A/B.en.srt"; plan.Moves[0].DstLocal != want {
		t.Errorf("DstLocal = %q, want %q", plan.Moves[0].DstLocal, want)
	}
	// The scan listed files by local path already, so the source needs no
	// translation.
	if plan.Moves[0].SrcLocal != "/mnt/loose/x.srt" {
		t.Errorf("SrcLocal = %q, should be the path as scanned", plan.Moves[0].SrcLocal)
	}
}

// An unmapped destination must still produce a usable plan rather than an
// empty path — falling back to the Stash path is visibly wrong in a dry run,
// where an empty string would silently become the filesystem root.
func TestBuildPlacementFallsBackWhenThePathIsNotMapped(t *testing.T) {
	m := match("/mnt/loose/x.srt", "en", 100, Confirmed, "1", "/elsewhere/A/B.mkv")

	plan := BuildPlacement([]Match{m}, mapper(t), false)

	if len(plan.Moves) != 1 {
		t.Fatalf("moves = %d, want 1", len(plan.Moves))
	}
	if plan.Moves[0].DstLocal != "/elsewhere/A/B.en.srt" {
		t.Errorf("DstLocal = %q, want the untranslated path", plan.Moves[0].DstLocal)
	}
}

// A wrong language tag is worse than none: Stash files the caption under it,
// and the user sees a track claiming to be a language it is not.
func TestBuildPlacementRefusesWhenLanguageIsUnknown(t *testing.T) {
	m := match("/mnt/loose/x.srt", "", 100, Confirmed, "1", "/stash/video/A/B.mkv")

	plan := BuildPlacement([]Match{m}, mapper(t), false)

	if len(plan.Moves) != 0 {
		t.Fatalf("moved a subtitle with no detected language: %+v", plan.Moves)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "language could not be detected" {
		t.Errorf("skipped = %+v, want one entry naming the language", plan.Skipped)
	}
}

// Ambiguous exists so the caller can decline to act. Treating it as a match
// would move a subtitle onto a video it may not belong to.
func TestBuildPlacementRefusesAmbiguousAndUnmatched(t *testing.T) {
	for _, v := range []Verdict{Ambiguous, Unmatched} {
		plan := BuildPlacement(
			[]Match{match("/mnt/loose/x.srt", "en", 100, v, "1", "/stash/video/A/B.mkv")},
			mapper(t), true /* even with likely included */)

		if len(plan.Moves) != 0 {
			t.Errorf("verdict %s produced a move", v)
		}
		if len(plan.Skipped) != 1 {
			t.Fatalf("verdict %s: skipped = %+v, want one entry", v, plan.Skipped)
		}
		if plan.Skipped[0].Reason == "" {
			t.Errorf("verdict %s skipped with no reason", v)
		}
	}
}

// Likely is opt-in. Defaulting it on would act on the weaker evidence without
// the caller asking.
func TestBuildPlacementLikelyIsOptIn(t *testing.T) {
	m := match("/mnt/loose/x.srt", "en", 100, Likely, "1", "/stash/video/A/B.mkv")

	if plan := BuildPlacement([]Match{m}, mapper(t), false); len(plan.Moves) != 0 {
		t.Error("Likely moved without includeLikely")
	}
	if plan := BuildPlacement([]Match{m}, mapper(t), true); len(plan.Moves) != 1 {
		t.Error("Likely did not move with includeLikely")
	}
}

func TestBuildPlacementSkipsWhenThereIsNoCandidate(t *testing.T) {
	m := Match{
		Subtitle: Subtitle{Path: "/mnt/loose/x.srt", Lang: "en"},
		Verdict:  Confirmed, // verdict says yes, but nothing to point at
	}

	plan := BuildPlacement([]Match{m}, mapper(t), false)

	if len(plan.Moves) != 0 {
		t.Fatal("moved a match with no candidate")
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "no match" {
		t.Errorf("skipped = %+v", plan.Skipped)
	}
}

// Duplicate downloads of one transcript are a real case. Both resolve to the
// same destination, and only one can have it.
func TestBuildPlacementKeepsTheFullerOfTwoDuplicates(t *testing.T) {
	small := match("/mnt/loose/small.srt", "en", 100, Confirmed, "1", "/stash/video/A/B.mkv")
	large := match("/mnt/loose/large.srt", "en", 900, Confirmed, "1", "/stash/video/A/B.mkv")

	// Whichever order they arrive in, the larger file must win.
	for _, tc := range []struct {
		name    string
		matches []Match
	}{
		{"smaller first", []Match{small, large}},
		{"larger first", []Match{large, small}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlacement(tc.matches, mapper(t), false)

			if len(plan.Moves) != 1 {
				t.Fatalf("moves = %d, want 1 — both claimed the same destination", len(plan.Moves))
			}
			if plan.Moves[0].SrcStash != "/mnt/loose/large.srt" {
				t.Errorf("kept %q, want the larger transcript", plan.Moves[0].SrcStash)
			}
			// The loser is reported, never deleted.
			if len(plan.Skipped) != 1 || plan.Skipped[0].SrcStash != "/mnt/loose/small.srt" {
				t.Errorf("skipped = %+v, want the smaller file reported", plan.Skipped)
			}
		})
	}
}

// Different languages for one video are not duplicates — that is the whole
// reason the language is stamped into the filename.
func TestBuildPlacementKeepsBothWhenLanguagesDiffer(t *testing.T) {
	en := match("/mnt/loose/a.srt", "en", 100, Confirmed, "1", "/stash/video/A/B.mkv")
	es := match("/mnt/loose/b.srt", "es", 100, Confirmed, "1", "/stash/video/A/B.mkv")

	plan := BuildPlacement([]Match{en, es}, mapper(t), false)

	if len(plan.Moves) != 2 {
		t.Fatalf("moves = %d, want 2 (skipped: %+v)", len(plan.Moves), plan.Skipped)
	}
	seen := map[string]bool{}
	for _, m := range plan.Moves {
		seen[m.DstStash] = true
	}
	for _, want := range []string{"/stash/video/A/B.en.srt", "/stash/video/A/B.es.srt"} {
		if !seen[want] {
			t.Errorf("missing destination %q", want)
		}
	}
}

// The source extension is preserved: a .vtt must not become a .srt, because
// the file's contents are still WebVTT.
func TestBuildPlacementPreservesTheSubtitleExtension(t *testing.T) {
	m := match("/mnt/loose/x.vtt", "en", 100, Confirmed, "1", "/stash/video/A/B.mkv")

	plan := BuildPlacement([]Match{m}, mapper(t), false)

	if want := "/stash/video/A/B.en.vtt"; plan.Moves[0].DstStash != want {
		t.Errorf("DstStash = %q, want %q", plan.Moves[0].DstStash, want)
	}
}

// PlannedMoves is the handover to the shared mover, which owns the
// never-overwrite guarantee. Dropping a field here would move a file to an
// empty path.
func TestPlannedMovesCarriesEveryPath(t *testing.T) {
	plan := BuildPlacement(
		[]Match{match("/mnt/loose/x.srt", "en", 100, Confirmed, "1", "/stash/video/A/B.mkv")},
		mapper(t), false)

	moves := plan.PlannedMoves()
	if len(moves) != 1 {
		t.Fatalf("PlannedMoves = %d, want 1", len(moves))
	}
	got := moves[0]
	if got.SrcStash == "" || got.DstStash == "" || got.SrcLocal == "" || got.DstLocal == "" {
		t.Errorf("PlannedMove has an empty path: %+v", got)
	}
	if got.DstLocal != plan.Moves[0].DstLocal {
		t.Errorf("DstLocal = %q, want %q", got.DstLocal, plan.Moves[0].DstLocal)
	}
}

func TestTrimExtension(t *testing.T) {
	for in, want := range map[string]string{
		"Ep01.1080p.mkv": "Ep01.1080p",
		"noext":          "noext",
		// A leading dot is the whole name, not an extension.
		".hidden": ".hidden",
	} {
		if got := trimExtension(in); got != want {
			t.Errorf("trimExtension(%q) = %q, want %q", in, got, want)
		}
	}
}
