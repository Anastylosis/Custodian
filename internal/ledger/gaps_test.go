package ledger

import (
	"context"
	"reflect"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func fileWithPhash(id, path string, size int64, phash string, opts ...func(*stash.File)) stash.File {
	f := stash.File{ID: id, Path: path, Basename: baseOfPath(path), Size: size}
	if phash != "" {
		f.Fingerprints = append(f.Fingerprints, stash.Fingerprint{Type: "phash", Value: phash})
	}
	for _, o := range opts {
		o(&f)
	}
	return f
}

func baseOfPath(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func TestPhashGapEmptyLedger(t *testing.T) {
	l := newTestLedger(t)
	gap, err := l.PhashGap(context.Background())
	if err != nil {
		t.Fatalf("PhashGap: %v", err)
	}
	if gap.Count != 0 || gap.Bytes != 0 || gap.WithStashID != 0 || len(gap.Dirs) != 0 {
		t.Fatalf("PhashGap on empty ledger = %+v, want zero value", gap)
	}
}

func TestPhashGapCountsBytesAndDirs(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/tmp4/a.mp4", 100, "")}},
		{ID: "2", Files: []stash.File{fileWithPhash("f2", "/data/tmp4/b.mp4", 200, "")},
			StashIDs: []stash.StashID{{Endpoint: "https://sb", ID: "u1"}}},
		{ID: "3", Files: []stash.File{fileWithPhash("f3", "/data/tmp4/c.mp4", 50, "abc")}},
		{ID: "4", Files: []stash.File{fileWithPhash("f4", "/data/other/d.mp4", 10, "")}},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	gap, err := l.PhashGap(ctx)
	if err != nil {
		t.Fatalf("PhashGap: %v", err)
	}
	if gap.Count != 3 {
		t.Fatalf("Count = %d, want 3", gap.Count)
	}
	if gap.Bytes != 100+200+10 {
		t.Fatalf("Bytes = %d, want %d", gap.Bytes, 100+200+10)
	}
	if gap.WithStashID != 1 {
		t.Fatalf("WithStashID = %d, want 1 (scene 2 only)", gap.WithStashID)
	}
	if len(gap.Dirs) != 2 {
		t.Fatalf("Dirs = %+v, want 2 entries", gap.Dirs)
	}
	// /data/tmp4 has the larger gap (2) and sorts first.
	if gap.Dirs[0].Dir != "/data/tmp4" || gap.Dirs[0].Gap != 2 || gap.Dirs[0].Live != 3 || gap.Dirs[0].Bytes != 300 {
		t.Fatalf("Dirs[0] = %+v", gap.Dirs[0])
	}
	if gap.Dirs[1].Dir != "/data/other" || gap.Dirs[1].Gap != 1 || gap.Dirs[1].Live != 1 || gap.Dirs[1].Bytes != 10 {
		t.Fatalf("Dirs[1] = %+v", gap.Dirs[1])
	}
}

func TestPhashGapIgnoresGoneFiles(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/a.mp4", 100, "")}},
	}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	// A second full sync that sees nothing tombstones the file.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	gap, err := l.PhashGap(ctx)
	if err != nil {
		t.Fatalf("PhashGap: %v", err)
	}
	if gap.Count != 0 {
		t.Fatalf("Count = %d, want 0 (file is gone)", gap.Count)
	}
}

func TestPhashGapScenesOrderedByPath(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		{ID: "2", Files: []stash.File{fileWithPhash("f2", "/data/b.mp4", 1, "")}},
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/a.mp4", 1, "")}},
		{ID: "3", Files: []stash.File{fileWithPhash("f3", "/data/c.mp4", 1, "known")}},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.PhashGapScenes(ctx)
	if err != nil {
		t.Fatalf("PhashGapScenes: %v", err)
	}
	want := []PhashGapFile{
		{SceneID: "1", Path: "/data/a.mp4"},
		{SceneID: "2", Path: "/data/b.mp4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PhashGapScenes = %+v, want %+v", got, want)
	}
}

func TestQuarantineGapEmptyDirIsZeroValue(t *testing.T) {
	l := newTestLedger(t)
	gap, err := l.QuarantineGap(context.Background(), "")
	if err != nil {
		t.Fatalf("QuarantineGap: %v", err)
	}
	if gap.Count != 0 || len(gap.Paths) != 0 {
		t.Fatalf("QuarantineGap(\"\") = %+v, want zero value", gap)
	}
}

func TestQuarantineGapSeparatesOwnFromForeign(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/_quarantine/own.mp4", 111, "")}},
		{ID: "2", Files: []stash.File{fileWithPhash("f2", "/data/_quarantine/foreign.mp4", 222, "")}},
		{ID: "3", Files: []stash.File{fileWithPhash("f3", "/data/_quarantine/sub/nested.mp4", 333, "")}},
		{ID: "4", Files: []stash.File{fileWithPhash("f4", "/data/elsewhere/other.mp4", 444, "")}},
		// A file whose path merely starts with the dir's characters but is
		// not inside it — must not be treated as "under" the quarantine dir.
		{ID: "5", Files: []stash.File{fileWithPhash("f5", "/data/_quarantine2/sneaky.mp4", 555, "")}},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	// custodian itself moved f1 there — log the move that put it there.
	mutID, err := l.AppendMutation(ctx, Mutation{
		Kind: "move", Target: "file f1", From: "/data/orig/own.mp4", To: "/data/_quarantine/own.mp4",
	})
	if err != nil {
		t.Fatalf("AppendMutation: %v", err)
	}
	if err := l.MarkMutation(ctx, mutID, true, ""); err != nil {
		t.Fatalf("MarkMutation: %v", err)
	}

	gap, err := l.QuarantineGap(ctx, "/data/_quarantine")
	if err != nil {
		t.Fatalf("QuarantineGap: %v", err)
	}
	if gap.Count != 2 {
		t.Fatalf("Count = %d, want 2 (foreign.mp4 and sub/nested.mp4)", gap.Count)
	}
	if gap.Bytes != 222+333 {
		t.Fatalf("Bytes = %d, want %d", gap.Bytes, 222+333)
	}
	want := []string{"/data/_quarantine/foreign.mp4", "/data/_quarantine/sub/nested.mp4"}
	if !reflect.DeepEqual(gap.Paths, want) {
		t.Fatalf("Paths = %v, want %v", gap.Paths, want)
	}
}

func TestQuarantineGapTrailingSlashOnDir(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/_quarantine/x.mp4", 1, "")}},
	}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	gap, err := l.QuarantineGap(ctx, "/data/_quarantine/")
	if err != nil {
		t.Fatalf("QuarantineGap: %v", err)
	}
	if gap.Count != 1 {
		t.Fatalf("Count = %d, want 1", gap.Count)
	}
}

