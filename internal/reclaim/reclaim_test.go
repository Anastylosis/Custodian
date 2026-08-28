package reclaim

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	"github.com/Anastylosis/Custodian/internal/vfs"
	stash "github.com/Anastylosis/stash-go"
)

// twoSceneGroup is the shape every apply test starts from: one keeper and
// one loser, both known to the ledger and to Stash.
func twoSceneGroup(t *testing.T) (*fakeStash, *stash.Client, *ledger.Ledger) {
	t.Helper()
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/tmp/worse.mp4", 2<<30, withTitle("The Real Title")),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})
	return f, c, l
}

func TestDryRunSendsNoMutationsAndPrintsThePlan(t *testing.T) {
	f, c, l := twoSceneGroup(t)

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := f.writes(); len(got) != 0 {
		t.Errorf("a dry run sent mutations: %v", got)
	}
	if !rep.DryRun {
		t.Error("Report.DryRun = false in a dry run")
	}
	if done, _, _, bytes := rep.Counts(); done != 1 || bytes != 2<<30 {
		t.Errorf("Counts() = %d groups / %d bytes, want 1 / %d", done, bytes, int64(2)<<30)
	}

	out := rep.String()
	for _, want := range []string{
		"DRY RUN",
		"/data/_quarantine",
		"_custodian_quarantine",
		"would download a verified backup",
		"/data/tmp/worse.mp4 → /data/_quarantine/worse.mp4",
		"would sceneUpdate scene 10",
		"Re-run with --commit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}

	// The ledger is untouched too: no mutation rows, no verdict change.
	if kinds := mutationKinds(t, l); len(kinds) != 0 {
		t.Errorf("a dry run wrote to the mutation log: %v", kinds)
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictCertain)
}

func TestCommitSendsBackupUnionTagMoveInOrder(t *testing.T) {
	f, c, l := twoSceneGroup(t)

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{"backupDatabase", "sceneUpdate", "tagCreate", "bulkSceneUpdate", "moveFiles"}
	if got := f.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}
	if !f.backupServed {
		t.Error("the backup was never downloaded")
	}
	if !rep.Backup.Taken || !strings.HasSuffix(rep.Backup.Manifest, stash.ManifestSuffix) {
		t.Errorf("Backup = %+v, want a taken backup with a manifest", rep.Backup)
	}

	update, _ := f.input(t, "sceneUpdate", 0)["input"].(map[string]any)
	if update["id"] != "10" {
		t.Errorf("sceneUpdate id = %v, want the keeper", update["id"])
	}
	if update["title"] != "The Real Title" {
		t.Errorf("sceneUpdate title = %v, want the loser's", update["title"])
	}

	move, _ := f.input(t, "moveFiles", 0)["input"].(map[string]any)
	if move["destination_folder"] != "/data/_quarantine" {
		t.Errorf("destination_folder = %v", move["destination_folder"])
	}
	if move["destination_basename"] != "worse.mp4" {
		t.Errorf("destination_basename = %v", move["destination_basename"])
	}
	if ids, _ := move["ids"].([]any); len(ids) != 1 || ids[0] != "f11" {
		t.Errorf("moveFiles ids = %v, want the loser's file", move["ids"])
	}

	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup", "union", "tag", "move"}) {
		t.Errorf("mutation log = %v", kinds)
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictQuarantined)
	assertAllOK(t, l)
}

func TestCommitReusesAnExistingTag(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	f.tagExists = true

	if _, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if slices.Contains(f.writes(), "tagCreate") {
		t.Errorf("an existing tag was created again: %v", f.writes())
	}
}

func TestStaleSceneIsSkippedAndNothingIsSent(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	delete(f.scenes, "11") // the loser vanished from Stash since the sync

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Groups) != 1 || !rep.Groups[0].Skipped {
		t.Fatalf("group = %+v, want it skipped", rep.Groups[0])
	}
	if !strings.Contains(rep.Groups[0].Reason, "gone from Stash") {
		t.Errorf("reason = %q", rep.Groups[0].Reason)
	}
	// The backup still happened; nothing else did.
	if got := f.writes(); !slices.Equal(got, []string{"backupDatabase"}) {
		t.Errorf("writes = %v, want the backup alone", got)
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictCertain)
}

