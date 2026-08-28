package ledger

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func newTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func rating(v int) *int { return &v }

func twoScenes() []stash.Scene {
	return []stash.Scene{
		{
			ID: "1", Title: "First", Code: "ABC-1", Date: "2024-01-01",
			Studio:     &stash.Studio{ID: "s1", Name: "Studio One"},
			Organized:  true,
			Rating100:  rating(80),
			PlayCount:  2,
			Performers: []stash.Performer{{ID: "p1", Name: "Alice"}, {ID: "p2", Name: "Bob"}},
			Tags:       []stash.Tag{{ID: "t1", Name: "tag-a"}, {ID: "t2", Name: "tag-b"}},
			URLs:       []string{"https://example/1"},
			StashIDs:   []stash.StashID{{Endpoint: "https://sb", ID: "uuid-1"}},
			Files: []stash.File{
				{
					ID: "f1", Basename: "a.mp4", Path: "/lib/a.mp4", Size: 100, Width: 1920, Height: 1080,
					Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "hash1"}, {Type: "phash", Value: "phash1"}},
				},
				{
					ID: "f2", Basename: "a.dup.mp4", Path: "/lib/a.dup.mp4", Size: 90, Width: 1280, Height: 720,
					Fingerprints: []stash.Fingerprint{{Type: "oshash", Value: "hash2"}},
				},
			},
			UpdatedAt: "2024-01-01 00:00:00",
		},
		{
			ID: "2", Title: "Second", Date: "2024-02-02",
			Files: []stash.File{
				{ID: "f3", Basename: "b.mp4", Path: "/lib/b.mp4", Size: 50},
			},
			UpdatedAt: "2024-02-02 00:00:00",
		},
	}
}

func TestOpenRejectsAnUnwritablePath(t *testing.T) {
	// A path inside a nonexistent directory: SQLite can't create the file.
	_, err := Open(filepath.Join(t.TempDir(), "no-such-dir", "x.sqlite"))
	if err == nil {
		t.Fatal("Open into a nonexistent directory should fail")
	}
}

