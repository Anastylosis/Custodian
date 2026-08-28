package reclaim

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/vfs"
	stash "github.com/Anastylosis/stash-go"
)

func TestReportRendersEveryLoserDetail(t *testing.T) {
	rep := Report{
		QuarantineDir: "/data/_quarantine",
		Tag:           "_custodian_quarantine",
		Backup: BackupStep{
			Taken: true, File: "/b/local.sqlite.85", Manifest: "/b/local.sqlite.85.manifest.json",
			Bytes: 4096,
		},
		Groups: []GroupReport{{
			Signature: "0123456789abcdef",
			Keeper:    "10",
			Union:     &UnionStep{Scene: "10", Fields: []string{"title"}, Conflicts: []string{"https://sb: kept a, dropped b"}},
			Losers: []LoserReport{{
				SceneID: "11", From: "/data/x/a.mp4", To: "/data/_quarantine/a.mp4", Bytes: 1024,
				Sidecars: []SidecarMove{
					{From: "/data/x/a.en.srt", To: "/data/_quarantine/a.en.srt", Done: true},
					{From: "/data/x/a.es.srt", To: "/data/_quarantine/a.es.srt", Note: "destination already exists"},
					{From: "/data/x/a.fr.srt", To: "/data/_quarantine/a.fr.srt"},
				},
				Notes: []string{"could not inspect sidecars: nope"},
			}},
			Notes: []string{"keeper 10 holds 2 files"},
			Errs:  []string{"moveFiles failed"},
			Bytes: 1024,
		}},
	}

	out := rep.String()
	for _, want := range []string{
		"COMMIT",
		"/b/local.sqlite.85.manifest.json",
		"group 0123456789ab… keeper 10",
		"union:   sceneUpdate scene 10: title",
		"conflict: https://sb: kept a, dropped b",
		"tag:     added",
		"a.en.srt",
		"(destination already exists)",
		"(planned)",
		"note:    could not inspect sidecars: nope",
		"note: keeper 10 holds 2 files",
		"ERROR: moveFiles failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}

	if _, _, failed, _ := rep.Counts(); failed != 1 {
		t.Errorf("a group with errors did not count as failed")
	}
	if !strings.Contains(rep.Summary(), "failed 1") {
		t.Errorf("Summary() = %q", rep.Summary())
	}
}

func TestReportShowsAnUntakenBackupOnACommit(t *testing.T) {
	rep := Report{Backup: BackupStep{Dir: "/b"}}
	if !strings.Contains(rep.String(), "backup:     not taken") {
		t.Errorf("report:\n%s", rep.String())
	}
}

func TestShortSignatureLeavesShortOnesAlone(t *testing.T) {
	if got := shortSignature("abc"); got != "abc" {
		t.Errorf("shortSignature(abc) = %q", got)
	}
}

func TestSkippedSidecarIsReportedNotFailed(t *testing.T) {
	_, c, l := twoSceneGroup(t)
	// The destination already holds a file of that name, so the move is
	// skipped rather than clobbering a subtitle.
	mem := vfs.NewMem("/data/tmp/worse.mp4", "/data/tmp/worse.en.srt", "/data/_quarantine/worse.en.srt")

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir(), Sidecars: memMover(t, mem)})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	side := rep.Groups[0].Losers[0].Sidecars
	if len(side) != 1 || side[0].Done || !strings.Contains(side[0].Note, "already exists") {
		t.Fatalf("sidecars = %+v", side)
	}

	entries, err := l.Mutations(t.Context(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	for _, m := range entries {
		if m.Kind == "sidecar" && (m.OK == nil || *m.OK) {
			t.Errorf("a skipped sidecar was logged as done: %+v", m)
		}
	}
}

func TestFailedSidecarIsReported(t *testing.T) {
	_, c, l := twoSceneGroup(t)
	mem := vfs.NewMem("/data/tmp/worse.mp4", "/data/tmp/worse.en.srt")
	mem.FailRename["/data/tmp/worse.en.srt"] = errors.New("read-only filesystem")

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir(), Sidecars: memMover(t, mem)})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	side := rep.Groups[0].Losers[0].Sidecars
	if len(side) != 1 || !strings.Contains(side[0].Note, "read-only") {
		t.Fatalf("sidecars = %+v", side)
	}
}