func TestScenesWithoutPrimaryFile(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/a.mp4", 1, "")}},
		{ID: "2", Files: nil},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.ScenesWithoutPrimaryFile(ctx)
	if err != nil {
		t.Fatalf("ScenesWithoutPrimaryFile: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("ScenesWithoutPrimaryFile = %v, want [2]", got)
	}
}

func TestScenesWithoutPrimaryFileIgnoresGoneScenes(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, []stash.Scene{{ID: "1", Files: nil}}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	got, err := l.ScenesWithoutPrimaryFile(ctx)
	if err != nil {
		t.Fatalf("ScenesWithoutPrimaryFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ScenesWithoutPrimaryFile = %v, want none (scene 1 is gone)", got)
	}
}

func TestTombstonedInLiveGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/a.mp4", 1, "x")}},
		{ID: "2", Files: []stash.File{fileWithPhash("f2", "/data/b.mp4", 1, "y")}},
	}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: VerdictCertain, Reason: "test"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	// Scene 1 vanishes from Stash: a full sync that never sees it again.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.UpsertScenes(ctx, run2, []stash.Scene{
		{ID: "2", Files: []stash.File{fileWithPhash("f2", "/data/b.mp4", 1, "y")}},
	}); err != nil {
		t.Fatalf("UpsertScenes 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	got, err := l.TombstonedInLiveGroups(ctx)
	if err != nil {
		t.Fatalf("TombstonedInLiveGroups: %v", err)
	}
	want := []TombstonedGroupRef{{SceneID: "1", Signature: "sig1", Verdict: VerdictCertain}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TombstonedInLiveGroups = %+v, want %+v", got, want)
	}
}

func TestTombstonedInLiveGroupsIgnoresGoneGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{
		{ID: "1", Files: []stash.File{fileWithPhash("f1", "/data/a.mp4", 1, "x")}},
	}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []Group{
		{Signature: "sig1", SceneIDs: []string{"1"}, Verdict: VerdictCertain, Reason: "test"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	// Scene 1 is tombstoned...
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}
	// ...and a rescan replaces the group set with nothing, tombstoning the
	// group too — no live group references it any more.
	if err := l.ReplaceGroups(ctx, run2, nil); err != nil {
		t.Fatalf("ReplaceGroups 2: %v", err)
	}

	got, err := l.TombstonedInLiveGroups(ctx)
	if err != nil {
		t.Fatalf("TombstonedInLiveGroups: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("TombstonedInLiveGroups = %+v, want none (group is gone)", got)
	}
}
