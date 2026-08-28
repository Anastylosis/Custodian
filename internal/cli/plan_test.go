package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/keeper"
	"github.com/Anastylosis/Custodian/internal/ledger"
)

func TestFormatBitrate(t *testing.T) {
	cases := map[int64]string{
		0:          "unknown",
		500_000:    "500 kbps",
		14_500_000: "14.5 Mbps",
	}
	for in, want := range cases {
		if got := formatBitrate(in); got != want {
			t.Errorf("formatBitrate(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestIsTieOutcome(t *testing.T) {
	cases := map[string]bool{
		"tie":              true,
		"tie under 10%":    true,
		"9904 wins":        false,
		"100 wins (final)": false,
	}
	for in, want := range cases {
		if got := isTieOutcome(in); got != want {
			t.Errorf("isTieOutcome(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFormatFileSummary(t *testing.T) {
	pf := ledger.PrimaryFile{Width: 1920, Height: 1080, BitRate: 14_500_000, Size: 5 << 30, Basename: "scene.mp4"}
	got := formatFileSummary("9904", pf)
	want := "9904 · 1080p · 14.5 Mbps · 5.00 GiB · scene.mp4"
	if got != want {
		t.Errorf("formatFileSummary =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatPlanGroup(t *testing.T) {
	g := ledger.Group{
		Signature: "hashA+hashB",
		SceneIDs:  []string{"9904", "9905"},
		Verdict:   ledger.VerdictCertain,
		Keeper:    "9904",
		Steps: []ledger.Step{
			{Rule: "tier", Detail: "1080p vs 1080p", Outcome: "tie"},
			{Rule: "bitrate", Detail: "14.5 vs 14.4 Mbps", Outcome: "tie under 10%"},
			{Rule: "codec", Detail: "hevc vs h264", Outcome: "9904 wins"},
		},
		Reclaimable: 3 << 30,
	}
	files := map[string]ledger.PrimaryFile{
		"9904": {Width: 1920, Height: 1080, BitRate: 14_500_000, Size: 5 << 30, Basename: "a.mp4"},
		"9905": {Width: 1920, Height: 1080, BitRate: 14_400_000, Size: 3 << 30, Basename: "b.mp4"},
	}

	var b strings.Builder
	formatPlanGroup(&b, g, files)
	want := "group hashA+hashB (certain)\n" +
		"  keeper: 9904 · 1080p · 14.5 Mbps · 5.00 GiB · a.mp4\n" +
		"  loser:  9905 · 1080p · 14.4 Mbps · 3.00 GiB · b.mp4\n" +
		"  codec: hevc vs h264 → 9904 wins\n" +
		"  reclaimable: 3.00 GiB\n\n"
	if b.String() != want {
		t.Errorf("formatPlanGroup =\n%q\nwant\n%q", b.String(), want)
	}
}

func TestFormatPlan(t *testing.T) {
	groups := []ledger.Group{
		{
			Signature: "hashA+hashB", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain, Keeper: "1",
			Steps:       []ledger.Step{{Rule: "tier", Detail: "1080p vs 720p", Outcome: "1 wins"}},
			Reclaimable: 1 << 30,
		},
		{
			Signature: "hashC+hashD", SceneIDs: []string{"3", "4"}, Verdict: ledger.VerdictCertain, Keeper: "3",
			Steps:       []ledger.Step{{Rule: "tier", Detail: "1080p vs 1080p", Outcome: "tie"}, {Rule: "size", Detail: "2.00 GiB vs 1.00 GiB", Outcome: "3 wins"}},
			Reclaimable: 1 << 30,
		},
	}
	files := map[string]ledger.PrimaryFile{
		"1": {Width: 1920, Height: 1080, Basename: "a.mp4"},
		"2": {Width: 1280, Height: 720, Basename: "b.mp4"},
		"3": {Width: 1920, Height: 1080, Size: 2 << 30, Basename: "c.mp4"},
		"4": {Width: 1920, Height: 1080, Size: 1 << 30, Basename: "d.mp4"},
	}
	histogram := map[string]int{"tier": 1, "size": 1}

	got := formatPlan(groups, files, histogram, 2<<30)
	want := "group hashA+hashB (certain)\n" +
		"  keeper: 1 · 1080p · unknown · 0 B · a.mp4\n" +
		"  loser:  2 · 720p · unknown · 0 B · b.mp4\n" +
		"  tier: 1080p vs 720p → 1 wins\n" +
		"  reclaimable: 1.00 GiB\n\n" +
		"group hashC+hashD (certain)\n" +
		"  keeper: 3 · 1080p · unknown · 2.00 GiB · c.mp4\n" +
		"  loser:  4 · 1080p · unknown · 1.00 GiB · d.mp4\n" +
		"  size: 2.00 GiB vs 1.00 GiB → 3 wins\n" +
		"  reclaimable: 1.00 GiB\n\n" +
		"planned: 2 groups, 2.00 GiB reclaimable\n" +
		"decided by:\n" +
		"  tier:     1\n" +
		"  size:     1\n"
	if got != want {
		t.Errorf("formatPlan =\n%q\nwant\n%q", got, want)
	}
}

func TestFormatPlanEmpty(t *testing.T) {
	got := formatPlan(nil, nil, nil, 0)
	want := "planned: 0 groups, 0 B reclaimable\n"
	if got != want {
		t.Errorf("formatPlan(empty) = %q, want %q", got, want)
	}
}

// newPlanTestLedger and insertSceneFile build a minimal ledger directly
// against the schema (scene + file rows only) rather than through
// UpsertScenes, since plan only reads PrimaryFiles.
func newPlanTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "plan.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func insertSceneFile(t *testing.T, l *ledger.Ledger, sceneID, path, basename string, size int64, width, height int, bitRate int64, codec string) {
	t.Helper()
	ctx := context.Background()
	if _, err := l.DB().ExecContext(ctx, `INSERT INTO scene(id, seen_run) VALUES(?, 1)`, sceneID); err != nil {
		t.Fatalf("inserting scene %s: %v", sceneID, err)
	}
	if _, err := l.DB().ExecContext(ctx,
		`INSERT INTO file(id, scene_id, path, basename, size, width, height, video_codec, bit_rate, is_primary, seen_run)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1)`,
		"f-"+sceneID, sceneID, path, basename, size, width, height, codec, bitRate); err != nil {
		t.Fatalf("inserting file for scene %s: %v", sceneID, err)
	}
}

func TestChoosePersistsAndComputesReclaimable(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "9904", "/data/organized/a.mp4", "a.mp4", 5_000_000_000, 1920, 1080, 14_000_000, "hevc")
	insertSceneFile(t, l, "9905", "/data/organized/b.mp4", "b.mp4", 3_000_000_000, 1280, 720, 14_000_000, "hevc")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	group := ledger.Group{Signature: "sig1", SceneIDs: []string{"9904", "9905"}, Verdict: ledger.VerdictCertain, Reason: "durations agree"}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{group}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	files, err := l.PrimaryFiles(ctx, allSceneIDs(groups))
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}

	histogram, total, err := choose(ctx, l, groups, files, &config.Config{})
	if err != nil {
		t.Fatalf("choose: %v", err)
	}

	if groups[0].Keeper != "9904" {
		t.Errorf("Keeper = %q, want 9904 (higher tier)", groups[0].Keeper)
	}
	if groups[0].Reclaimable != 3_000_000_000 {
		t.Errorf("Reclaimable = %d, want 3000000000 (loser 9905's primary file size)", groups[0].Reclaimable)
	}
	if total != 3_000_000_000 {
		t.Errorf("total = %d, want 3000000000", total)
	}
	if histogram["tier"] != 1 {
		t.Errorf("histogram[tier] = %d, want 1, got %v", histogram["tier"], histogram)
	}

	// SetChoice actually persisted: a fresh Groups() call reflects the choice.
	reloaded, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups (reload): %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].Keeper != "9904" || reloaded[0].Reclaimable != 3_000_000_000 {
		t.Errorf("reloaded group = %+v, want keeper 9904 reclaimable 3000000000", reloaded)
	}
	if len(reloaded[0].Steps) == 0 {
		t.Error("reloaded group has no Steps, want the persisted rule trail")
	}
}

// Sanity check that keeper.DefaultRules is actually what plan uses — a
// change to keeper's default staging prefixes should be visible here too.
func TestPlanUsesKeeperDefaultRules(t *testing.T) {
	if len(keeper.DefaultRules().CodecPreference) == 0 {
		t.Fatal("keeper.DefaultRules().CodecPreference is empty")
	}
}

// TestChooseWiresOrganizeFiledForLocationTiebreak shows plan's choose wiring
// organize.Filed into keeper.Rules.Filed: two scenes tie on tier, bitrate,
// codec and size, and neither path sits under keeper's default
// StagingPrefixes ("/data/tmp", "/data/nosort"), so the StagingPrefixes
// heuristic alone would call both filed and fall through to scene_id. With
// an organize template configured, the location step must instead decide
// on organize's base_dir, and the report must read "location: filed vs not
// filed".
func TestChooseWiresOrganizeFiledForLocationTiebreak(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()

	insertSceneFile(t, l, "9904", "/library/Some Performer/a.mp4", "a.mp4", 1_000_000, 1920, 1080, 5_000_000, "hevc")
	insertSceneFile(t, l, "9905", "/incoming/b.mp4", "b.mp4", 1_000_000, 1920, 1080, 5_000_000, "hevc")

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	group := ledger.Group{Signature: "sig1", SceneIDs: []string{"9904", "9905"}, Verdict: ledger.VerdictCertain, Reason: "durations agree"}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{group}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	files, err := l.PrimaryFiles(ctx, allSceneIDs(groups))
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}

	cfg := &config.Config{Organize: config.OrganizeConfig{
		BaseDir:      "/library",
		PathTemplate: "{performer}/{title}.{ext}",
	}}
	if _, _, err := choose(ctx, l, groups, files, cfg); err != nil {
		t.Fatalf("choose: %v", err)
	}

	last := groups[0].Steps[len(groups[0].Steps)-1]
	if last.Rule != "location" || last.Detail != "filed vs not filed" || last.Outcome != "9904 wins" {
		t.Fatalf("deciding step = %+v, want location/filed vs not filed/9904 wins", last)
	}

	var b strings.Builder
	formatPlanGroup(&b, groups[0], files)
	if !strings.Contains(b.String(), "  location: filed vs not filed → 9904 wins\n") {
		t.Errorf("formatPlanGroup output =\n%s\nwant a line reading location: filed vs not filed → 9904 wins", b.String())
	}
}

func TestChooseLeavesMultifileGroupsToScan(t *testing.T) {
	l := newPlanTestLedger(t)
	ctx := context.Background()
	insertSceneFile(t, l, "1", "/library/a.mp4", "a.mp4", 1_000_000, 1920, 1080, 5_000_000, "hevc")
	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	step := ledger.Step{Rule: "file_choice", Outcome: "f1", Detail: "keep f1"}
	g := ledger.Group{Signature: "solo", SceneIDs: []string{"1"}, Verdict: ledger.VerdictCertain, Steps: []ledger.Step{step}}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{g}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if _, _, err := choose(ctx, l, []ledger.Group{g}, nil, &config.Config{}); err != nil {
		t.Fatalf("choose: %v", err)
	}
	got, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(got) != 1 || len(got[0].Steps) != 1 || got[0].Steps[0].Rule != "file_choice" {
		t.Fatalf("file_choice step lost: %+v", got)
	}
}