func TestStalePathIsSkipped(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	moved := f.scenes["11"]
	moved.Files[0].Path = "/data/somewhere/else.mp4"
	f.scenes["11"] = moved

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !rep.Groups[0].Skipped || !strings.Contains(rep.Groups[0].Reason, "moved since the last sync") {
		t.Errorf("group = %+v, want a stale-path skip", rep.Groups[0])
	}
}

func TestSceneWithNoFileIsSkipped(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	empty := f.scenes["11"]
	empty.Files = nil
	f.scenes["11"] = empty

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(rep.Groups[0].Reason, "has no file") {
		t.Errorf("reason = %q", rep.Groups[0].Reason)
	}
}

func TestBasenameCollisionGetsTheSceneIDSuffix(t *testing.T) {
	// Two groups whose losers share a basename — routine when the same scene
	// was filed under two performers.
	scenes := []stash.Scene{
		scene("10", "/data/a/keep.mp4", 4<<30),
		scene("11", "/data/a/clip.mp4", 2<<30),
		scene("20", "/data/b/keep2.mp4", 4<<30),
		scene("21", "/data/b/clip.mp4", 1<<30),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-a", "10", scenes[:2]}, planned{"sig-b", "20", scenes[2:]})

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	_ = f

	var tos []string
	for _, g := range rep.Groups {
		for _, l := range g.Losers {
			tos = append(tos, l.To)
		}
	}
	want := []string{"/data/_quarantine/clip.mp4", "/data/_quarantine/clip_21.mp4"}
	if !slices.Equal(tos, want) {
		t.Errorf("quarantine paths = %v, want %v", tos, want)
	}
}

func TestSidecarsAreMovedAfterTheVideo(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	_ = f

	mem := vfs.NewMem("/data/tmp/worse.mp4", "/data/tmp/worse.en.srt", "/data/tmp/other.mp4")
	mover := memMover(t, mem)

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir(), Sidecars: mover})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	side := rep.Groups[0].Losers[0].Sidecars
	if len(side) != 1 || !side[0].Done {
		t.Fatalf("sidecars = %+v, want one moved", side)
	}
	if side[0].To != "/data/_quarantine/worse.en.srt" {
		t.Errorf("sidecar destination = %q", side[0].To)
	}
	if !slices.Contains(mem.Renames, "/data/tmp/worse.en.srt -> /data/_quarantine/worse.en.srt") {
		t.Errorf("renames = %v", mem.Renames)
	}
	if kinds := mutationKinds(t, l); !slices.Contains(kinds, "sidecar") {
		t.Errorf("mutation log = %v, want a sidecar row", kinds)
	}
}

func TestSidecarsPlannedButNotMovedInADryRun(t *testing.T) {
	_, c, l := twoSceneGroup(t)
	mem := vfs.NewMem("/data/tmp/worse.mp4", "/data/tmp/worse.en.srt")

	rep, err := Apply(t.Context(), c, l, Options{Sidecars: memMover(t, mem)})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	side := rep.Groups[0].Losers[0].Sidecars
	if len(side) != 1 || side[0].Done {
		t.Fatalf("sidecars = %+v, want one planned and not moved", side)
	}
	if len(mem.Renames) != 0 {
		t.Errorf("a dry run renamed files: %v", mem.Renames)
	}
}

func TestNoSidecarMoverSaysSoPerGroup(t *testing.T) {
	_, c, l := twoSceneGroup(t)

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Groups[0].Notes) == 0 || !strings.Contains(rep.Groups[0].Notes[0], "no library access") {
		t.Errorf("notes = %v, want one about sidecars being off", rep.Groups[0].Notes)
	}
}

