// Package keeper chooses the best copy in a duplicate-candidate group
// (REBUILD.md §4.3). Pure: no Stash calls, no writes — it scores the
// scenes ledger.Groups already assembled and returns a plan.
package keeper

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// Step is one rule keeper.Choose evaluated, so a report can quote why a
// scene won or tied: "tier: 1080p vs 720p → 9904 wins",
// "bitrate: 14.5 vs 14.4 Mbps → tie under 10%".
type Step = ledger.Step

// Choice is the keeper Choose picked for one group, the scenes it beat, and
// the rule trail that decided it.
type Choice struct {
	Keeper string
	Losers []string
	Steps  []Step
}

// Rules configures how Choose ranks scenes within a group. Zero value is
// not useful on its own for Filed — see DefaultRules.
type Rules struct {
	// BitrateSignificance is the fraction of the higher bitrate below which
	// two files' bitrates count as a tie.
	BitrateSignificance float64
	// SizeSignificance is the fraction of the larger size below which two
	// files' sizes count as a tie.
	SizeSignificance float64
	// CodecPreference ranks video codecs best first. A codec absent from
	// the list ranks below every listed codec and ties against other
	// absent codecs.
	CodecPreference []string
	// StagingPrefixes marks a path as not-yet-filed when it starts with
	// any of these prefixes. Used to build the default Filed when Filed is
	// nil.
	StagingPrefixes []string
	// Filed reports whether path is already filed (not staging) — the
	// location tiebreak. Nil falls back to "does not start with a
	// StagingPrefixes prefix".
	Filed func(path string) bool

	// Eligible restricts Choose's keeper pool to these scene ids — the
	// completeness rule for a `partial` group (REBUILD.md §4.3):
	// completeness sits above the quality chain, so only copies that
	// matched their canonical duration compete on tier/bitrate/codec/size.
	// Every group member not in Eligible is still a loser. Nil or empty
	// means every scene is eligible, Choose's behaviour for `certain`
	// groups.
	Eligible []string
}

// DefaultRules is REBUILD.md §4.3's starting point.
func DefaultRules() Rules {
	staging := []string{"/data/tmp", "/data/nosort"}
	return Rules{
		BitrateSignificance: 0.10,
		SizeSignificance:    0.10,
		CodecPreference:     []string{"av1", "hevc", "h264", "vp9"},
		StagingPrefixes:     staging,
		Filed:               filedFunc(staging),
	}
}

func filedFunc(stagingPrefixes []string) func(string) bool {
	return func(path string) bool { return !hasStagingPrefix(path, stagingPrefixes) }
}

