package reclaim

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/vfs"
	stash "github.com/Anastylosis/stash-go"
)

// appliedGroup applies (commit) a two-scene group — a keeper missing a
// title and a director, and a loser carrying both — and returns the fake,
// client and ledger with the loser quarantined for real.
func appliedGroup(t *testing.T) (*fakeStash, *stash.Client, *ledger.Ledger, Report) {
	t.Helper()
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/tmp/worse.mp4", 2<<30, withTitle("The Real Title"), withDirector("Some Director")),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if done, _, failed, _ := rep.Counts(); done != 1 || failed != 0 {
		t.Fatalf("Apply Counts = %d done, %d failed, want 1, 0", done, failed)
	}
	return f, c, l, rep
}

func TestUndoRestoresTagsPathsAndKeeperFields(t *testing.T) {
	f, c, l, _ := appliedGroup(t)

	// Sanity: the apply actually happened.
	loser, _ := f.scene("11")
	if loser.Files[0].Path != "/data/_quarantine/worse.mp4" || !hasTagID(loser.Tags, "tag-1") {
		t.Fatalf("loser after apply = %+v, want it quarantined", loser)
	}

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if done, skipped, failed := rep.Counts(); done != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("Undo Counts = %d/%d/%d, want 1/0/0:\n%s", done, skipped, failed, rep.String())
	}

	loser, _ = f.scene("11")
	if loser.Files[0].Path != "/data/tmp/worse.mp4" {
		t.Errorf("loser path = %q, want it back at /data/tmp/worse.mp4", loser.Files[0].Path)
	}
	if hasTagID(loser.Tags, "tag-1") {
		t.Errorf("loser still carries the quarantine tag: %+v", loser.Tags)
	}

	keeper, _ := f.scene("10")
	if keeper.Title != "" || keeper.Director != "" {
		t.Errorf("keeper = %+v, want title and director restored to empty", keeper)
	}

	assertVerdict(t, l, "sig-1", ledger.VerdictCertain)

	kinds := mutationKinds(t, l)
	for _, want := range []string{"undo:tag", "undo:move", "undo:union"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("mutation log = %v, missing %q", kinds, want)
		}
	}
	assertAllOK(t, l)

	gr := rep.Groups[0]
	if !slices.Contains(gr.KeeperFields, "title") || !slices.Contains(gr.KeeperFields, "director") {
		t.Errorf("KeeperFields = %v, want title and director", gr.KeeperFields)
	}
	if len(gr.Losers) != 1 || !gr.Losers[0].Untagged || !gr.Losers[0].MovedBack {
		t.Errorf("loser report = %+v, want untagged and moved back", gr.Losers)
	}
}

func TestUndoRefusesAReclaimedGroup(t *testing.T) {
	_, c, l, _ := appliedGroup(t)
	if err := l.SetOutcome(t.Context(), "sig-1", ledger.VerdictReclaimed, 999); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(rep.Groups) != 1 || !rep.Groups[0].Skipped {
		t.Fatalf("group = %+v, want it skipped", rep.Groups)
	}
	if !strings.Contains(rep.Groups[0].Reason, "not quarantined") {
		t.Errorf("reason = %q", rep.Groups[0].Reason)
	}
	if got := len(undoMutationRows(t, l)); got != 0 {
		t.Errorf("undoing a reclaimed group sent %d mutations", got)
	}
}

func TestUndoPartiallyRestoresWhenTheKeeperWasEditedSinceApply(t *testing.T) {
	f, c, l, _ := appliedGroup(t)

	// Someone (or something) changed the keeper's title in Stash after
	// apply but before undo — director is untouched.
	edited, _ := f.scene("10")
	edited.Title = "User Edited Title"
	f.mu.Lock()
	f.scenes["10"] = edited
	f.mu.Unlock()

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	gr := rep.Groups[0]
	if slices.Contains(gr.KeeperFields, "title") {
		t.Errorf("title was restored despite being edited: %v", gr.KeeperFields)
	}
	if !slices.Contains(gr.KeeperFields, "director") {
		t.Errorf("director was not restored: %v", gr.KeeperFields)
	}
	if !containsSubstring(gr.KeeperNotes, "title") {
		t.Errorf("notes = %v, want one about title being edited", gr.KeeperNotes)
	}

	keeper, _ := f.scene("10")
	if keeper.Title != "User Edited Title" {
		t.Errorf("title = %q, want the edit left alone", keeper.Title)
	}
	if keeper.Director != "" {
		t.Errorf("director = %q, want it restored to empty", keeper.Director)
	}
	// The rest of the group still finishes: a partial keeper restore is not
	// a failure.
	if !gr.Done {
		t.Errorf("group = %+v, want it done despite the partial restore", gr)
	}
}

