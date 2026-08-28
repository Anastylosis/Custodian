package reclaim

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/evidence"
	"github.com/Anastylosis/Custodian/internal/keeper"
	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/mirror"
	stash "github.com/Anastylosis/stash-go"
)

// Integration test in this file talks to a real Stash instance and only runs
// when SFX_INT_STASH_URL is set in the environment. Without that env var it
// is skipped, so `go test ./...` stays hermetic. SFX_INT_STASH_SEEDED=1 turns
// a missing fixture into a failure instead of a skip — see
// internal/stash/integration_test.go for the full explanation; this copies
// the same tiny gate since the two aren't in the same package.
//
// Unlike its siblings in internal/mirror and internal/evidence, this test
// moves and deletes files: it runs Apply and Prune with Commit=true against
// the harness's Stash, which physically rewrites the fixture library on
// disk. It additionally requires SFX_INT_STASH_WRITABLE=1, so a plain
// SFX_INT_STASH_URL export against someone's read-only or production Stash
// does not accidentally quarantine and delete real files — that flag is the
// operator saying "yes, this Stash's library is a throwaway I can mutate."
func integrationClient(t *testing.T) *stash.Client {
	t.Helper()
	url := os.Getenv("SFX_INT_STASH_URL")
	if url == "" {
		t.Skip("SFX_INT_STASH_URL not set; skipping integration test")
	}
	if os.Getenv("SFX_INT_STASH_WRITABLE") != "1" {
		t.Skip("SFX_INT_STASH_WRITABLE not set to 1; skipping — this test moves and deletes files")
	}
	return stash.NewClient(url)
}

func requireSeeded(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if ok {
		return
	}
	if os.Getenv("SFX_INT_STASH_SEEDED") == "1" {
		t.Fatalf("SFX_INT_STASH_SEEDED=1 but "+format, args...)
	}
	t.Skipf(format, args...)
}

// TestIntegrationUndoThenMultifileApplyAndPrune is the second end-to-end
// run against the seeded harness, covering the two things
// TestIntegrationApplyThenPrune does not: Undo, and the multifile shape
// (REBUILD.md's Phase 5b).
//
// It runs BEFORE TestIntegrationApplyThenPrune in this file (Go runs a
// file's tests in declaration order) deliberately: the fixture seeds
// exactly one stashdb.org duplicate pair, and TestIntegrationApplyThenPrune
// fully reclaims it (merge + delete — nothing left to re-discover
// afterwards). Phase A here applies that same pair, undoes it, and stops —
// leaving it exactly as fresh as `custodian sync` found it, tag off, file
// back, keeper fields unchanged — so TestIntegrationApplyThenPrune still
// finds its one certain group and reclaims it independently, unaffected by
// having run second. Phase B then does apply-and-prune to `reclaimed` on
// the OTHER fixture duplicate — the multifile-take1/2.mp4 scene — which
// nothing else in this file touches, so there is no such handoff to make
// there: it starts and ends the run fully reclaimed.
func TestIntegrationUndoThenMultifileApplyAndPrune(t *testing.T) {
	client := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	t.Run("undo restores the stashdb pair to fresh", func(t *testing.T) {
		testUndoRestoresTheStashdbPairToFresh(ctx, t, client)
	})
	t.Run("multifile apply then prune reaches reclaimed", func(t *testing.T) {
		testMultifileApplyThenPruneReachesReclaimed(ctx, t, client)
	})
}