func TestAMidGroupErrorStopsThatGroupAndContinues(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/a/keep.mp4", 4<<30),
		scene("11", "/data/a/clip.mp4", 2<<30),
		scene("20", "/data/b/keep2.mp4", 4<<30),
		scene("21", "/data/b/other.mp4", 1<<30),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-a", "10", scenes[:2]}, planned{"sig-b", "20", scenes[2:]})
	f.failOn["moveFiles"] = "destination is not inside a library path"

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply returned an error for a per-group failure: %v", err)
	}
	if _, _, failed, _ := rep.Counts(); failed != 2 {
		t.Errorf("failed groups = %d, want 2", failed)
	}
	// It got as far as the second group rather than stopping at the first.
	if len(rep.Groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(rep.Groups))
	}
	for _, g := range rep.Groups {
		if len(g.Errs) == 0 || !strings.Contains(g.Errs[0], "not inside a library path") {
			t.Errorf("group %s errs = %v", g.Signature, g.Errs)
		}
	}

	// The failed attempt is on record, stamped as a failure.
	entries, err := l.Mutations(t.Context(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	var failures int
	for _, m := range entries {
		if m.Kind == "move" && m.OK != nil && !*m.OK {
			failures++
			if !strings.Contains(m.Note, "not inside a library path") {
				t.Errorf("failed move note = %q", m.Note)
			}
		}
	}
	if failures != 2 {
		t.Errorf("failed move rows = %d, want 2", failures)
	}
	assertVerdict(t, l, "sig-a", ledger.VerdictCertain)
}

func TestBackupFailureAbortsBeforeAnyMutation(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	f.failOn["backupDatabase"] = "no space left on device"

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err == nil {
		t.Fatal("Apply succeeded without a backup")
	}
	if !strings.Contains(err.Error(), "no verified backup") {
		t.Errorf("err = %v", err)
	}
	if got := f.writes(); !slices.Equal(got, []string{"backupDatabase"}) {
		t.Errorf("writes after a failed backup = %v", got)
	}
	if len(rep.Groups) != 0 {
		t.Errorf("groups = %d, want none touched", len(rep.Groups))
	}
	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup"}) {
		t.Errorf("mutation log = %v, want the failed backup attempt alone", kinds)
	}
	if !strings.Contains(rep.String(), "backup:     FAILED") {
		t.Errorf("report does not show the failure:\n%s", rep.String())
	}
}

func TestLimitCapsTheGroupsActedOn(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/a/keep.mp4", 4<<30),
		scene("11", "/data/a/clip.mp4", 2<<30),
		scene("20", "/data/b/keep2.mp4", 4<<30),
		scene("21", "/data/b/other.mp4", 1<<30),
	}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-a", "10", scenes[:2]}, planned{"sig-b", "20", scenes[2:]})

	rep, err := Apply(t.Context(), c, l, Options{Limit: 1})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Groups) != 1 {
		t.Errorf("groups = %d, want 1", len(rep.Groups))
	}
}

func TestGroupWithoutAKeeperIsNotActedOn(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/a/keep.mp4", 4<<30),
		scene("11", "/data/a/clip.mp4", 2<<30),
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

	rep, err := Apply(ctx, c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Groups) != 0 {
		t.Errorf("groups = %+v, want none — plan has not run", rep.Groups)
	}
}

func TestMultiFileKeeperIsReportedNotActedOn(t *testing.T) {
	keeper := scene("10", "/data/keep/best.mp4", 4<<30)
	keeper.Files = append(keeper.Files, stash.File{ID: "f10b", Path: "/data/keep/best.dup.mp4", Basename: "best.dup.mp4"})
	scenes := []stash.Scene{keeper, scene("11", "/data/tmp/worse.mp4", 2<<30)}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !containsSubstring(rep.Groups[0].Notes, "holds 2 files") {
		t.Errorf("notes = %v", rep.Groups[0].Notes)
	}
}

func TestUnionCarriesWhatTheKeeperLacksAndReportsConflicts(t *testing.T) {
	keeper := scene("10", "/data/keep/best.mp4", 4<<30)
	keeper.StashIDs = []stash.StashID{{Endpoint: "https://sb", ID: "uuid-a"}}
	loser := scene("11", "/data/tmp/worse.mp4", 2<<30, withTitle("The Real Title"))
	loser.StashIDs = []stash.StashID{{Endpoint: "https://sb", ID: "uuid-b"}}
	loser.Performers = []stash.Performer{{ID: "p1", Name: "Alice"}}
	scenes := []stash.Scene{keeper, loser}

	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	u := rep.Groups[0].Union
	if u == nil {
		t.Fatal("no union step reported")
	}
	if !slices.Contains(u.Fields, "title") || !slices.Contains(u.Fields, "performer_ids") {
		t.Errorf("union fields = %v", u.Fields)
	}
	if len(u.Conflicts) != 1 || !strings.Contains(u.Conflicts[0], "dropped uuid-b") {
		t.Errorf("conflicts = %v", u.Conflicts)
	}
}

