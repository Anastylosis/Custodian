package reclaim

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// multifileScene builds a scene holding several files — the shape
// evidence.MultifileGenerator groups (REBUILD.md §1.5): Stash attaches a
// re-detected file to an existing scene by oshash/md5, so within one scene
// the files are already byte-equivalent. The first file given is the
// scene's primary, matching how stash.Scene.PrimaryFile() reads Files[0].
func multifileScene(id string, files ...stash.File) stash.Scene {
	return stash.Scene{ID: id, Files: files}
}

// findFile returns the file with the given id, for asserting a scene's
// post-move state against the fake.
func findFile(files []stash.File, id string) *stash.File {
	for i := range files {
		if files[i].ID == id {
			return &files[i]
		}
	}
	return nil
}

// seedMultifileGroup writes one multi-file scene and its `certain`
// candidate_group carrying a file_choice step — what `scan` (evidence.Run
// with MultifileGenerator) leaves behind, without `plan` ever touching it:
// a multifile group has one scene, so plan has nothing to choose between.
func seedMultifileGroup(t *testing.T, l *ledger.Ledger, signature string, sc stash.Scene, chosenFileID string) {
	t.Helper()
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{sc}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{{
		Signature: signature, SceneIDs: []string{sc.ID}, Verdict: ledger.VerdictCertain,
		Reason:   "byte-equivalent files attached to one scene",
		Evidence: []ledger.Evidence{{Generator: "multifile", Kind: "multifile", Detail: sc.ID}},
		Steps:    []ledger.Step{{Rule: "file_choice", Outcome: "filed", Detail: chosenFileID}},
	}}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
}

// multifileApplied applies (commit) a multi-file scene's group and returns
// the fake, client, ledger and Apply's report, with the group quarantined
// for real — no `plan` step, since a multifile group has nothing for it to
// choose between scenes.
func multifileApplied(t *testing.T, sc stash.Scene, chosen string) (*fakeStash, *stash.Client, *ledger.Ledger, Report) {
	t.Helper()
	f, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, chosen)

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Multifiles) != 1 || !rep.Multifiles[0].Done {
		t.Fatalf("Apply Multifiles = %+v, want one done", rep.Multifiles)
	}
	return f, c, l, rep
}

func twoFileScene() stash.Scene {
	return multifileScene("10",
		stash.File{ID: "f10a", Basename: "take1.mp4", Path: "/data/keep/take1.mp4", Size: 4 << 30},
		stash.File{ID: "f10b", Basename: "take2.mp4", Path: "/data/keep/take2.mp4", Size: 3 << 30},
	)
}

func TestMultifileApplyDryRunSendsNoMutations(t *testing.T) {
	sc := twoFileScene()
	f, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10b")

	rep, err := Apply(t.Context(), c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := f.writes(); len(got) != 0 {
		t.Errorf("a dry run sent mutations: %v", got)
	}
	if kinds := mutationKinds(t, l); len(kinds) != 0 {
		t.Errorf("a dry run wrote to the mutation log: %v", kinds)
	}
	if len(rep.Multifiles) != 1 || !rep.Multifiles[0].Done {
		t.Fatalf("Multifiles = %+v, want one done", rep.Multifiles)
	}

	mr := rep.Multifiles[0]
	if mr.Kept != "f10b" || !mr.PrimarySet {
		t.Errorf("MultifileReport = %+v, want f10b kept and PrimarySet", mr)
	}
	if len(mr.Moved) != 1 || mr.Moved[0].FileID != "f10a" || mr.Moved[0].To != "/data/_quarantine/take1.mp4" {
		t.Errorf("Moved = %+v", mr.Moved)
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictCertain)

	out := rep.String()
	for _, want := range []string{
		"multifile:", "group sig-mf scene 10", "keep:    f10b",
		"would set file f10b", "take1.mp4", "would clean up: 1 scenes",
		"Re-run with --commit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestMultifileApplyCommitSetsPrimaryAndMovesTheOthers(t *testing.T) {
	sc := twoFileScene()
	f, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10b")

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{"backupDatabase", "sceneUpdate", "moveFiles"}
	if got := f.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes = %v, want %v (no tagCreate/bulkSceneUpdate — multifile never tags)", got, want)
	}

	update, _ := f.input(t, "sceneUpdate", 0)["input"].(map[string]any)
	if update["id"] != "10" || update["primary_file_id"] != "f10b" {
		t.Errorf("sceneUpdate input = %v, want primary_file_id f10b on scene 10", update)
	}
	move, _ := f.input(t, "moveFiles", 0)["input"].(map[string]any)
	if ids, _ := move["ids"].([]any); len(ids) != 1 || ids[0] != "f10a" {
		t.Errorf("moveFiles ids = %v, want the loser file f10a", move["ids"])
	}

	scene, _ := f.scene("10")
	if scene.PrimaryFile().ID != "f10b" {
		t.Errorf("primary after apply = %s, want f10b", scene.PrimaryFile().ID)
	}
	moved := findFile(scene.Files, "f10a")
	if moved == nil || moved.Path != "/data/_quarantine/take1.mp4" {
		t.Fatalf("f10a after apply = %+v, want it quarantined", moved)
	}

	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup", "primary", "move"}) {
		t.Errorf("mutation log = %v", kinds)
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictQuarantined)
	assertAllOK(t, l)

	if rep.ChangesFile == "" {
		t.Fatal("ChangesFile is empty")
	}
}