// testUndoRestoresTheStashdbPairToFresh is Phase A of
// TestIntegrationUndoThenMultifileApplyAndPrune, split out to keep each
// phase's cyclomatic complexity under the repo's gocyclo gate.
func testUndoRestoresTheStashdbPairToFresh(ctx context.Context, t *testing.T, client *stash.Client) {
	l := newLedger(t)
	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	summary, err := evidence.Run(ctx, l, []evidence.Generator{evidence.StashIDGenerator{}}, evidence.Policy{Tolerance: evidence.DefaultTolerance})
	if err != nil {
		t.Fatalf("evidence.Run: %v", err)
	}
	requireSeeded(t, summary.Certain >= 1, "summary.Certain = %d, want >= 1 (the stashdb.org duplicate pair)", summary.Certain)

	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	requireSeeded(t, len(groups) == 1, "certain groups = %d, want 1 (the stashdb.org duplicate pair)", len(groups))
	group := groups[0]

	files, err := l.PrimaryFiles(ctx, group.SceneIDs)
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}
	choice := keeper.Choose(group, files, keeper.DefaultRules())
	requireSeeded(t, len(choice.Losers) == 1, "keeper.Choose losers = %v, want exactly 1", choice.Losers)
	if err := l.SetChoice(ctx, group.Signature, choice.Keeper, choice.Losers, choice.Steps); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}
	keeperID, loserID := choice.Keeper, choice.Losers[0]

	// Captured before Apply touches anything, so undo's restoration can
	// be checked against ground truth rather than against Apply's own
	// (possibly buggy) idea of "before".
	keeperPre, found, err := client.FindScene(ctx, keeperID)
	if err != nil || !found {
		t.Fatalf("FindScene(keeper %s) before apply: found=%v err=%v", keeperID, found, err)
	}
	loserPre, found, err := client.FindScene(ctx, loserID)
	if err != nil || !found {
		t.Fatalf("FindScene(loser %s) before apply: found=%v err=%v", loserID, found, err)
	}
	loserOriginalPath := loserPre.PrimaryFile().Path

	applyOpts := Options{QuarantineDir: "/library/_quarantine", BackupDir: t.TempDir(), Commit: true}
	applyRep, err := Apply(ctx, client, l, applyOpts)
	if err != nil {
		t.Fatalf("Apply: %v\n%s", err, applyRep.String())
	}
	if done, _, failed, _ := applyRep.Counts(); done != 1 || failed != 0 {
		t.Fatalf("Apply Counts = done=%d failed=%d, want 1/0\n%s", done, failed, applyRep.String())
	}

	quarantined, found, err := client.FindScene(ctx, loserID)
	if err != nil || !found {
		t.Fatalf("FindScene(loser %s) after apply: found=%v err=%v", loserID, found, err)
	}
	if !hasTag(quarantined, DefaultTag) {
		t.Fatalf("loser %s tags = %v after apply, want %s", loserID, quarantined.Tags, DefaultTag)
	}
	if !strings.HasPrefix(quarantined.PrimaryFile().Path, applyOpts.QuarantineDir+"/") {
		t.Fatalf("loser %s file path = %s after apply, want it under %s/", loserID, quarantined.PrimaryFile().Path, applyOpts.QuarantineDir)
	}

	undoRep, err := Undo(ctx, client, l, UndoOptions{Signature: group.Signature, Commit: true})
	if err != nil {
		t.Fatalf("Undo: %v\n%s", err, undoRep.String())
	}
	t.Logf("Undo report:\n%s", undoRep.String())
	if done, skipped, failed := undoRep.Counts(); done != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("Undo Counts = done=%d skipped=%d failed=%d, want 1/0/0\n%s", done, skipped, failed, undoRep.String())
	}

	// Tag gone, file back at its original path.
	restored, found, err := client.FindScene(ctx, loserID)
	if err != nil || !found {
		t.Fatalf("FindScene(loser %s) after undo: found=%v err=%v", loserID, found, err)
	}
	if hasTag(restored, DefaultTag) {
		t.Errorf("loser %s still carries %s after undo: %v", loserID, DefaultTag, restored.Tags)
	}
	if restored.PrimaryFile().Path != loserOriginalPath {
		t.Errorf("loser %s file path = %s after undo, want %s", loserID, restored.PrimaryFile().Path, loserOriginalPath)
	}
	if hostDir := os.Getenv("SFX_INT_STASH_LIBRARY_DIR"); hostDir != "" {
		hostPath := filepath.Join(hostDir, strings.TrimPrefix(loserOriginalPath, "/library/"))
		if _, err := os.Stat(hostPath); err != nil {
			t.Errorf("loser file not found on the host at %s after undo: %v", hostPath, err)
		}
	}

	// Keeper fields as before: whatever union carried over (nothing, if
	// the fixture pair already agreed on every field) came back off.
	keeperPost, found, err := client.FindScene(ctx, keeperID)
	if err != nil || !found {
		t.Fatalf("FindScene(keeper %s) after undo: found=%v err=%v", keeperID, found, err)
	}
	if keeperPost.Title != keeperPre.Title || keeperPost.Director != keeperPre.Director || keeperPost.Date != keeperPre.Date {
		t.Errorf("keeper %s fields after undo = %+v, want them back at %+v",
			keeperID, struct{ Title, Director, Date string }{keeperPost.Title, keeperPost.Director, keeperPost.Date},
			struct{ Title, Director, Date string }{keeperPre.Title, keeperPre.Director, keeperPre.Date})
	}

	// Ledger verdict restored.
	fresh, found, err := l.GroupBySignature(ctx, group.Signature)
	if err != nil {
		t.Fatalf("GroupBySignature: %v", err)
	}
	requireSeeded(t, found && fresh.Verdict == ledger.VerdictCertain,
		"group %s verdict after undo = %+v, want certain", group.Signature, fresh)

	// Every undo:* mutation this run wrote is ok.
	assertRunMutations(t, l, undoRep.RunID, []string{"undo:tag", "undo:move"}, map[string]bool{
		"undo:tag": true, "undo:move": true,
	})

	logAllMutations(t, l)
}