func TestUnionWithNothingToCarrySendsNoUpdate(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30, withTitle("same")),
		scene("11", "/data/tmp/worse.mp4", 2<<30, withTitle("same")),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if slices.Contains(f.writes(), "sceneUpdate") {
		t.Errorf("an empty union was still sent: %v", f.writes())
	}
	if !strings.Contains(rep.String(), "nothing to carry over") {
		t.Errorf("report does not say the union was empty:\n%s", rep.String())
	}
}

func TestApplyRejectsMissingDependencies(t *testing.T) {
	if _, err := Apply(context.Background(), nil, nil, Options{}); err == nil {
		t.Error("Apply with no client or ledger should fail")
	}
}

func TestDefaultBackupDirIsUnderTheDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/data")
	if got := DefaultBackupDir(); got != "/tmp/data/custodian/backups" {
		t.Errorf("DefaultBackupDir() = %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if got := DefaultBackupDir(); !strings.HasSuffix(got, "/.local/share/custodian/backups") {
		t.Errorf("DefaultBackupDir() = %q", got)
	}
}

// memMover builds a sidecar.Mover over an in-memory FS with an identity path
// map, so the Stash-space paths in these tests are the local ones too.
func memMover(t *testing.T, mem *vfs.Mem) *sidecar.Mover {
	t.Helper()
	mp, err := pathmap.New(nil)
	if err != nil {
		t.Fatalf("pathmap.New: %v", err)
	}
	mv, err := sidecar.NewMover(mem, mp, nil)
	if err != nil {
		t.Fatalf("sidecar.NewMover: %v", err)
	}
	return mv
}

func assertVerdict(t *testing.T, l *ledger.Ledger, signature, want string) {
	t.Helper()
	groups, err := l.Groups(t.Context(), ledger.GroupFilter{})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	for _, g := range groups {
		if g.Signature == signature {
			if g.Verdict != want {
				t.Errorf("verdict of %s = %q, want %q", signature, g.Verdict, want)
			}
			return
		}
	}
	t.Fatalf("group %s not found", signature)
}

func assertAllOK(t *testing.T, l *ledger.Ledger) {
	t.Helper()
	entries, err := l.Mutations(t.Context(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	for _, m := range entries {
		if m.OK == nil || !*m.OK {
			t.Errorf("mutation %s %s is not marked ok: %+v", m.Kind, m.Target, m)
		}
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// seedMistagGroup writes the given scenes and one `mistag` candidate_group
// holding them — no keeper, since apply's mistag path tags every scene
// rather than choosing one.
func seedMistagGroup(t *testing.T, l *ledger.Ledger, signature, reason string, scenes ...stash.Scene) {
	t.Helper()
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	ids := make([]string, len(scenes))
	for i, sc := range scenes {
		ids[i] = sc.ID
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: signature, SceneIDs: ids, Verdict: ledger.VerdictMistag, Reason: reason},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
}

func TestMistagDryRunSendsNoMutationsAndShowsThePlan(t *testing.T) {
	scenes := []stash.Scene{scene("20", "/data/a.mp4", 1<<30), scene("21", "/data/b.mp4", 1<<30)}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedMistagGroup(t, l, "sig-mistag", "20: 1400s vs canonical 1000s", scenes...)

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := f.writes(); len(got) != 0 {
		t.Errorf("a dry run sent mutations: %v", got)
	}
	if len(rep.Mistags) != 1 {
		t.Fatalf("Mistags = %+v, want 1 entry", rep.Mistags)
	}
	m := rep.Mistags[0]
	if len(m.SceneIDs) != 2 {
		t.Errorf("SceneIDs = %v, want both scenes", m.SceneIDs)
	}

	out := rep.String()
	for _, want := range []string{"mistags:", "would add", "_custodian_mistag", "20: 1400s vs canonical 1000s"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan is missing %q:\n%s", want, out)
		}
	}
	assertVerdict(t, l, "sig-mistag", ledger.VerdictMistag)
}

func TestMistagCommitTagsEveryScene(t *testing.T) {
	scenes := []stash.Scene{scene("20", "/data/a.mp4", 1<<30), scene("21", "/data/b.mp4", 1<<30)}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedMistagGroup(t, l, "sig-mistag", "sources disagree", scenes...)

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Mistags) != 1 || !rep.Mistags[0].Tagged {
		t.Fatalf("Mistags = %+v, want it tagged", rep.Mistags)
	}

	want := []string{"backupDatabase", "tagCreate", "bulkSceneUpdate"}
	if got := f.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes = %v, want %v (no moveFiles or sceneUpdate — mistag never moves)", got, want)
	}

	bulk, _ := f.input(t, "bulkSceneUpdate", 0)["input"].(map[string]any)
	ids, _ := bulk["ids"].([]any)
	if len(ids) != 2 {
		t.Errorf("bulkSceneUpdate ids = %v, want both scenes", bulk["ids"])
	}

	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup", "tag"}) {
		t.Errorf("mutation log = %v", kinds)
	}
	// A mistag group's verdict is not moved by apply — the fix is metadata,
	// not a state transition.
	assertVerdict(t, l, "sig-mistag", ledger.VerdictMistag)
	assertAllOK(t, l)
}

