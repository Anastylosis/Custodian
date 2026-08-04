package subs

import (
	"strings"

	"github.com/Anastylosis/StashJanitor/internal/pathmap"
	"github.com/Anastylosis/StashJanitor/internal/sidecar"
)

// Placement is one subtitle about to be moved next to its matched video.
type Placement struct {
	SrcStash string
	DstStash string
	SrcLocal string
	DstLocal string
	SceneID  string
	Verdict  Verdict
	Lang     string
}

// SkippedPlacement is a subtitle deliberately not placed.
type SkippedPlacement struct {
	SrcStash string
	Reason   string
}

// Plan is the full set of placements plus what was left alone.
type Plan struct {
	Moves   []Placement
	Skipped []SkippedPlacement
}

// PlannedMoves converts placements into the form sidecar.Mover.Apply consumes,
// so both features share one move implementation — including its never-
// overwrite guarantee and cross-filesystem fallback.
func (p Plan) PlannedMoves() []sidecar.PlannedMove {
	out := make([]sidecar.PlannedMove, 0, len(p.Moves))
	for _, m := range p.Moves {
		out = append(out, sidecar.PlannedMove{
			SrcStash: m.SrcStash, DstStash: m.DstStash,
			SrcLocal: m.SrcLocal, DstLocal: m.DstLocal,
		})
	}
	return out
}

// BuildPlacement turns matches into a move plan.
//
// The destination is always <video-stem>.<lang>.<ext>. Stamping the language
// is what allows several tracks on one scene: a bare <stem>.srt is filed by
// Stash as language "00", and a second unknown-language track would collide on
// the same filename. When the language could not be detected the subtitle is
// left alone rather than guessed at, since a wrong tag is worse than none.
//
// Two subtitles resolving to the same destination is a real case — duplicate
// downloads of one transcript. The larger file wins, on the grounds that a
// fuller transcript is the more useful one; the other is reported, not deleted.
func BuildPlacement(matches []Match, mp *pathmap.Mapper, includeLikely bool) Plan {
	var plan Plan
	claimed := map[string]int{} // dstStash -> index into plan.Moves

	for _, m := range matches {
		best := m.Best()
		switch {
		case best == nil:
			plan.Skipped = append(plan.Skipped, SkippedPlacement{m.Subtitle.Path, "no match"})
			continue
		case m.Verdict == Confirmed:
		case m.Verdict == Likely && includeLikely:
		default:
			plan.Skipped = append(plan.Skipped, SkippedPlacement{
				m.Subtitle.Path, strings.ToLower(string(m.Verdict))})
			continue
		}
		if m.Subtitle.Lang == "" {
			plan.Skipped = append(plan.Skipped, SkippedPlacement{
				m.Subtitle.Path, "language could not be detected"})
			continue
		}

		ext := extOf(m.Subtitle.Path)
		videoStem := trimExtension(pathmap.Base(best.Scene.Path))
		dstDir := pathmap.Dir(best.Scene.Path)
		dstStash := pathmap.Join(dstDir, videoStem+"."+m.Subtitle.Lang+ext)

		if prev, taken := claimed[dstStash]; taken {
			// Duplicate transcripts of the same video: keep the fuller one.
			if m.Subtitle.Size > sizeOf(matches, plan.Moves[prev].SrcStash) {
				plan.Skipped = append(plan.Skipped, SkippedPlacement{
					plan.Moves[prev].SrcStash, "duplicate of a fuller transcript"})
				plan.Moves[prev] = newPlacement(m, best, dstStash, mp)
			} else {
				plan.Skipped = append(plan.Skipped, SkippedPlacement{
					m.Subtitle.Path, "duplicate of a fuller transcript"})
			}
			continue
		}
		claimed[dstStash] = len(plan.Moves)
		plan.Moves = append(plan.Moves, newPlacement(m, best, dstStash, mp))
	}
	return plan
}

func newPlacement(m Match, best *Candidate, dstStash string, mp *pathmap.Mapper) Placement {
	p := Placement{
		SrcStash: m.Subtitle.Path,
		DstStash: dstStash,
		SceneID:  best.Scene.ID,
		Verdict:  m.Verdict,
		Lang:     m.Subtitle.Lang,
	}
	// The scan listed files by local path already, so SrcStash IS local here;
	// the destination comes from Stash and must be translated.
	p.SrcLocal = m.Subtitle.Path
	if l, err := mp.ToLocal(dstStash); err == nil {
		p.DstLocal = l
	} else {
		p.DstLocal = dstStash
	}
	return p
}

func sizeOf(matches []Match, path string) int64 {
	for _, m := range matches {
		if m.Subtitle.Path == path {
			return m.Subtitle.Size
		}
	}
	return 0
}

func trimExtension(name string) string {
	if i := strings.LastIndex(name, "."); i > 0 {
		return name[:i]
	}
	return name
}