func hasStagingPrefix(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Choose picks the keeper in g by folding the rule chain — tier, bitrate,
// codec, size, filed location, older scene id (final, always decides) —
// pairwise over every scene. Every scene but the keeper is a loser.
//
// Completeness is not a rule among those: for a `certain` group, whose
// durations already agree within tolerance, every member is a candidate on
// quality alone (r.Eligible nil). For a `partial` group, the caller sets
// r.Eligible to the scenes that matched their canonical duration (§4.3):
// only those compete on the chain above, a leading "completeness" Step
// records which ones, and every scene outside Eligible is still a loser.
func Choose(g ledger.Group, files map[string]ledger.PrimaryFile, r Rules) Choice {
	ids := g.SceneIDs
	if len(ids) == 0 {
		return Choice{}
	}
	if r.Filed == nil {
		prefixes := r.StagingPrefixes
		if prefixes == nil {
			prefixes = DefaultRules().StagingPrefixes
		}
		r.Filed = filedFunc(prefixes)
	}

	pool := ids
	var completeness *Step
	if len(r.Eligible) > 0 {
		if restricted := filterEligible(ids, r.Eligible); len(restricted) > 0 {
			pool = restricted
		}
		completeness = &Step{
			Rule:    "completeness",
			Outcome: fmt.Sprintf("eligible: %s", strings.Join(pool, ", ")),
			Detail:  fmt.Sprintf("%d of %d scenes match canonical duration", len(pool), len(ids)),
		}
	}

	best := pool[0]
	var steps []Step
	for _, challenger := range pool[1:] {
		winner, cmpSteps := compare(best, challenger, files, r)
		steps = cmpSteps
		if winner == challenger {
			best = challenger
		}
	}
	if completeness != nil {
		steps = append([]Step{*completeness}, steps...)
	}

	losers := make([]string, 0, len(ids)-1)
	for _, id := range ids {
		if id != best {
			losers = append(losers, id)
		}
	}
	return Choice{Keeper: best, Losers: losers, Steps: steps}
}

// filterEligible returns the subset of ids that appears in eligible,
// preserving ids' order.
func filterEligible(ids, eligible []string) []string {
	set := make(map[string]bool, len(eligible))
	for _, id := range eligible {
		set[id] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if set[id] {
			out = append(out, id)
		}
	}
	return out
}

// compare runs the rule chain between aID and bID and returns the winning
// scene id plus every rule it evaluated, in order — leading ties, then the
// rule that decided.
func compare(aID, bID string, files map[string]ledger.PrimaryFile, r Rules) (string, []Step) {
	a, b := files[aID], files[bID]
	var steps []Step

	tierCmp, tierDetail, tierTie := compareTier(a, b)
	if !tierTie {
		winner := pick(aID, bID, tierCmp)
		return winner, append(steps, Step{Rule: "tier", Detail: tierDetail, Outcome: winner + " wins"})
	}
	steps = append(steps, Step{Rule: "tier", Detail: tierDetail, Outcome: "tie"})

	brCmp, brDetail := significance(a.BitRate, b.BitRate, r.BitrateSignificance, formatMbps)
	if brCmp != 0 {
		winner := pick(aID, bID, brCmp)
		return winner, append(steps, Step{Rule: "bitrate", Detail: brDetail, Outcome: winner + " wins"})
	}
	steps = append(steps, Step{Rule: "bitrate", Detail: brDetail, Outcome: "tie under " + pct(r.BitrateSignificance)})

	codecCmp, codecDetail := compareCodec(a.VideoCodec, b.VideoCodec, r.CodecPreference)
	if codecCmp != 0 {
		winner := pick(aID, bID, codecCmp)
		return winner, append(steps, Step{Rule: "codec", Detail: codecDetail, Outcome: winner + " wins"})
	}
	steps = append(steps, Step{Rule: "codec", Detail: codecDetail, Outcome: "tie"})

	sizeCmp, sizeDetail := significance(a.Size, b.Size, r.SizeSignificance, formatBytes)
	if sizeCmp != 0 {
		winner := pick(aID, bID, sizeCmp)
		return winner, append(steps, Step{Rule: "size", Detail: sizeDetail, Outcome: winner + " wins"})
	}
	steps = append(steps, Step{Rule: "size", Detail: sizeDetail, Outcome: "tie under " + pct(r.SizeSignificance)})

	locWinner, locDetail, locTie := compareFiled(aID, bID, a.Path, b.Path, r.Filed)
	if !locTie {
		return locWinner, append(steps, Step{Rule: "location", Detail: locDetail, Outcome: locWinner + " wins"})
	}
	steps = append(steps, Step{Rule: "location", Detail: locDetail, Outcome: "tie"})

	winner, detail := olderSceneID(aID, bID)
	return winner, append(steps, Step{Rule: "scene_id", Detail: detail, Outcome: winner + " wins (final)"})
}

// compareTier judges resolution tier via stash.TierOf, so 1080-tier beats
// 720-tier but 1920x1080 vs 1920x1088 ties. cmp is 1 if a wins, -1 if b
// wins, 0 for a tie.
func compareTier(a, b ledger.PrimaryFile) (cmp int, detail string, tie bool) {
	ta, tb := stash.TierOf(a.Width, a.Height), stash.TierOf(b.Width, b.Height)
	detail = fmt.Sprintf("%s vs %s", ta, tb)
	if ta == tb {
		return 0, detail, true
	}
	if ta > tb {
		return 1, detail, false
	}
	return -1, detail, false
}

// significance compares a and b, formatted by detail, and reports a tie
// when their absolute difference is under threshold of the higher value.
// cmp is 1 if a wins, -1 if b wins, 0 for a tie.
func significance(a, b int64, threshold float64, format func(a, b int64) string) (cmp int, detail string) {
	detail = format(a, b)
	higher := a
	if b > higher {
		higher = b
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	if higher <= 0 || float64(diff) < threshold*float64(higher) {
		return 0, detail
	}
	if a > b {
		return 1, detail
	}
	return -1, detail
}

func formatMbps(a, b int64) string {
	return fmt.Sprintf("%.1f vs %.1f Mbps", float64(a)/1_000_000, float64(b)/1_000_000)
}

func formatBytes(a, b int64) string {
	return fmt.Sprintf("%s vs %s", confirm.HumanBytes(a), confirm.HumanBytes(b))
}

// compareCodec ranks by preference, lower rank = better; a codec absent
// from preference ranks below every listed one. Returns 1 if a wins, -1 if
// b wins, 0 tie.
func compareCodec(a, b string, preference []string) (cmp int, detail string) {
	rank := buildCodecRank(preference)
	ra, aOK := rank[normalizeCodec(a)]
	if !aOK {
		ra = len(preference)
	}
	rb, bOK := rank[normalizeCodec(b)]
	if !bOK {
		rb = len(preference)
	}
	detail = fmt.Sprintf("%s vs %s", codecOrUnknown(a), codecOrUnknown(b))
	switch {
	case ra == rb:
		return 0, detail
	case ra < rb:
		return 1, detail
	default:
		return -1, detail
	}
}

func codecOrUnknown(c string) string {
	if c == "" {
		return "unknown"
	}
	return c
}

// buildCodecRank maps each normalized codec in preference to its rank
// (0 = best); any codec not listed ranks len(preference).
func buildCodecRank(preference []string) map[string]int {
	out := make(map[string]int, len(preference))
	for i, c := range preference {
		out[normalizeCodec(c)] = i
	}
	return out
}

// normalizeCodec mirrors the old internal/decide/scorer.go's normalizeCodec:
// lower-cases, strips '.'/'-', and maps Stash's "h265" alias to "hevc".
func normalizeCodec(c string) string {
	c = strings.ToLower(c)
	c = strings.ReplaceAll(c, ".", "")
	c = strings.ReplaceAll(c, "-", "")
	if c == "h265" {
		return "hevc"
	}
	return c
}

// compareFiled prefers a filed path (organize's already_correct) over an
// unfiled one, whatever r.Filed considers "filed" to mean.
func compareFiled(aID, bID, aPath, bPath string, filed func(string) bool) (winner, detail string, tie bool) {
	fa, fb := filed(aPath), filed(bPath)
	switch {
	case fa == fb && fa:
		return "", "both filed", true
	case fa == fb:
		return "", "both not filed", true
	case fa:
		return aID, "filed vs not filed", false
	default:
		return bID, "not filed vs filed", false
	}
}

// olderSceneID picks the numerically lower scene id — final, always
// decides, so a `certain` group never falls into review over a tie.
func olderSceneID(a, b string) (winner, detail string) {
	detail = fmt.Sprintf("%s vs %s", a, b)
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		if ai <= bi {
			return a, detail
		}
		return b, detail
	}
	if a <= b {
		return a, detail
	}
	return b, detail
}

func pct(f float64) string {
	return strconv.FormatFloat(f*100, 'g', -1, 64) + "%"
}

// pick returns aID if cmp is positive (a wins), else bID.
func pick(aID, bID string, cmp int) string {
	if cmp > 0 {
		return aID
	}
	return bID
}