func TestMultifileApplyChosenAlreadyPrimarySkipsSetPrimaryFile(t *testing.T) {
	sc := twoFileScene()
	f, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10a") // f10a is already Files[0] — the primary

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if slices.Contains(f.writes(), "sceneUpdate") {
		t.Errorf("SetPrimaryFile was sent when the chosen file was already primary: %v", f.writes())
	}
	if rep.Multifiles[0].PrimarySet {
		t.Error("PrimarySet = true, want false — nothing needed setting")
	}
	if slices.Contains(mutationKinds(t, l), "primary") {
		t.Errorf("mutation log = %v, want no primary row", mutationKinds(t, l))
	}
}

func TestMultifileApplyBasenameCollisionGetsFileIDSuffix(t *testing.T) {
	sc := multifileScene("10",
		stash.File{ID: "f10a", Basename: "clip.mp4", Path: "/data/keep/clip.mp4", Size: 4 << 30},
		stash.File{ID: "f10b", Basename: "clip.mp4", Path: "/data/staging/clip.mp4", Size: 2 << 30},
		stash.File{ID: "f10c", Basename: "clip.mp4", Path: "/data/other/clip.mp4", Size: 1 << 30},
	)
	_, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10a")

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var tos []string
	for _, m := range rep.Multifiles[0].Moved {
		tos = append(tos, m.To)
	}
	want := []string{"/data/_quarantine/clip.mp4", "/data/_quarantine/clip_f10c.mp4"}
	if !slices.Equal(tos, want) {
		t.Errorf("quarantine paths = %v, want %v", tos, want)
	}
}