func TestFindSceneErrorSkipsTheGroup(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	f.failOn["findScene"] = "database is locked"

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !rep.Groups[0].Skipped || !strings.Contains(rep.Groups[0].Reason, "database is locked") {
		t.Errorf("group = %+v", rep.Groups[0])
	}
}

func TestEnsureTagFailureStopsTheGroup(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	f.failOn["tagCreate"] = "tag name is taken"

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Groups[0].Errs) == 0 || !strings.Contains(rep.Groups[0].Errs[0], "ensuring tag") {
		t.Fatalf("errs = %v", rep.Groups[0].Errs)
	}
	if slices.Contains(f.writes(), "moveFiles") {
		t.Error("a file was moved after the tag failed")
	}
	// The tag attempt is on record as a failure, and nothing after it ran.
	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup", "union", "tag"}) {
		t.Errorf("mutation log = %v", kinds)
	}
}

func TestBackupDirectoryThatCannotBeCreatedFailsClosed(t *testing.T) {
	_, c, l := twoSceneGroup(t)
	// A regular file where the directory should be.
	dir := t.TempDir() + "/not-a-dir"
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if _, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: dir}); err == nil {
		t.Error("Apply proceeded with an unusable backup directory")
	}
}

func TestOptionsDefaults(t *testing.T) {
	got := Options{}.withDefaults()
	if got.QuarantineDir != DefaultQuarantineDir || got.Tag != DefaultTag || got.BackupDir == "" {
		t.Errorf("withDefaults() = %+v", got)
	}
	p := PruneOptions{}.withDefaults()
	if p.Age != DefaultAge || p.Tag != DefaultTag || p.BackupDir == "" {
		t.Errorf("PruneOptions.withDefaults() = %+v", p)
	}
}

func TestUnionFieldsNamesEverySetField(t *testing.T) {
	s, i, b := "x", 1, true
	got := unionFields(stash.SceneUpdate{
		Title: &s, Code: &s, Date: &s, Details: &s, Director: &s, StudioID: &s,
		Rating100: &i, Organized: &b,
		URLs: []string{"u"}, TagIDs: []string{"t"}, PerformerIDs: []string{"p"},
		GalleryIDs: []string{"g"}, StashIDs: []stash.StashID{{Endpoint: "e", ID: "i"}},
	})
	if len(got) != 13 {
		t.Errorf("unionFields() = %v, want all 13", got)
	}
	if len(unionFields(stash.SceneUpdate{})) != 0 {
		t.Error("an empty update named fields")
	}
}

func TestApplyFailsWhenTheLedgerIsClosed(t *testing.T) {
	_, c, l := twoSceneGroup(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Apply(t.Context(), c, l, Options{}); err == nil {
		t.Error("Apply on a closed ledger should fail")
	}
	if _, err := Prune(t.Context(), c, l, PruneOptions{}); err == nil {
		t.Error("Prune on a closed ledger should fail")
	}
}

func TestPruneGroupWithNoKeeperStoredIsSkipped(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/tmp/worse.mp4", 2<<30),
	}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{{
		Signature: "unplanned", SceneIDs: []string{"10", "11"}, Verdict: ledger.VerdictCertain,
	}}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	// A group `plan` never touched has no keeper: fetch says so rather than
	// picking one.
	a := &applier{client: c, l: l, opts: Options{}.withDefaults()}
	_, reason, err := a.fetch(ctx, ledger.Group{Signature: "unplanned"}, nil)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(reason, "no keeper stored") {
		t.Errorf("reason = %q", reason)
	}
}

func TestFetchSkipsAGroupWhoseLosersAllVanished(t *testing.T) {
	scenes := []stash.Scene{scene("10", "/data/keep/best.mp4", 4<<30)}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !rep.Groups[0].Skipped || !strings.Contains(rep.Groups[0].Reason, "no losers left") {
		t.Errorf("group = %+v", rep.Groups[0])
	}
}