func TestUpsertScenesWritesChildRows(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	var performerCount, tagCount, urlCount, stashIDCount int
	for _, q := range []struct {
		query string
		dst   *int
	}{
		{`SELECT COUNT(*) FROM scene_performer WHERE scene_id = '1'`, &performerCount},
		{`SELECT COUNT(*) FROM scene_tag WHERE scene_id = '1'`, &tagCount},
		{`SELECT COUNT(*) FROM scene_url WHERE scene_id = '1'`, &urlCount},
		{`SELECT COUNT(*) FROM scene_stash_id WHERE scene_id = '1'`, &stashIDCount},
	} {
		if err := l.DB().QueryRow(q.query).Scan(q.dst); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	if performerCount != 2 || tagCount != 2 || urlCount != 1 || stashIDCount != 1 {
		t.Errorf("child rows = performers:%d tags:%d urls:%d stash_ids:%d, want 2,2,1,1",
			performerCount, tagCount, urlCount, stashIDCount)
	}

	var fileCount int
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM file WHERE scene_id = '1'`).Scan(&fileCount); err != nil {
		t.Fatalf("counting files: %v", err)
	}
	if fileCount != 2 {
		t.Errorf("file count = %d, want 2", fileCount)
	}

	var isPrimary int
	var oshash, phash string
	if err := l.DB().QueryRow(`SELECT is_primary, oshash, phash FROM file WHERE id = 'f1'`).
		Scan(&isPrimary, &oshash, &phash); err != nil {
		t.Fatalf("reading file f1: %v", err)
	}
	if isPrimary != 1 || oshash != "hash1" || phash != "phash1" {
		t.Errorf("f1 = is_primary:%d oshash:%q phash:%q, want 1,hash1,phash1", isPrimary, oshash, phash)
	}

	var f2Primary int
	var f2Phash *string
	if err := l.DB().QueryRow(`SELECT is_primary, phash FROM file WHERE id = 'f2'`).
		Scan(&f2Primary, &f2Phash); err != nil {
		t.Fatalf("reading file f2: %v", err)
	}
	if f2Primary != 0 {
		t.Errorf("f2 is_primary = %d, want 0", f2Primary)
	}
	if f2Phash != nil {
		t.Errorf("f2 phash = %v, want NULL (no phash fingerprint present)", *f2Phash)
	}

	// f3 (scene 2) carries no fingerprints at all.
	var f3OSHash, f3MD5, f3Phash *string
	if err := l.DB().QueryRow(`SELECT oshash, md5, phash FROM file WHERE id = 'f3'`).
		Scan(&f3OSHash, &f3MD5, &f3Phash); err != nil {
		t.Fatalf("reading file f3: %v", err)
	}
	if f3OSHash != nil || f3MD5 != nil || f3Phash != nil {
		t.Errorf("f3 fingerprints = %v,%v,%v, want all NULL", f3OSHash, f3MD5, f3Phash)
	}
}

func TestUpsertScenesReplacesChildrenWholesale(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes (first): %v", err)
	}

	// Re-upsert scene 1 with a changed title and fewer tags.
	changed := []stash.Scene{
		{
			ID: "1", Title: "First (retitled)",
			Tags:  []stash.Tag{{ID: "t1", Name: "tag-a"}},
			Files: []stash.File{{ID: "f1", Basename: "a.mp4", Path: "/lib/a.mp4"}},
		},
	}
	if err := l.UpsertScenes(ctx, runID, changed); err != nil {
		t.Fatalf("UpsertScenes (second): %v", err)
	}

	var title string
	if err := l.DB().QueryRow(`SELECT title FROM scene WHERE id = '1'`).Scan(&title); err != nil {
		t.Fatalf("reading title: %v", err)
	}
	if title != "First (retitled)" {
		t.Errorf("title = %q, want %q", title, "First (retitled)")
	}

	var tagCount, performerCount int
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM scene_tag WHERE scene_id = '1'`).Scan(&tagCount); err != nil {
		t.Fatalf("counting tags: %v", err)
	}
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM scene_performer WHERE scene_id = '1'`).Scan(&performerCount); err != nil {
		t.Fatalf("counting performers: %v", err)
	}
	if tagCount != 1 {
		t.Errorf("tag count = %d, want 1", tagCount)
	}
	if performerCount != 0 {
		t.Errorf("performer count = %d, want 0 (replaced wholesale, none this time)", performerCount)
	}
}

func TestUpsertScenesEmptyIsANoop(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, nil); err != nil {
		t.Fatalf("UpsertScenes(nil): %v", err)
	}
}

func TestFullRunTombstonesUnseenScene(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	scenesGone, filesGone, err := l.TombstoneUnseen(ctx, run1)
	if err != nil {
		t.Fatalf("TombstoneUnseen after run1: %v", err)
	}
	if scenesGone != 0 || filesGone != 0 {
		t.Errorf("TombstoneUnseen right after seeding = %d,%d, want 0,0 (nothing unseen yet)", scenesGone, filesGone)
	}

	// Second full run only sees scene 1 — scene 2 vanished from Stash.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	only1 := twoScenes()[:1]
	if err := l.UpsertScenes(ctx, run2, only1); err != nil {
		t.Fatalf("UpsertScenes run2: %v", err)
	}
	scenesGone, filesGone, err = l.TombstoneUnseen(ctx, run2)
	if err != nil {
		t.Fatalf("TombstoneUnseen after run2: %v", err)
	}
	if scenesGone != 1 {
		t.Errorf("scenesGone = %d, want 1", scenesGone)
	}
	if filesGone != 1 {
		t.Errorf("filesGone = %d, want 1", filesGone)
	}

	var goneRun *int64
	if err := l.DB().QueryRow(`SELECT gone_run FROM scene WHERE id = '2'`).Scan(&goneRun); err != nil {
		t.Fatalf("reading gone_run: %v", err)
	}
	if goneRun == nil || *goneRun != run2 {
		t.Errorf("scene 2 gone_run = %v, want %d", goneRun, run2)
	}

	// Scene 1 should NOT be tombstoned — it was seen in run2.
	var scene1Gone *int64
	if err := l.DB().QueryRow(`SELECT gone_run FROM scene WHERE id = '1'`).Scan(&scene1Gone); err != nil {
		t.Fatalf("reading scene 1 gone_run: %v", err)
	}
	if scene1Gone != nil {
		t.Errorf("scene 1 gone_run = %v, want NULL", *scene1Gone)
	}

	// A third TombstoneUnseen call for the same run finds nothing new.
	scenesGone, filesGone, err = l.TombstoneUnseen(ctx, run2)
	if err != nil {
		t.Fatalf("TombstoneUnseen (repeat): %v", err)
	}
	if scenesGone != 0 || filesGone != 0 {
		t.Errorf("repeat TombstoneUnseen = %d,%d, want 0,0", scenesGone, filesGone)
	}
}

func TestStatsOnEmptyLedger(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats != (Stats{}) {
		t.Errorf("Stats on an empty ledger = %+v, want the zero value", stats)
	}
}

func TestStats(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, run1, RunSummary{ScenesSeen: 2, FilesSeen: 3, StashVersion: "v0.31.1"}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ScenesLive != 2 {
		t.Errorf("ScenesLive = %d, want 2", stats.ScenesLive)
	}
	if stats.ScenesGone != 0 {
		t.Errorf("ScenesGone = %d, want 0", stats.ScenesGone)
	}
	if stats.FilesLive != 3 {
		t.Errorf("FilesLive = %d, want 3", stats.FilesLive)
	}
	if stats.ScenesWithMultipleFiles != 1 {
		t.Errorf("ScenesWithMultipleFiles = %d, want 1", stats.ScenesWithMultipleFiles)
	}
	if stats.ScenesWithStashID != 1 {
		t.Errorf("ScenesWithStashID = %d, want 1", stats.ScenesWithStashID)
	}
	if stats.ScenesWithoutStashID != 1 {
		t.Errorf("ScenesWithoutStashID = %d, want 1", stats.ScenesWithoutStashID)
	}
	if stats.BytesLive != 240 {
		t.Errorf("BytesLive = %d, want 240", stats.BytesLive)
	}
	if stats.FilesWithoutPhash != 2 {
		t.Errorf("FilesWithoutPhash = %d, want 2", stats.FilesWithoutPhash)
	}
	if stats.LastSync == nil || stats.LastSync.ID != run1 {
		t.Errorf("LastSync = %v, want run %d", stats.LastSync, run1)
	}
	if stats.LastScan != nil {
		t.Errorf("LastScan = %v, want nil (no scan run yet)", stats.LastScan)
	}
}

func TestStatsLastScanIgnoresSyncRuns(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	syncRun, err := l.BeginRun(ctx, "incremental", "2026-01-01")
	if err != nil {
		t.Fatalf("BeginRun (incremental): %v", err)
	}
	if err := l.FinishRun(ctx, syncRun, RunSummary{ScenesSeen: 1}); err != nil {
		t.Fatalf("FinishRun (incremental): %v", err)
	}
	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun (scan): %v", err)
	}
	if err := l.FinishRun(ctx, scanRun, RunSummary{Notes: "certain 1, conflicted 0, dismissed 0"}); err != nil {
		t.Fatalf("FinishRun (scan): %v", err)
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.LastSync == nil || stats.LastSync.ID != syncRun {
		t.Errorf("LastSync = %v, want run %d (the sync, not the later scan)", stats.LastSync, syncRun)
	}
	if stats.LastScan == nil || stats.LastScan.ID != scanRun {
		t.Errorf("LastScan = %v, want run %d", stats.LastScan, scanRun)
	}
}

func TestLastRunOfKindNoKinds(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	r, err := l.LastRunOfKind(ctx)
	if err != nil {
		t.Fatalf("LastRunOfKind(): %v", err)
	}
	if r != nil {
		t.Errorf("LastRunOfKind() = %v, want nil", r)
	}
}

func TestLastRunOfKindNoneYet(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	r, err := l.LastRunOfKind(ctx, "full", "incremental")
	if err != nil {
		t.Fatalf("LastRunOfKind: %v", err)
	}
	if r != nil {
		t.Errorf("LastRunOfKind = %v, want nil (nothing has run)", r)
	}
}

func TestNewestUpdatedAt(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	empty, err := l.NewestUpdatedAt(ctx)
	if err != nil {
		t.Fatalf("NewestUpdatedAt (empty ledger): %v", err)
	}
	if empty != "" {
		t.Errorf("NewestUpdatedAt on an empty ledger = %q, want empty", empty)
	}

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	newest, err := l.NewestUpdatedAt(ctx)
	if err != nil {
		t.Fatalf("NewestUpdatedAt: %v", err)
	}
	if newest != "2024-02-02 00:00:00" {
		t.Errorf("NewestUpdatedAt = %q, want %q", newest, "2024-02-02 00:00:00")
	}
}

func TestBeginFinishLastRunRoundtrip(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	if r, err := l.LastRun(ctx); err != nil || r != nil {
		t.Fatalf("LastRun on an empty ledger = %v, %v, want nil, nil", r, err)
	}

	runID, err := l.BeginRun(ctx, "incremental", "2024-01-01 00:00:00")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	r, err := l.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun (before finish): %v", err)
	}
	if r == nil || r.ID != runID || r.Kind != "incremental" || r.Since != "2024-01-01 00:00:00" {
		t.Fatalf("LastRun (before finish) = %+v", r)
	}
	if r.FinishedAt != "" {
		t.Errorf("FinishedAt before FinishRun = %q, want empty", r.FinishedAt)
	}

	if err := l.FinishRun(ctx, runID, RunSummary{
		ScenesSeen: 7, FilesSeen: 9, ScenesGone: 1, StashVersion: "v0.31.1", Notes: "ok",
	}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	r, err = l.LastRun(ctx)
	if err != nil {
		t.Fatalf("LastRun (after finish): %v", err)
	}
	if r.FinishedAt == "" {
		t.Error("FinishedAt after FinishRun is still empty")
	}
	if r.ScenesSeen != 7 || r.FilesSeen != 9 || r.ScenesGone != 1 || r.StashVersion != "v0.31.1" || r.Notes != "ok" {
		t.Errorf("LastRun (after finish) = %+v", r)
	}
}

func TestDecisionRoundtrip(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	_, found, err := l.GetDecision(ctx, "missing")
	if err != nil {
		t.Fatalf("GetDecision (missing): %v", err)
	}
	if found {
		t.Fatal("GetDecision found a decision that was never put")
	}

	d := Decision{Key: "hash1|hash2", Kind: "duplicate", Value: "certain", Keeper: "1", DecidedAt: "2024-01-01", Notes: "auto"}
	if err := l.PutDecision(ctx, d); err != nil {
		t.Fatalf("PutDecision: %v", err)
	}
	got, found, err := l.GetDecision(ctx, d.Key)
	if err != nil {
		t.Fatalf("GetDecision: %v", err)
	}
	if !found || got != d {
		t.Errorf("GetDecision = %+v, %v, want %+v, true", got, found, d)
	}

	d.Value = "rejected"
	if err := l.PutDecision(ctx, d); err != nil {
		t.Fatalf("PutDecision (overwrite): %v", err)
	}
	got, _, err = l.GetDecision(ctx, d.Key)
	if err != nil {
		t.Fatalf("GetDecision (after overwrite): %v", err)
	}
	if got.Value != "rejected" {
		t.Errorf("Value after overwrite = %q, want rejected", got.Value)
	}
}

func TestAppendMutation(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.AppendMutation(ctx, Mutation{
		RunID: runID, At: "2024-01-01", Kind: "move", Target: "f1", From: "/a", To: "/b",
	}); err != nil {
		t.Fatalf("AppendMutation: %v", err)
	}

	var count int
	if err := l.DB().QueryRow(`SELECT COUNT(*) FROM mutation_log WHERE run_id = ?`, runID).Scan(&count); err != nil {
		t.Fatalf("counting mutation_log: %v", err)
	}
	if count != 1 {
		t.Errorf("mutation_log rows = %d, want 1", count)
	}
}

func TestErrorsAreWrappedWhenTheLedgerIsClosed(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := l.BeginRun(ctx, "full", ""); err == nil {
		t.Error("BeginRun on a closed ledger should fail")
	}
	if err := l.FinishRun(ctx, 1, RunSummary{}); err == nil {
		t.Error("FinishRun on a closed ledger should fail")
	}
	if _, err := l.LastRun(ctx); err == nil {
		t.Error("LastRun on a closed ledger should fail")
	}
	if _, err := l.NewestUpdatedAt(ctx); err == nil {
		t.Error("NewestUpdatedAt on a closed ledger should fail")
	}
	if err := l.UpsertScenes(ctx, 1, twoScenes()); err == nil {
		t.Error("UpsertScenes on a closed ledger should fail")
	}
	if _, _, err := l.TombstoneUnseen(ctx, 1); err == nil {
		t.Error("TombstoneUnseen on a closed ledger should fail")
	}
	if _, err := l.Stats(ctx); err == nil {
		t.Error("Stats on a closed ledger should fail")
	}
	if err := l.PutDecision(ctx, Decision{Key: "x"}); err == nil {
		t.Error("PutDecision on a closed ledger should fail")
	}
	if _, _, err := l.GetDecision(ctx, "x"); err == nil {
		t.Error("GetDecision on a closed ledger should fail")
	}
	if err := l.AppendMutation(ctx, Mutation{}); err == nil {
		t.Error("AppendMutation on a closed ledger should fail")
	}
}

func TestUpsertScenesFailsWhenAChildTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE scene_tag`); err != nil {
		t.Fatalf("dropping scene_tag: %v", err)
	}

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err == nil {
		t.Error("UpsertScenes should fail when a child table is missing")
	}
}

func TestTombstoneUnseenFailsWhenFileTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE file`); err != nil {
		t.Fatalf("dropping file: %v", err)
	}

	nextRun, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, nextRun); err == nil {
		t.Error("TombstoneUnseen should fail when the file table is missing")
	}
}

func TestStatsFailsWhenFileTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE file`); err != nil {
		t.Fatalf("dropping file: %v", err)
	}
	if _, err := l.Stats(ctx); err == nil {
		t.Error("Stats should fail when the file table is missing")
	}
}

func TestOpenTwiceOnTheSamePathIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custodian.ledger.sqlite")
	l1, err := Open(path)
	if err != nil {
		t.Fatalf("Open (first): %v", err)
	}
	_ = l1.Close()

	l2, err := Open(path)
	if err != nil {
		t.Fatalf("Open (second): %v", err)
	}
	defer func() { _ = l2.Close() }()

	var version int
	if err := l2.DB().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestReplaceChildrenFailsOnForeignKeyViolation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		scene stash.Scene
	}{
		{"performer", stash.Scene{ID: "ghost", Performers: []stash.Performer{{ID: "p1", Name: "Nobody"}}}},
		{"tag", stash.Scene{ID: "ghost", Tags: []stash.Tag{{ID: "t1", Name: "Nothing"}}}},
		{"url", stash.Scene{ID: "ghost", URLs: []string{"https://example/x"}}},
		{"stash_id", stash.Scene{ID: "ghost", StashIDs: []stash.StashID{{Endpoint: "e", ID: "x"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newTestLedger(t)
			tx, err := l.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			defer func() { _ = tx.Rollback() }()

			stmts, err := prepareUpsertStatements(ctx, tx)
			if err != nil {
				t.Fatalf("prepareUpsertStatements: %v", err)
			}
			defer stmts.close()

			// Scene "ghost" was never inserted, so any child row referencing
			// it violates the foreign key.
			if err := stmts.replaceChildren(ctx, c.scene); err == nil {
				t.Errorf("replaceChildren(%s) should fail: scene %q was never inserted", c.name, c.scene.ID)
			}
		})
	}
}

func TestStatsFailsWhenStashIDTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE scene_stash_id`); err != nil {
		t.Fatalf("dropping scene_stash_id: %v", err)
	}
	if _, err := l.Stats(ctx); err == nil {
		t.Error("Stats should fail when scene_stash_id is missing")
	}
}

// schemaV2SQL is phase 2's schema.sql, frozen here so
// TestOpenMigratesAnExistingV2Ledger can build a database in the shape a
// pre-keeper ledger would actually be in — candidate_group with no keeper
// column, no candidate_step table — then verify Open migrates it forward.
const schemaV2SQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS scene (
  id          TEXT PRIMARY KEY,
  title       TEXT NOT NULL DEFAULT '',
  code        TEXT NOT NULL DEFAULT '',
  date        TEXT NOT NULL DEFAULT '',
  studio_id   TEXT NOT NULL DEFAULT '',
  studio_name TEXT NOT NULL DEFAULT '',
  organized   INTEGER NOT NULL DEFAULT 0,
  rating100   INTEGER,
  play_count  INTEGER NOT NULL DEFAULT 0,
  updated_at  TEXT NOT NULL DEFAULT '',
  seen_run    INTEGER NOT NULL,
  gone_run    INTEGER
);

CREATE TABLE IF NOT EXISTS file (
  id          TEXT PRIMARY KEY,
  scene_id    TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
  path        TEXT NOT NULL DEFAULT '',
  basename    TEXT NOT NULL DEFAULT '',
  size        INTEGER NOT NULL DEFAULT 0,
  mod_time    TEXT NOT NULL DEFAULT '',
  width       INTEGER NOT NULL DEFAULT 0,
  height      INTEGER NOT NULL DEFAULT 0,
  duration    REAL NOT NULL DEFAULT 0,
  video_codec TEXT NOT NULL DEFAULT '',
  audio_codec TEXT NOT NULL DEFAULT '',
  frame_rate  REAL NOT NULL DEFAULT 0,
  bit_rate    INTEGER NOT NULL DEFAULT 0,
  is_primary  INTEGER NOT NULL DEFAULT 0,
  oshash      TEXT,
  md5         TEXT,
  phash       TEXT,
  seen_run    INTEGER NOT NULL,
  gone_run    INTEGER
);

CREATE TABLE IF NOT EXISTS run (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  kind          TEXT NOT NULL,
  since         TEXT,
  scenes_seen   INTEGER NOT NULL DEFAULT 0,
  files_seen    INTEGER NOT NULL DEFAULT 0,
  scenes_gone   INTEGER NOT NULL DEFAULT 0,
  stash_version TEXT NOT NULL DEFAULT '',
  notes         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS decision (
  key        TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  value      TEXT NOT NULL,
  keeper     TEXT,
  decided_at TEXT NOT NULL,
  notes      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS mutation_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER,
  at         TEXT NOT NULL,
  kind       TEXT NOT NULL,
  target     TEXT NOT NULL,
  from_value TEXT NOT NULL DEFAULT '',
  to_value   TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS candidate_group (
  signature   TEXT PRIMARY KEY,
  verdict     TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  reclaimable INTEGER NOT NULL DEFAULT 0,
  seen_run    INTEGER NOT NULL,
  gone_run    INTEGER
);

CREATE TABLE IF NOT EXISTS candidate_scene (
  signature TEXT NOT NULL REFERENCES candidate_group(signature) ON DELETE CASCADE,
  scene_id  TEXT NOT NULL,
  PRIMARY KEY(signature, scene_id)
);

CREATE TABLE IF NOT EXISTS candidate_evidence (
  signature TEXT NOT NULL REFERENCES candidate_group(signature) ON DELETE CASCADE,
  generator TEXT NOT NULL,
  kind      TEXT NOT NULL,
  detail    TEXT NOT NULL DEFAULT '',
  value     REAL
);

INSERT INTO schema_version(version) VALUES(2);
INSERT INTO candidate_group(signature, verdict, reason, reclaimable, seen_run, gone_run)
  VALUES('hash1+hash2', 'certain', 'durations agree', 100, 1, NULL);
`

func TestAddColumnIfMissingFailsWhenTableIsGone(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE candidate_group`); err != nil {
		t.Fatalf("dropping candidate_group: %v", err)
	}
	if err := l.addColumnIfMissing(ctx, "candidate_group", "keeper", "TEXT"); err == nil {
		t.Error("addColumnIfMissing should fail when the table doesn't exist")
	}
}

func TestAddColumnIfMissingNoopsWhenColumnAlreadyThere(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	// candidate_group.keeper already exists on a fresh ledger.
	if err := l.addColumnIfMissing(ctx, "candidate_group", "keeper", "TEXT"); err != nil {
		t.Errorf("addColumnIfMissing should no-op when the column exists: %v", err)
	}
}

func TestStatsFailsWhenRunTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE run`); err != nil {
		t.Fatalf("dropping run: %v", err)
	}
	if _, err := l.Stats(ctx); err == nil {
		t.Error("Stats should fail when run is missing")
	}
}