func TestMultifileApplyMissingFileChoiceStepIsSkipped(t *testing.T) {
	sc := twoFileScene()
	_, c := newFakeStash(t, sc)
	l := newLedger(t)
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{sc}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{{
		Signature: "sig-mf", SceneIDs: []string{"10"}, Verdict: ledger.VerdictCertain,
		Evidence: []ledger.Evidence{{Generator: "multifile", Kind: "multifile"}},
		// No Steps — as if `plan` ran over it (a single-scene group) and
		// recomputed an empty Choice, wiping the file_choice step scan wrote.
	}}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	rep, err := Apply(ctx, c, l, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(rep.Multifiles) != 1 || !rep.Multifiles[0].Skipped || !strings.Contains(rep.Multifiles[0].Reason, "file_choice") {
		t.Errorf("Multifiles = %+v", rep.Multifiles)
	}
}

func TestMultifileApplySkipsAGoneScene(t *testing.T) {
	sc := twoFileScene()
	f, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10b")
	delete(f.scenes, "10")

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !rep.Multifiles[0].Skipped || !strings.Contains(rep.Multifiles[0].Reason, "gone from Stash") {
		t.Errorf("Multifiles = %+v", rep.Multifiles)
	}
}

func TestChangesJSONContentForMultifileApply(t *testing.T) {
	sc := twoFileScene()
	_, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10b")

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.ChangesFile == "" {
		t.Fatal("ChangesFile is empty")
	}

	data, err := os.ReadFile(rep.ChangesFile)
	if err != nil {
		t.Fatalf("reading changes JSON: %v", err)
	}
	var entries []ChangeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decoding changes JSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Signature != "sig-mf" || e.Keeper != "10" || e.RunID != rep.RunID {
		t.Errorf("entry = %+v, want sig-mf/10/%d", e, rep.RunID)
	}
	if len(e.Files) != 1 || e.Files[0].ID != "f10a" ||
		e.Files[0].From != "/data/keep/take1.mp4" || e.Files[0].To != "/data/_quarantine/take1.mp4" {
		t.Errorf("Files = %+v", e.Files)
	}
	if len(e.Losers) != 0 || len(e.Union) != 0 {
		t.Errorf("a multifile entry should carry no losers/union: %+v", e)
	}
}

func TestMultifilePruneDryRunSendsNoMutations(t *testing.T) {
	sc := twoFileScene()
	f, c, l, applyRep := multifileApplied(t, sc, "f10b")
	ctx := t.Context()
	backdateRun(t, l, applyRep.RunID, 60*24*time.Hour)
	beforeWrites, beforeLog := f.writes(), mutationKinds(t, l)

	prep, err := Prune(ctx, c, l, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := f.writes(); !slices.Equal(got, beforeWrites) {
		t.Errorf("a dry run sent mutations: %v", got)
	}
	if got := mutationKinds(t, l); !slices.Equal(got, beforeLog) {
		t.Errorf("a dry run wrote to the mutation log: %v", got)
	}
	if len(prep.Groups) != 1 || !prep.Groups[0].Multifile || prep.Groups[0].Skipped {
		t.Fatalf("PruneGroup = %+v, want one done multifile group", prep.Groups)
	}
	if !strings.Contains(prep.String(), "same scene, no merge needed") {
		t.Errorf("report does not explain the no-merge shape:\n%s", prep.String())
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictQuarantined)
}

func TestMultifilePruneDeletesWithoutMerging(t *testing.T) {
	sc := twoFileScene()
	f, c, l, applyRep := multifileApplied(t, sc, "f10b")
	ctx := t.Context()
	backdateRun(t, l, applyRep.RunID, 60*24*time.Hour)

	prep, err := Prune(ctx, c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if slices.Contains(f.writes(), "sceneMerge") {
		t.Errorf("a multifile prune ran sceneMerge: %v", f.writes())
	}
	if !slices.Contains(f.writes(), "deleteFiles") {
		t.Fatalf("writes = %v, want deleteFiles", f.writes())
	}
	del := f.input(t, "deleteFiles", 0)
	if ids, _ := del["ids"].([]any); len(ids) != 1 || ids[0] != "f10a" {
		t.Errorf("deleteFiles ids = %v, want the moved-out file f10a", del["ids"])
	}
	if len(prep.Groups) != 1 || !prep.Groups[0].Multifile || !prep.Groups[0].Done {
		t.Fatalf("PruneGroup = %+v, want a done multifile group", prep.Groups)
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictReclaimed)
	assertAllOK(t, l)
}

func TestMultifileUndoDryRunSendsNothing(t *testing.T) {
	sc := twoFileScene()
	f, c, l, _ := multifileApplied(t, sc, "f10b")
	beforeLog := mutationKinds(t, l)

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-mf"})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !rep.DryRun {
		t.Error("Report.DryRun = false in a dry run")
	}

	scene, _ := f.scene("10")
	moved := findFile(scene.Files, "f10a")
	if moved == nil || moved.Path != "/data/_quarantine/take1.mp4" {
		t.Errorf("a dry run moved the file back: %+v", moved)
	}
	if scene.PrimaryFile().ID != "f10b" {
		t.Error("a dry run restored the primary")
	}
	if got := mutationKinds(t, l); !slices.Equal(got, beforeLog) {
		t.Errorf("a dry run wrote to the mutation log: %v", got)
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictQuarantined)
}

func TestMultifileUndoRestoresFileAndPrimary(t *testing.T) {
	sc := twoFileScene()
	f, c, l, _ := multifileApplied(t, sc, "f10b")

	// Sanity: the apply actually happened.
	scene, _ := f.scene("10")
	if scene.PrimaryFile().ID != "f10b" {
		t.Fatalf("primary after apply = %s, want f10b", scene.PrimaryFile().ID)
	}

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-mf", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if done, skipped, failed := rep.Counts(); done != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("Undo Counts = %d/%d/%d, want 1/0/0:\n%s", done, skipped, failed, rep.String())
	}

	scene, _ = f.scene("10")
	restored := findFile(scene.Files, "f10a")
	if restored == nil || restored.Path != "/data/keep/take1.mp4" {
		t.Errorf("f10a after undo = %+v, want it back at /data/keep/take1.mp4", restored)
	}
	if scene.PrimaryFile().ID != "f10a" {
		t.Errorf("primary after undo = %s, want f10a restored", scene.PrimaryFile().ID)
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictCertain)

	kinds := mutationKinds(t, l)
	for _, want := range []string{"undo:move", "undo:primary"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("mutation log = %v, missing %q", kinds, want)
		}
	}
	assertAllOK(t, l)

	if len(rep.Multifiles) != 1 || !rep.Multifiles[0].PrimaryRestored || !rep.Multifiles[0].Done {
		t.Errorf("Multifiles = %+v, want primary restored and done", rep.Multifiles)
	}
	if len(rep.Multifiles[0].Moved) != 1 || !rep.Multifiles[0].Moved[0].MovedBack {
		t.Errorf("Moved = %+v, want the file moved back", rep.Multifiles[0].Moved)
	}

	out := rep.String()
	for _, want := range []string{"group sig-mf scene 10", "primary: restored", "restored to certain"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestMultifileUndoWithNothingToRestorePrimarySaysSo(t *testing.T) {
	sc := twoFileScene()
	f, c, l, _ := multifileApplied(t, sc, "f10a") // already primary — no SetPrimaryFile sent
	_ = f

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-mf", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	mr := rep.Multifiles[0]
	if mr.PrimaryRestored {
		t.Error("PrimaryRestored = true, want false — nothing was ever changed")
	}
	if !containsSubstring(mr.Notes, "no primary mutation was logged") {
		t.Errorf("Notes = %v", mr.Notes)
	}
}

func TestMultifileUndoRefusesAReclaimedGroup(t *testing.T) {
	sc := twoFileScene()
	_, c, l, _ := multifileApplied(t, sc, "f10b")
	if err := l.SetOutcome(t.Context(), "sig-mf", ledger.VerdictReclaimed, 999); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-mf", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(rep.Multifiles) != 1 || !rep.Multifiles[0].Skipped {
		t.Fatalf("Multifiles = %+v, want it skipped", rep.Multifiles)
	}
	if !strings.Contains(rep.Multifiles[0].Reason, "not quarantined") {
		t.Errorf("reason = %q", rep.Multifiles[0].Reason)
	}
}

func TestMultifileReportRendersKeptMovedAndBytes(t *testing.T) {
	rep := Report{
		Multifiles: []MultifileReport{{
			Signature: "0123456789abcdef", SceneID: "10",
			Kept: "f10b", KeptPath: "/data/keep/take2.mp4", PrimarySet: true,
			Moved: []MovedFile{{
				FileID: "f10a", From: "/data/keep/take1.mp4", To: "/data/_quarantine/take1.mp4", Bytes: 4 << 30,
			}},
			Bytes: 4 << 30, Done: true,
		}},
	}
	out := rep.String()
	for _, want := range []string{
		"multifile:",
		"group 0123456789ab… scene 10",
		"keep:    f10b (/data/keep/take2.mp4)",
		"primary: set file f10b",
		"/data/keep/take1.mp4",
		"/data/_quarantine/take1.mp4",
		"reclaimable:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestMultifileReportRendersSkippedNotesAndErrors(t *testing.T) {
	rep := Report{
		Multifiles: []MultifileReport{
			{Signature: "sig-skip", SceneID: "20", Skipped: true, Reason: "no file_choice step recorded"},
			{
				Signature: "sig-1", SceneID: "10", Kept: "f10b", KeptPath: "/data/keep/take2.mp4",
				Moved: []MovedFile{
					{
						FileID: "f10a", From: "/data/keep/take1.mp4", To: "/data/_quarantine/take1.mp4", Bytes: 1024,
						Sidecars: []SidecarMove{
							{From: "/data/keep/take1.en.srt", To: "/data/_quarantine/take1.en.srt", Done: true},
							{From: "/data/keep/take1.es.srt", To: "/data/_quarantine/take1.es.srt", Note: "destination already exists"},
							{From: "/data/keep/take1.fr.srt", To: "/data/_quarantine/take1.fr.srt"},
						},
						Notes: []string{"could not inspect sidecars: nope"},
					},
				},
				Notes: []string{"sidecars: no library access configured"},
				Errs:  []string{"moveFiles failed"},
				Bytes: 1024,
			},
		},
	}
	out := rep.String()
	for _, want := range []string{
		"SKIPPED: no file_choice step recorded",
		"group sig-1 scene 10",
		"take1.en.srt", "(destination already exists)", "(planned)",
		"note:    could not inspect sidecars: nope",
		"note: sidecars: no library access configured",
		"ERROR: moveFiles failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	if _, _, failed, _ := rep.MultifileCounts(); failed != 1 {
		t.Errorf("a failed multifile group did not count as failed")
	}
	if !strings.Contains(rep.Summary(), "multifile 0, skipped 1, failed 1") {
		t.Errorf("Summary() = %q", rep.Summary())
	}
}

func TestMultifileUndoReportStringCommitAndDryRun(t *testing.T) {
	rep := UndoReport{
		DryRun: true,
		Multifiles: []MultifileUndoReport{
			{Signature: "sig-skip", SceneID: "20", Skipped: true, Reason: "verdict is \"reclaimed\", not quarantined — refusing to undo"},
			{
				Signature: "sig-1", SceneID: "10",
				Moved: []UndoMovedFile{{
					FileID: "f10a", From: "/data/_quarantine/take1.mp4", To: "/data/keep/take1.mp4",
					Sidecars: []SidecarMove{{From: "/data/_quarantine/take1.en.srt", To: "/data/keep/take1.en.srt"}},
					Notes:    []string{"file sits somewhere unexpected"},
				}},
				PrimaryRestored: true,
				Notes:           []string{"no primary mutation was logged"},
				RestoredVerdict: ledger.VerdictCertain,
			},
		},
	}
	out := rep.String()
	for _, want := range []string{
		"SKIPPED", "refusing to undo",
		"group sig-1 scene 10", "file f10a",
		"/data/_quarantine/take1.mp4 → /data/keep/take1.mp4",
		"sidecar:", "note:    file sits somewhere unexpected",
		"primary: would restore", "note:    no primary mutation was logged",
		"would restore to certain",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run String() is missing %q:\n%s", want, out)
		}
	}

	rep.DryRun = false
	rep.Multifiles[1].Done = true
	rep.Multifiles[1].Moved[0].MovedBack = true
	rep.Multifiles[1].Moved[0].Errs = []string{"boom"}
	rep.Multifiles[1].Errs = []string{"boom"}
	out = rep.String()
	for _, want := range []string{"primary: restored", "restored to certain", "ERROR:   boom", "ERROR: boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("commit String() is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "DRY RUN") {
		t.Error("a committed report should not say DRY RUN")
	}
}

func TestMultifilePruneSkipsAGoneScene(t *testing.T) {
	sc := twoFileScene()
	f, c, l, applyRep := multifileApplied(t, sc, "f10b")
	delete(f.scenes, "10")
	backdateRun(t, l, applyRep.RunID, 60*24*time.Hour)

	prep, err := Prune(t.Context(), c, l, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !prep.Groups[0].Skipped || !strings.Contains(prep.Groups[0].Reason, "gone from Stash") {
		t.Errorf("PruneGroup = %+v", prep.Groups[0])
	}
}

func TestMultifilePruneSkipsWhenNothingIsLeftToReclaim(t *testing.T) {
	sc := twoFileScene()
	f, c, l, applyRep := multifileApplied(t, sc, "f10b")
	backdateRun(t, l, applyRep.RunID, 60*24*time.Hour)

	// The loser was already reclaimed by hand: only the kept file remains.
	f.mu.Lock()
	kept := f.scenes["10"]
	kept.Files = []stash.File{*findFile(kept.Files, "f10b")}
	f.scenes["10"] = kept
	f.mu.Unlock()

	prep, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !prep.Groups[0].Skipped || !strings.Contains(prep.Groups[0].Reason, "nothing left to reclaim") {
		t.Errorf("PruneGroup = %+v", prep.Groups[0])
	}
	if slices.Contains(f.writes(), "deleteFiles") {
		t.Error("deleteFiles ran with nothing left to reclaim")
	}
}

func TestMultifilePruneRecordsAFailedDelete(t *testing.T) {
	sc := twoFileScene()
	f, c, l, applyRep := multifileApplied(t, sc, "f10b")
	backdateRun(t, l, applyRep.RunID, 60*24*time.Hour)
	f.failOn["deleteFiles"] = "destination not found"

	prep, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prune returned an error for a per-group failure: %v", err)
	}
	if len(prep.Groups[0].Errs) == 0 {
		t.Fatal("the failed delete was not reported")
	}
	assertVerdict(t, l, "sig-mf", ledger.VerdictQuarantined)
}

func TestMultifileUndoSkipsWhenNoMoveMutationIsLogged(t *testing.T) {
	// A quarantined multifile group whose applying run never logged a move
	// mutation for it — synthetic, since Apply always logs one, but Undo
	// must still refuse cleanly rather than restore nothing silently.
	sc := twoFileScene()
	f, c := newFakeStash(t, sc)
	l := newLedger(t)
	seedMultifileGroup(t, l, "sig-mf", sc, "f10b")
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.SetOutcome(ctx, "sig-mf", ledger.VerdictQuarantined, runID); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
	_ = f

	rep, err := Undo(ctx, c, l, UndoOptions{Signature: "sig-mf", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !rep.Multifiles[0].Skipped || !strings.Contains(rep.Multifiles[0].Reason, "no move mutation logged") {
		t.Errorf("Multifiles = %+v", rep.Multifiles)
	}
}

func TestMultifileUndoSkipsAGoneScene(t *testing.T) {
	sc := twoFileScene()
	f, c, l, _ := multifileApplied(t, sc, "f10b")
	delete(f.scenes, "10")

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-mf", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !rep.Multifiles[0].Skipped || !strings.Contains(rep.Multifiles[0].Reason, "gone from Stash") {
		t.Errorf("Multifiles = %+v", rep.Multifiles)
	}
}

func TestMultifileUndoRefusesWhenAnotherFileOccupiesTheOriginalPath(t *testing.T) {
	sc := twoFileScene()
	f, c, l, _ := multifileApplied(t, sc, "f10b")

	f.mu.Lock()
	f.scenes["99"] = scene("99", "/data/keep/take1.mp4", 1<<20)
	f.mu.Unlock()

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-mf", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	mv := rep.Multifiles[0].Moved[0]
	if mv.MovedBack {
		t.Error("MovedBack = true, want the move refused")
	}
	if !containsSubstring(mv.Errs, "already has a file") {
		t.Errorf("Errs = %v, want a refusal about the occupied path", mv.Errs)
	}
}

// backdateRun sets runID's started_at into the past, so Prune's age filter
// (`AppliedBefore`) picks it up — the same trick prune_test.go's
// quarantinedGroup uses for the tag-and-union shape.
func backdateRun(t *testing.T, l *ledger.Ledger, runID int64, age time.Duration) {
	t.Helper()
	when := time.Now().UTC().Add(-age).Format(time.RFC3339)
	if _, err := l.DB().ExecContext(t.Context(), `UPDATE run SET started_at = ? WHERE id = ?`, when, runID); err != nil {
		t.Fatalf("backdating run %d: %v", runID, err)
	}
}