// testMultifileApplyThenPruneReachesReclaimed is Phase B of
// TestIntegrationUndoThenMultifileApplyAndPrune: the multifile-take1/2.mp4
// scene, untouched by Phase A or by TestIntegrationApplyThenPrune, taken
// from `certain` through `quarantined` to `reclaimed`.
func testMultifileApplyThenPruneReachesReclaimed(ctx context.Context, t *testing.T, client *stash.Client) {
	l := newLedger(t)
	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	summary, err := evidence.Run(ctx, l, []evidence.Generator{evidence.MultifileGenerator{}}, evidence.Policy{Tolerance: evidence.DefaultTolerance})
	if err != nil {
		t.Fatalf("evidence.Run: %v", err)
	}
	requireSeeded(t, summary.Certain >= 1, "summary.Certain = %d, want >= 1 (the multifile-take1/2.mp4 scene)", summary.Certain)

	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	requireSeeded(t, len(groups) == 1, "certain groups = %d, want 1 (the multifile-take1/2.mp4 scene)", len(groups))
	group := groups[0]
	requireSeeded(t, len(group.SceneIDs) == 1, "multifile group scenes = %v, want exactly 1", group.SceneIDs)
	sceneID := group.SceneIDs[0]

	scenePre, found, err := client.FindScene(ctx, sceneID)
	if err != nil || !found {
		t.Fatalf("FindScene(%s) before apply: found=%v err=%v", sceneID, found, err)
	}
	requireSeeded(t, len(scenePre.Files) == 2, "scene %s Files = %d before apply, want 2", sceneID, len(scenePre.Files))

	applyOpts := Options{QuarantineDir: "/library/_quarantine", BackupDir: t.TempDir(), Commit: true}
	applyRep, err := Apply(ctx, client, l, applyOpts)
	if err != nil {
		t.Fatalf("Apply: %v\n%s", err, applyRep.String())
	}
	t.Logf("Apply report:\n%s", applyRep.String())

	mdone, mskipped, mfailed, _ := applyRep.MultifileCounts()
	if mdone != 1 || mskipped != 0 || mfailed != 0 {
		t.Fatalf("Apply MultifileCounts = done=%d skipped=%d failed=%d, want 1/0/0\n%s", mdone, mskipped, mfailed, applyRep.String())
	}
	mr := applyRep.Multifiles[0]
	if len(mr.Moved) != 1 {
		t.Fatalf("Moved = %+v, want exactly one file quarantined", mr.Moved)
	}
	movedFile := mr.Moved[0]

	afterApply, found, err := client.FindScene(ctx, sceneID)
	if err != nil || !found {
		t.Fatalf("FindScene(%s) after apply: found=%v err=%v", sceneID, found, err)
	}
	if afterApply.PrimaryFile().ID != mr.Kept {
		t.Errorf("primary after apply = %s, want %s", afterApply.PrimaryFile().ID, mr.Kept)
	}
	var quarantinedFile *stash.File
	for i := range afterApply.Files {
		if afterApply.Files[i].ID == movedFile.FileID {
			quarantinedFile = &afterApply.Files[i]
		}
	}
	if quarantinedFile == nil || !strings.HasPrefix(quarantinedFile.Path, applyOpts.QuarantineDir+"/") {
		t.Fatalf("moved file after apply = %+v, want it under %s/", quarantinedFile, applyOpts.QuarantineDir)
	}

	assertRunMutations(t, l, applyRep.RunID, []string{"backup", "move"}, map[string]bool{
		"backup": true, "move": true,
	})

	quarantinedGroups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictQuarantined})
	if err != nil {
		t.Fatalf("Groups(quarantined): %v", err)
	}
	requireSeeded(t, len(quarantinedGroups) == 1, "quarantined groups = %d, want 1", len(quarantinedGroups))

	time.Sleep(2100 * time.Millisecond)
	pruneOpts := PruneOptions{Age: time.Nanosecond, BackupDir: t.TempDir(), Commit: true}
	pruneRep, err := Prune(ctx, client, l, pruneOpts)
	if err != nil {
		t.Fatalf("Prune: %v\n%s", err, pruneRep.String())
	}
	t.Logf("Prune report:\n%s", pruneRep.String())

	pdone, pskipped, pfailed, _ := pruneRep.Counts()
	if pdone != 1 || pskipped != 0 || pfailed != 0 {
		t.Fatalf("Prune Counts = done=%d skipped=%d failed=%d, want 1/0/0\n%s", pdone, pskipped, pfailed, pruneRep.String())
	}
	if !pruneRep.Groups[0].Multifile {
		t.Error("PruneGroup.Multifile = false, want true")
	}

	afterPrune, found, err := client.FindScene(ctx, sceneID)
	if err != nil || !found {
		t.Fatalf("FindScene(%s) after prune: found=%v err=%v", sceneID, found, err)
	}
	if len(afterPrune.Files) != 1 {
		t.Errorf("scene %s Files = %d after prune, want 1", sceneID, len(afterPrune.Files))
	}
	if afterPrune.Files[0].ID != mr.Kept {
		t.Errorf("scene %s's one remaining file = %s after prune, want %s", sceneID, afterPrune.Files[0].ID, mr.Kept)
	}

	if hostDir := os.Getenv("SFX_INT_STASH_LIBRARY_DIR"); hostDir != "" {
		hostPath := filepath.Join(hostDir, strings.TrimPrefix(quarantinedFile.Path, "/library/"))
		if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
			t.Errorf("quarantined file at %s (err=%v), want it gone from disk", hostPath, err)
		}
	} else {
		t.Log("SFX_INT_STASH_LIBRARY_DIR not set; skipping the host-filesystem check that the file is physically gone")
	}

	assertRunMutations(t, l, pruneRep.RunID, []string{"backup", "delete"}, map[string]bool{
		"backup": true, "delete": true,
	})

	reclaimed, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictReclaimed})
	if err != nil {
		t.Fatalf("Groups(reclaimed): %v", err)
	}
	requireSeeded(t, len(reclaimed) == 1, "reclaimed groups = %d, want 1", len(reclaimed))

	logAllMutations(t, l)
}

