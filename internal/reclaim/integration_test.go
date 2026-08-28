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