func TestUndoDryRunSendsNothing(t *testing.T) {
	f, c, l, applyRep := appliedGroup(t)
	beforeLog := mutationKinds(t, l)

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1"})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !rep.DryRun {
		t.Error("Report.DryRun = false in a dry run")
	}

	loser, _ := f.scene("11")
	if loser.Files[0].Path != "/data/_quarantine/worse.mp4" {
		t.Errorf("a dry run moved the file: %q", loser.Files[0].Path)
	}
	if !hasTagID(loser.Tags, "tag-1") {
		t.Error("a dry run removed the tag")
	}
	keeper, _ := f.scene("10")
	if keeper.Title != "The Real Title" {
		t.Errorf("a dry run changed the keeper's title: %q", keeper.Title)
	}

	if got := mutationKinds(t, l); !slices.Equal(got, beforeLog) {
		t.Errorf("a dry run wrote to the mutation log: %v", got)
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictQuarantined)
	_ = applyRep
}

func TestUndoByRunIDCoversEveryGroupTheRunQuarantined(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/a/keep.mp4", 4<<30),
		scene("11", "/data/a/clip.mp4", 2<<30),
		scene("20", "/data/b/keep2.mp4", 4<<30),
		scene("21", "/data/b/other.mp4", 1<<30),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-a", "10", scenes[:2]}, planned{"sig-b", "20", scenes[2:]})

	applyRep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rep, err := Undo(t.Context(), c, l, UndoOptions{RunID: applyRep.RunID, Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if len(rep.Groups) != 2 {
		t.Fatalf("Groups = %+v, want both groups undone", rep.Groups)
	}
	assertVerdict(t, l, "sig-a", ledger.VerdictCertain)
	assertVerdict(t, l, "sig-b", ledger.VerdictCertain)

	for _, id := range []string{"11", "21"} {
		sc, _ := f.scene(id)
		if hasTagID(sc.Tags, "tag-1") {
			t.Errorf("scene %s still tagged", id)
		}
	}
}

func TestUndoNeedsExactlyOneOfSignatureOrRunID(t *testing.T) {
	_, c, l, _ := appliedGroup(t)
	if _, err := Undo(t.Context(), c, l, UndoOptions{}); err == nil {
		t.Error("Undo with neither Signature nor RunID should fail")
	}
	if _, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1", RunID: 1}); err == nil {
		t.Error("Undo with both Signature and RunID should fail")
	}
}

func TestChangesJSONContentForOneApply(t *testing.T) {
	f, c, l := twoSceneGroup(t)
	_ = f

	rep, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.ChangesErr != "" {
		t.Fatalf("ChangesErr = %q", rep.ChangesErr)
	}
	if rep.ChangesFile == "" {
		t.Fatal("ChangesFile is empty")
	}
	if !strings.HasSuffix(rep.ChangesFile, ".changes.json") {
		t.Errorf("ChangesFile = %q, want a .changes.json suffix", rep.ChangesFile)
	}
	if !strings.Contains(rep.String(), rep.ChangesFile) {
		t.Errorf("report does not print the changes JSON path:\n%s", rep.String())
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

	if e.Signature != "sig-1" || e.Keeper != "10" || e.RunID != rep.RunID {
		t.Errorf("entry = %+v, want sig-1/10/%d", e, rep.RunID)
	}
	if _, err := time.Parse(time.RFC3339, e.At); err != nil {
		t.Errorf("At = %q is not RFC3339: %v", e.At, err)
	}

	title, ok := e.Union["title"]
	if !ok {
		t.Fatalf("union = %+v, missing title", e.Union)
	}
	if title.Before != "" || title.After != "The Real Title" {
		t.Errorf("title change = %+v, want before \"\" after \"The Real Title\"", title)
	}

	if len(e.Losers) != 1 {
		t.Fatalf("losers = %+v, want 1", e.Losers)
	}
	l0 := e.Losers[0]
	if l0.SceneID != "11" || l0.FileID != "f11" || l0.From != "/data/tmp/worse.mp4" ||
		l0.To != "/data/_quarantine/worse.mp4" || l0.Tag != DefaultTag {
		t.Errorf("loser change = %+v", l0)
	}
	if len(e.Merged) != 0 || len(e.Deleted) != 0 {
		t.Errorf("an apply entry should carry no merged/deleted: %+v", e)
	}
}

func TestUndoRefusesWhenAnotherFileOccupiesTheOriginalPath(t *testing.T) {
	f, c, l, _ := appliedGroup(t)

	// Something else now sits where the loser used to be.
	f.mu.Lock()
	f.scenes["99"] = scene("99", "/data/tmp/worse.mp4", 1<<20)
	f.mu.Unlock()

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1", Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	gr := rep.Groups[0]
	if len(gr.Losers) != 1 {
		t.Fatalf("Losers = %+v", gr.Losers)
	}
	lr := gr.Losers[0]
	if !lr.Untagged {
		t.Error("the tag should still come off even when the move is refused")
	}
	if lr.MovedBack {
		t.Error("MovedBack = true, want the move refused")
	}
	if !containsSubstring(lr.Errs, "already has a file") {
		t.Errorf("Errs = %v, want a refusal about the occupied path", lr.Errs)
	}

	loser, _ := f.scene("11")
	if loser.Files[0].Path != "/data/_quarantine/worse.mp4" {
		t.Errorf("loser path = %q, want it left in quarantine", loser.Files[0].Path)
	}
}

func TestUndoMovesSidecarsBack(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/tmp/worse.mp4", 2<<30, withTitle("The Real Title")),
	}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	seedGroups(t, l, planned{"sig-1", "10", scenes})

	mem := vfs.NewMem("/data/tmp/worse.mp4", "/data/tmp/worse.en.srt")
	mover := memMover(t, mem)

	if _, err := Apply(t.Context(), c, l, Options{Commit: true, BackupDir: t.TempDir(), Sidecars: mover}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !slices.Contains(mem.Renames, "/data/tmp/worse.en.srt -> /data/_quarantine/worse.en.srt") {
		t.Fatalf("renames after apply = %v", mem.Renames)
	}

	rep, err := Undo(t.Context(), c, l, UndoOptions{Signature: "sig-1", Commit: true, Sidecars: mover})
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	side := rep.Groups[0].Losers[0].Sidecars
	if len(side) != 1 || !side[0].Done {
		t.Fatalf("sidecars = %+v, want one moved back", side)
	}
	if side[0].To != "/data/tmp/worse.en.srt" {
		t.Errorf("sidecar destination = %q", side[0].To)
	}
	if !slices.Contains(mem.Renames, "/data/_quarantine/worse.en.srt -> /data/tmp/worse.en.srt") {
		t.Errorf("renames after undo = %v", mem.Renames)
	}
	if !slices.Contains(mutationKinds(t, l), "undo:sidecar") {
		t.Errorf("mutation log = %v, want an undo:sidecar row", mutationKinds(t, l))
	}
}

func TestUndoReportStringCommitAndDryRun(t *testing.T) {
	rep := UndoReport{
		DryRun: true,
		Groups: []UndoGroupReport{
			{Signature: "sig-skip", Skipped: true, Reason: "verdict is \"reclaimed\", not quarantined — refusing to undo"},
			{
				Signature: "sig-1", Keeper: "10",
				Losers: []UndoLoserReport{{
					SceneID: "11", From: "/data/_quarantine/worse.mp4", To: "/data/tmp/worse.mp4",
					Sidecars: []SidecarMove{{From: "/data/_quarantine/worse.en.srt", To: "/data/tmp/worse.en.srt"}},
					Notes:    []string{"file sits somewhere unexpected"},
				}},
				KeeperFields:    []string{"title"},
				KeeperNotes:     []string{"director: edited since apply — left alone"},
				RestoredVerdict: ledger.VerdictCertain,
			},
		},
	}
	out := rep.String()
	for _, want := range []string{
		"DRY RUN", "SKIPPED", "refusing to undo",
		"loser 11", "would remove", "/data/_quarantine/worse.mp4 → /data/tmp/worse.mp4",
		"sidecar:", "note:", "would restore title", "director: edited since apply",
		"would restore to certain", "Re-run with --commit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run String() is missing %q:\n%s", want, out)
		}
	}

	rep.DryRun = false
	rep.Groups[1].Done = true
	rep.Groups[1].Losers[0].Untagged = true
	rep.Groups[1].Losers[0].MovedBack = true
	rep.Groups[1].Losers[0].Errs = []string{"boom"}
	out = rep.String()
	for _, want := range []string{"removed\n", "restored title", "restored to certain", "ERROR:   boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("commit String() is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "DRY RUN") {
		t.Error("a committed report should not say DRY RUN")
	}
}

// undoMutationRows returns the mutation log's undo:* rows, for asserting an
// undo run wrote nothing when it should not have.
func undoMutationRows(t *testing.T, l *ledger.Ledger) []ledger.Mutation {
	t.Helper()
	entries, err := l.Mutations(t.Context(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	var undoRows []ledger.Mutation
	for _, m := range entries {
		if strings.HasPrefix(m.Kind, "undo:") {
			undoRows = append(undoRows, m)
		}
	}
	return undoRows
}
