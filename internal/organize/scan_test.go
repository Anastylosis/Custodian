package organize

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func performers(names ...string) []stash.Performer {
	out := make([]stash.Performer, len(names))
	for i, n := range names {
		out[i] = stash.Performer{Name: n}
	}
	return out
}

func file(id, path, basename string, height int) stash.File {
	return stash.File{ID: id, Path: path, Basename: basename, Height: height, Width: 1920}
}

func seedScan(t *testing.T, l *ledger.Ledger, scenes []stash.Scene) {
	t.Helper()
	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
}

func TestScanRequiresLedgerAndConfig(t *testing.T) {
	ctx := context.Background()
	if _, err := Scan(ctx, nil, &config.Config{Organize: *defaultCfg()}, ScanOptions{}); err == nil {
		t.Fatal("want error for nil ledger")
	}
	l := newTestLedger(t)
	if _, err := Scan(ctx, l, &config.Config{}, ScanOptions{}); err == nil {
		t.Fatal("want error for missing base_dir/path_template")
	}
}

func TestScanMove(t *testing.T) {
	l := newTestLedger(t)
	seedScan(t, l, []stash.Scene{
		{
			ID: "1", Title: "T1", Date: "2024-01-01", Performers: performers("P1"),
			Files: []stash.File{file("f1", "/incoming/x.mp4", "x.mp4", 1080)},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Moves != 1 || len(res.Entries) != 1 {
		t.Fatalf("res = %+v", res)
	}
	e := res.Entries[0]
	want := filepath.FromSlash("/data/P1/2024-01-01_P1-T1_1080p.mp4")
	if e.Status != "move" || e.TargetPath != want {
		t.Errorf("entry = %+v, want move to %s", e, want)
	}
}

func TestScanRename(t *testing.T) {
	l := newTestLedger(t)
	target := filepath.FromSlash("/data/P2/2024-02-02_P2-T2_720p.mp4")
	current := filepath.FromSlash("/data/P2/wrong-name.mp4")
	seedScan(t, l, []stash.Scene{
		{
			ID: "2", Title: "T2", Date: "2024-02-02", Performers: performers("P2"),
			Files: []stash.File{file("f2", current, "wrong-name.mp4", 720)},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Renames != 1 {
		t.Fatalf("res = %+v", res)
	}
	e := res.Entries[0]
	if e.Status != "rename" || e.TargetPath != target {
		t.Errorf("entry = %+v, want rename to %s", e, target)
	}
}

func TestScanAlreadyCorrect(t *testing.T) {
	l := newTestLedger(t)
	path := filepath.FromSlash("/data/P3/2024-03-03_P3-T3_1080p.mp4")
	seedScan(t, l, []stash.Scene{
		{
			ID: "3", Title: "T3", Date: "2024-03-03", Performers: performers("P3"),
			Files: []stash.File{file("f3", path, "2024-03-03_P3-T3_1080p.mp4", 1080)},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.AlreadyCorrect != 1 || res.Moves != 0 || res.Renames != 0 {
		t.Fatalf("res = %+v", res)
	}
	if res.Entries[0].Status != "already_correct" {
		t.Errorf("entry = %+v", res.Entries[0])
	}
}

func TestScanMissingMetadata(t *testing.T) {
	l := newTestLedger(t)
	seedScan(t, l, []stash.Scene{
		{
			ID: "4", Title: "T4", Date: "2024-04-04", // no performers
			Files: []stash.File{file("f4", "/incoming/no-performer.mp4", "no-performer.mp4", 1080)},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.SkipNoMetadata != 1 {
		t.Fatalf("res = %+v", res)
	}
	if res.Entries[0].Status != "skip_no_metadata" || res.Entries[0].TargetPath != res.Entries[0].CurrentPath {
		t.Errorf("entry = %+v", res.Entries[0])
	}
}

func TestScanConflictBetweenTwoPlannedMoves(t *testing.T) {
	l := newTestLedger(t)
	seedScan(t, l, []stash.Scene{
		{
			ID: "5a", Title: "T5", Date: "2024-05-05", Performers: performers("P5"),
			Files: []stash.File{file("f5a", "/incoming/a.mp4", "a.mp4", 720)},
		},
		{
			ID: "5b", Title: "T5", Date: "2024-05-05", Performers: performers("P5"),
			Files: []stash.File{file("f5b", "/incoming/b.mp4", "b.mp4", 720)},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Conflicts != 2 || res.Moves != 0 {
		t.Fatalf("res = %+v", res)
	}
	for _, e := range res.Entries {
		if e.Status != "conflict" {
			t.Errorf("entry %+v, want conflict", e)
		}
	}
}

func TestScanConflictWithExistingFileOnDisk(t *testing.T) {
	l := newTestLedger(t)
	occupied := filepath.FromSlash("/data/P6/2024-06-06_P6-T6_1080p.mp4")
	seedScan(t, l, []stash.Scene{
		{
			ID: "occupant", Title: "T6", Date: "2024-06-06", Performers: performers("P6"),
			Files: []stash.File{file("f6a", occupied, "2024-06-06_P6-T6_1080p.mp4", 1080)},
		},
		{
			ID: "mover", Title: "T6", Date: "2024-06-06", Performers: performers("P6"),
			Files: []stash.File{file("f6b", "/incoming/other.mp4", "other.mp4", 1080)},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.Conflicts != 1 || res.AlreadyCorrect != 1 {
		t.Fatalf("res = %+v", res)
	}
	var mover Entry
	for _, e := range res.Entries {
		if e.SceneID == "mover" {
			mover = e
		}
	}
	if mover.Status != "conflict" {
		t.Errorf("mover entry = %+v, want conflict", mover)
	}
}

func TestScanSkipsQuarantinedAndPartialLosers(t *testing.T) {
	l := newTestLedger(t)
	seedScan(t, l, []stash.Scene{
		{
			ID: "keeper", Title: "K", Date: "2024-07-07", Performers: performers("P7"),
			Files: []stash.File{file("fk", "/incoming/keeper.mp4", "keeper.mp4", 1080)},
		},
		{
			ID: "loser", Title: "L", Date: "2024-07-08", Performers: performers("P7"),
			Files: []stash.File{file("fl", "/incoming/loser.mp4", "loser.mp4", 1080)},
		},
	})

	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig7", SceneIDs: []string{"keeper", "loser"}, Verdict: ledger.VerdictPartial, Reason: "clip"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.SetChoice(ctx, "sig7", "keeper", []string{"loser"}, nil); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	res, err := Scan(ctx, l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.SkippedDuplicate != 1 {
		t.Fatalf("SkippedDuplicate = %d, want 1 (res = %+v)", res.SkippedDuplicate, res)
	}
	for _, e := range res.Entries {
		if e.SceneID == "loser" {
			t.Errorf("loser scene should have been skipped entirely, found entry %+v", e)
		}
	}
}

func TestScanMultiFileWarning(t *testing.T) {
	l := newTestLedger(t)
	seedScan(t, l, []stash.Scene{
		{
			ID: "8", Title: "T8", Date: "2024-08-08", Performers: performers("P8"),
			Files: []stash.File{
				file("f8a", "/incoming/main.mp4", "main.mp4", 1080),
				file("f8b", "/incoming/extra.mp4", "extra.mp4", 1080),
			},
		},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.MultiFileWarning != 1 || res.ScenesProcessed != 1 {
		t.Fatalf("res = %+v", res)
	}
}

func TestScanLimit(t *testing.T) {
	l := newTestLedger(t)
	seedScan(t, l, []stash.Scene{
		{ID: "9", Title: "T9", Date: "2024-09-09", Performers: performers("P9"),
			Files: []stash.File{file("f9", "/incoming/one.mp4", "one.mp4", 1080)}},
		{ID: "10", Title: "T10", Date: "2024-10-10", Performers: performers("P10"),
			Files: []stash.File{file("f10", "/incoming/two.mp4", "two.mp4", 1080)}},
	})

	res, err := Scan(context.Background(), l, &config.Config{Organize: *defaultCfg()}, ScanOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.ScenesProcessed != 1 {
		t.Fatalf("res = %+v, want exactly 1 scene processed", res)
	}
}