// TestIntegrationApplyThenPrune is the first true end-to-end run of Apply and
// Prune with Commit=true: it plans the stashdb.org duplicate pair the way
// `custodian sync && custodian plan` would, quarantines the loser, checks
// nothing moves twice, then reclaims it for real. It never re-seeds the
// harness — the fixture library it mutates is meant to be thrown away
// afterwards (`run.sh down`).
//
// The rescued-loser path (an operator un-tags a quarantined scene before
// prune runs) is not exercised here: reseeding a fresh Stash mid-test just to
// cover that one branch is too slow for what it buys, and
// TestPruneSkipsARescuedLoser in prune_test.go already covers it against a
// fake server.
func TestIntegrationApplyThenPrune(t *testing.T) {
	client := integrationClient(t)
	l := newLedger(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// --- 1. sync -> evidence.Run -> keeper.Choose+SetChoice, mirroring what
	// `custodian plan` does (internal/cli/plan.go's `choose`) without
	// importing internal/cli. ------------------------------------------
	if _, err := mirror.Sync(ctx, client, l, mirror.Options{Full: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	summary, err := evidence.Run(ctx, l, []evidence.Generator{evidence.StashIDGenerator{}}, evidence.Policy{Tolerance: evidence.DefaultTolerance})
	if err != nil {
		t.Fatalf("evidence.Run: %v", err)
	}
	requireSeeded(t, summary.Certain >= 1, "summary.Certain = %d, want >= 1 (the stashdb.org duplicate pair)", summary.Certain)

	groups, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictCertain})
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	requireSeeded(t, len(groups) == 1, "certain groups = %d, want 1 (the stashdb.org duplicate pair)", len(groups))
	group := groups[0]

	files, err := l.PrimaryFiles(ctx, group.SceneIDs)
	if err != nil {
		t.Fatalf("PrimaryFiles: %v", err)
	}
	choice := keeper.Choose(group, files, keeper.DefaultRules())
	requireSeeded(t, len(choice.Losers) == 1, "keeper.Choose losers = %v, want exactly 1", choice.Losers)
	if err := l.SetChoice(ctx, group.Signature, choice.Keeper, choice.Losers, choice.Steps); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}
	keeperID, loserID := choice.Keeper, choice.Losers[0]
	t.Logf("planned: keeper=%s loser=%s (decided by %s)", keeperID, loserID, choice.Steps[len(choice.Steps)-1].Rule)

	// --- 2. Apply, Commit=true. ------------------------------------------
	//
	// Finding: Stash's moveFiles does NOT require the destination folder to
	// exist beforehand. Verified by hand against this same harness before
	// writing this test: moveFiles into a brand new /library/_quarantine
	// (absent from the host's stash-library dir) succeeded, and Stash
	// created the directory itself (owned by the container's root, since
	// Stash runs as root in the image) as part of the move. So neither this
	// test nor a real deployment needs to pre-create QuarantineDir on the
	// host — it only has to be a path inside a configured library root.
	backupDir1 := t.TempDir()
	applyOpts := Options{
		QuarantineDir: "/library/_quarantine",
		BackupDir:     backupDir1,
		Commit:        true,
	}
	rep, err := Apply(ctx, client, l, applyOpts)
	if err != nil {
		t.Fatalf("Apply: %v\n%s", err, rep.String())
	}
	t.Logf("Apply report:\n%s", rep.String())

	done, skipped, failed, _ := rep.Counts()
	if done != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("Apply Counts = done=%d skipped=%d failed=%d, want 1/0/0\n%s", done, skipped, failed, rep.String())
	}

	// The loser now carries the quarantine tag and its file lives under
	// QuarantineDir.
	loserScene, found, err := client.FindScene(ctx, loserID)
	if err != nil {
		t.Fatalf("FindScene(loser %s): %v", loserID, err)
	}
	if !found {
		t.Fatalf("FindScene(loser %s): not found after Apply", loserID)
	}
	if !hasTag(loserScene, DefaultTag) {
		t.Errorf("loser %s tags = %v, want %s", loserID, loserScene.Tags, DefaultTag)
	}
	loserFile := loserScene.PrimaryFile()
	if loserFile == nil {
		t.Fatalf("loser %s has no file after Apply", loserID)
	}
	if !strings.HasPrefix(loserFile.Path, applyOpts.QuarantineDir+"/") {
		t.Errorf("loser %s file path = %s, want it under %s/", loserID, loserFile.Path, applyOpts.QuarantineDir)
	}

	// The keeper's own metadata is intact: still carries the shared
	// stash_id, and the union either changed nothing (both fixture clips
	// already agree on every unioned field) or wrote it back unharmed.
	keeperScene, found, err := client.FindScene(ctx, keeperID)
	if err != nil {
		t.Fatalf("FindScene(keeper %s): %v", keeperID, err)
	}
	if !found {
		t.Fatalf("FindScene(keeper %s): not found after Apply", keeperID)
	}
	if len(keeperScene.StashIDs) == 0 {
		t.Errorf("keeper %s StashIDs = %v, want the shared stashdb.org id intact", keeperID, keeperScene.StashIDs)
	}

	// The backup file and its manifest exist, and the manifest says the
	// server it backed up had all 3 fixture scenes.
	if !rep.Backup.Taken {
		t.Fatalf("Backup.Taken = false, want true; Backup.Err = %s", rep.Backup.Err)
	}
	if _, err := os.Stat(rep.Backup.File); err != nil {
		t.Errorf("backup file %s: %v", rep.Backup.File, err)
	}
	manifestBytes, err := os.ReadFile(rep.Backup.Manifest)
	if err != nil {
		t.Fatalf("reading manifest %s: %v", rep.Backup.Manifest, err)
	}
	var manifest stash.BackupManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parsing manifest %s: %v", rep.Backup.Manifest, err)
	}
	requireSeeded(t, manifest.Server.SceneCount == 3,
		"manifest Server.SceneCount = %d, want 3", manifest.Server.SceneCount)

	// The mutation log for this run shows backup, an optional union, tag
	// and move — every one of them ok.
	assertRunMutations(t, l, rep.RunID, []string{"backup"}, map[string]bool{
		"backup": true, "union": true, "tag": true, "move": true,
	})

	quarantined, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictQuarantined})
	if err != nil {
		t.Fatalf("Groups(quarantined): %v", err)
	}
	requireSeeded(t, len(quarantined) == 1, "quarantined groups = %d, want 1", len(quarantined))

	// --- 3. Apply again: nothing left at verdict `certain`, so it is a
	// no-op — the idempotency Apply promises. -----------------------------
	rep2, err := Apply(ctx, client, l, applyOpts)
	if err != nil {
		t.Fatalf("Apply (second run): %v", err)
	}
	if len(rep2.Groups) != 0 {
		t.Errorf("second Apply Groups = %+v, want none (already quarantined)", rep2.Groups)
	}

	// --- 4. Prune, Commit=true. -------------------------------------------
	//
	// PruneOptions.Age == 0 is not "no age filter" — withDefaults maps it to
	// DefaultAge (30 days), which would skip a group quarantined seconds
	// ago. AppliedBefore is compared against the *applying run's*
	// started_at, stored with second precision (time.RFC3339, no fractional
	// seconds) — so a near-zero Age computed within the same wall-clock
	// second as Apply's BeginRun would still fail the `<` comparison. A
	// short, real sleep across a second boundary plus an explicit,
	// deliberately tiny (but non-zero) Age sidesteps both problems.
	time.Sleep(2100 * time.Millisecond)
	backupDir2 := t.TempDir()
	pruneOpts := PruneOptions{
		Age:       time.Nanosecond,
		BackupDir: backupDir2,
		Commit:    true,
	}
	prep, err := Prune(ctx, client, l, pruneOpts)
	if err != nil {
		t.Fatalf("Prune: %v\n%s", err, prep.String())
	}
	t.Logf("Prune report:\n%s", prep.String())

	pdone, pskipped, pfailed, _ := prep.Counts()
	if pdone != 1 || pskipped != 0 || pfailed != 0 {
		t.Fatalf("Prune Counts = done=%d skipped=%d failed=%d, want 1/0/0\n%s", pdone, pskipped, pfailed, prep.String())
	}

	// The loser scene is gone entirely (sceneMerge folded it into the
	// keeper and deleted the source scene).
	_, found, err = client.FindScene(ctx, loserID)
	if err != nil {
		t.Fatalf("FindScene(loser %s) after Prune: %v", loserID, err)
	}
	if found {
		t.Errorf("loser scene %s still exists after Prune", loserID)
	}

	// The keeper holds exactly its own file: the merge attached the loser's
	// file to it, and deleteFiles then removed that file (both from Stash
	// and from disk).
	keeperScene2, found, err := client.FindScene(ctx, keeperID)
	if err != nil {
		t.Fatalf("FindScene(keeper %s) after Prune: %v", keeperID, err)
	}
	if !found {
		t.Fatalf("FindScene(keeper %s): not found after Prune", keeperID)
	}
	if len(keeperScene2.Files) != 1 {
		t.Errorf("keeper %s Files = %d, want 1 after Prune", keeperID, len(keeperScene2.Files))
	}

	// The quarantined video is physically gone from the host library dir.
	// This needs the host-side path the container's /library is bind-mounted
	// from; SFX_INT_STASH_LIBRARY_DIR carries it when the caller can supply
	// it (the local harness and CI both can — see ci.yml). Without it, this
	// one check is skipped rather than failed: everything else already
	// proves the file is gone from Stash's own bookkeeping.
	if hostDir := os.Getenv("SFX_INT_STASH_LIBRARY_DIR"); hostDir != "" {
		hostPath := filepath.Join(hostDir, strings.TrimPrefix(loserFile.Path, "/library/"))
		if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
			t.Errorf("quarantined file at %s (err=%v), want it gone from disk", hostPath, err)
		}
	} else {
		t.Log("SFX_INT_STASH_LIBRARY_DIR not set; skipping the host-filesystem check that the file is physically gone")
	}

	assertRunMutations(t, l, prep.RunID, []string{"backup", "merge", "delete"}, map[string]bool{
		"backup": true, "merge": true, "delete": true,
	})

	reclaimed, err := l.Groups(ctx, ledger.GroupFilter{Verdict: ledger.VerdictReclaimed})
	if err != nil {
		t.Fatalf("Groups(reclaimed): %v", err)
	}
	requireSeeded(t, len(reclaimed) == 1, "reclaimed groups = %d, want 1", len(reclaimed))

	if !prep.Backup.Taken {
		t.Errorf("Prune Backup.Taken = false, want true; Backup.Err = %s", prep.Backup.Err)
	}
	if prep.Backup.File == rep.Backup.File {
		t.Errorf("Prune backup file %s reused Apply's, want a second, independent backup", prep.Backup.File)
	}

	logAllMutations(t, l)
}