func TestMistagCommitReusesAnExistingTag(t *testing.T) {
	scenes := []stash.Scene{scene("20", "/data/a.mp4", 1<<30)}
	f, c := newFakeStash(t, scenes...)
	f.tagExists = true
	l := newLedger(t)
	seedMistagGroup(t, l, "sig-mistag", "x", scenes...)

	if _, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if slices.Contains(f.writes(), "tagCreate") {
		t.Errorf("an existing tag was created again: %v", f.writes())
	}
}

func TestMistagCommitFailureIsReportedNotFatal(t *testing.T) {
	scenes := []stash.Scene{scene("20", "/data/a.mp4", 1<<30)}
	f, c := newFakeStash(t, scenes...)
	f.failOn["bulkSceneUpdate"] = "boom"
	l := newLedger(t)
	seedMistagGroup(t, l, "sig-mistag", "x", scenes...)

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply should not fail the whole run over one mistag group: %v", err)
	}
	if len(rep.Mistags) != 1 || rep.Mistags[0].Err == "" {
		t.Fatalf("Mistags = %+v, want an error recorded", rep.Mistags)
	}
	tagged, failed := rep.MistagCounts()
	if tagged != 0 || failed != 1 {
		t.Errorf("MistagCounts() = %d, %d, want 0, 1", tagged, failed)
	}
}

func TestPlannedGroupsIncludesPartialAlongsideCertain(t *testing.T) {
	certainScenes := []stash.Scene{scene("10", "/data/keep/best.mp4", 4<<30), scene("11", "/data/tmp/worse.mp4", 2<<30)}
	partialScenes := []stash.Scene{scene("30", "/data/keep/complete.mp4", 3<<30), scene("31", "/data/tmp/clip.mp4", 1<<30)}
	all := append(append([]stash.Scene{}, certainScenes...), partialScenes...)
	_, c := newFakeStash(t, all...)
	l := newLedger(t)

	// Both groups have to arrive in the same ReplaceGroups call: it
	// tombstones any candidate_group an earlier run left out.
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, all); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig-certain", SceneIDs: []string{"10", "11"}, Verdict: ledger.VerdictCertain, Reason: "x"},
		{Signature: "sig-partial", SceneIDs: []string{"30", "31"}, Verdict: ledger.VerdictPartial, Reason: "x"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.SetChoice(ctx, "sig-certain", "10", []string{"11"}, nil); err != nil {
		t.Fatalf("SetChoice (certain): %v", err)
	}
	if err := l.SetChoice(ctx, "sig-partial", "30", []string{"31"}, nil); err != nil {
		t.Fatalf("SetChoice (partial): %v", err)
	}

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Groups) != 2 {
		t.Fatalf("Groups = %+v, want both the certain and the partial group processed", rep.Groups)
	}
	assertVerdict(t, l, "sig-certain", ledger.VerdictQuarantined)
	assertVerdict(t, l, "sig-partial", ledger.VerdictQuarantined)
}