func TestOpenMigratesAnExistingV2Ledger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.ledger.sqlite")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening raw sqlite: %v", err)
	}
	if _, err := raw.Exec(schemaV2SQL); err != nil {
		t.Fatalf("applying v2 schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("closing raw sqlite: %v", err)
	}

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrating v2): %v", err)
	}
	defer func() { _ = l.Close() }()

	var hasKeeper bool
	rows, err := l.DB().Query(`PRAGMA table_info(candidate_group)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scanning table_info: %v", err)
		}
		if name == "keeper" {
			hasKeeper = true
		}
	}
	_ = rows.Close()
	if !hasKeeper {
		t.Fatal("candidate_group.keeper column missing after migration")
	}

	var stepTableExists int
	if err := l.DB().QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='candidate_step'`).
		Scan(&stepTableExists); err != nil {
		t.Fatalf("checking candidate_step: %v", err)
	}
	if stepTableExists != 1 {
		t.Fatal("candidate_step table missing after migration")
	}

	// The pre-existing group survived the migration untouched.
	var verdict string
	if err := l.DB().QueryRow(`SELECT verdict FROM candidate_group WHERE signature = 'hash1+hash2'`).
		Scan(&verdict); err != nil {
		t.Fatalf("reading migrated group: %v", err)
	}
	if verdict != "certain" {
		t.Errorf("verdict = %q, want certain", verdict)
	}

	// SetChoice works against the migrated schema.
	ctx := context.Background()
	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"}, []Step{{Rule: "tier", Outcome: "1 wins"}}); err != nil {
		t.Fatalf("SetChoice on migrated ledger: %v", err)
	}

	var version int
	if err := l.DB().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, currentSchemaVersion)
	}
}