// assertRunMutations fails the test unless every kind in want was seen for
// runID with ok == true, and every kind in required was seen at all (want's
// keys minus required are optional — e.g. "union", which the fixture pair
// may or may not actually need).
func assertRunMutations(t *testing.T, l *ledger.Ledger, runID int64, required []string, want map[string]bool) {
	t.Helper()
	ctx := t.Context()
	entries, err := l.Mutations(ctx, 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}

	seen := map[string]bool{}
	okByKind := map[string]bool{}
	for _, m := range entries {
		if m.RunID != runID {
			continue
		}
		seen[m.Kind] = true
		if m.OK == nil || !*m.OK {
			okByKind[m.Kind] = false
			t.Errorf("mutation %s (id %d, run %d) ok = %v, note = %q", m.Kind, m.ID, runID, m.OK, m.Note)
		} else if _, already := okByKind[m.Kind]; !already {
			okByKind[m.Kind] = true
		}
	}

	for _, kind := range required {
		if !seen[kind] {
			t.Errorf("run %d has no %s mutation, want one", runID, kind)
		}
	}
	for kind := range want {
		if seen[kind] && !okByKind[kind] {
			t.Errorf("run %d mutation %s was not ok", runID, kind)
		}
	}
}

// logAllMutations prints the full mutation log, oldest first, the way
// `custodian log` would show it — the final piece of the integration
// report.
func logAllMutations(t *testing.T, l *ledger.Ledger) {
	t.Helper()
	entries, err := l.Mutations(t.Context(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	t.Log("=== custodian log ===")
	for i := len(entries) - 1; i >= 0; i-- {
		m := entries[i]
		ok := "?"
		if m.OK != nil {
			if *m.OK {
				ok = "ok"
			} else {
				ok = "FAIL"
			}
		}
		t.Logf("run=%d %-7s %-6s target=%-20s from=%-40s to=%-40s note=%s",
			m.RunID, m.Kind, ok, m.Target, m.From, m.To, m.Note)
	}
}
