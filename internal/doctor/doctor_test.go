package doctor

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func fileNoPhash(id, path string, size int64) stash.File {
	return stash.File{ID: id, Path: path, Basename: baseOf(path), Size: size}
}

func baseOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func TestCheckOnEmptyLedgerIsAllClean(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	f, client := newFakeStash(t)
	f.sceneCount = 0
	cfg := &config.Config{}
	cfg.Quarantine.Dir = "/data/_quarantine"

	rep, err := Check(ctx, client, l, cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Phash.Count != 0 {
		t.Errorf("Phash.Count = %d, want 0", rep.Phash.Count)
	}
	if rep.Quarantine.Count != 0 {
		t.Errorf("Quarantine.Count = %d, want 0", rep.Quarantine.Count)
	}
	if len(rep.NoPrimary) != 0 {
		t.Errorf("NoPrimary = %v, want none", rep.NoPrimary)
	}
	if len(rep.Tombstoned) != 0 {
		t.Errorf("Tombstoned = %v, want none", rep.Tombstoned)
	}
	if rep.Age.LastSync != "" {
		t.Errorf("Age.LastSync = %q, want empty (never synced)", rep.Age.LastSync)
	}

	lines := rep.Lines()
	want := []string{
		"phash gap:      none",
		"quarantine:     clean — nothing foreign in the quarantine directory",
		"no primary:     none",
		"tombstoned:     none",
		"sync age:       never synced — `custodian sync --full`",
	}
	if len(lines) != len(want) {
		t.Fatalf("Lines() = %v, want %v", lines, want)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("Lines()[%d] = %q, want %q", i, lines[i], w)
		}
	}
}

func TestCheckFindsEveryGap(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		// Files without a phash, concentrated in one directory.
		{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/tmp4/a.mp4", 100)}},
		{ID: "2", Files: []stash.File{fileNoPhash("f2", "/data/tmp4/b.mp4", 200)}},
		// A scene with no files at all — no primary file.
		{ID: "3", Files: nil},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, []ledger.Group{
		{Signature: "sig1", SceneIDs: []string{"1", "2"}, Verdict: ledger.VerdictCertain, Reason: "test"},
	}); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{ScenesSeen: len(scenes)}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	// A second full sync: scene 1 vanishes from Stash (tombstoned, still
	// referenced by sig1), scenes 2 and 3 are re-seen unchanged, and a
	// foreign file appears in the quarantine dir custodian never moved.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.UpsertScenes(ctx, run2, []stash.Scene{
		{ID: "2", Files: []stash.File{fileNoPhash("f2", "/data/tmp4/b.mp4", 200)}},
		{ID: "3", Files: nil},
		{ID: "4", Files: []stash.File{fileNoPhash("f4", "/data/_quarantine/foreign.mp4", 999)}},
	}); err != nil {
		t.Fatalf("UpsertScenes 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}
	if err := l.FinishRun(ctx, run2, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun 2: %v", err)
	}

	f, client := newFakeStash(t)
	f.sceneCount = 99 // disagrees with the ledger's live count

	cfg := &config.Config{}
	cfg.Quarantine.Dir = "/data/_quarantine"

	rep, err := Check(ctx, client, l, cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if rep.Phash.Count != 2 {
		t.Errorf("Phash.Count = %d, want 2 (scene 2's and scene 4's files; scene 1 was tombstoned)", rep.Phash.Count)
	}
	if rep.Quarantine.Count != 1 || len(rep.Quarantine.Paths) != 1 || rep.Quarantine.Paths[0] != "/data/_quarantine/foreign.mp4" {
		t.Errorf("Quarantine = %+v, want one foreign file", rep.Quarantine)
	}
	if len(rep.NoPrimary) != 1 || rep.NoPrimary[0] != "3" {
		t.Errorf("NoPrimary = %v, want [3]", rep.NoPrimary)
	}
	if len(rep.Tombstoned) != 1 || rep.Tombstoned[0].SceneID != "1" || rep.Tombstoned[0].Signature != "sig1" {
		t.Errorf("Tombstoned = %+v, want scene 1 in sig1", rep.Tombstoned)
	}
	if rep.Age.StashScenes != 99 {
		t.Errorf("Age.StashScenes = %d, want 99", rep.Age.StashScenes)
	}
	if rep.Age.LedgerScenes == rep.Age.StashScenes {
		t.Errorf("Age.LedgerScenes = %d, expected it to differ from StashScenes", rep.Age.LedgerScenes)
	}

	lines := rep.Lines()
	if len(lines) == 0 {
		t.Fatal("Lines() returned nothing")
	}
}

func TestCheckAgeGapStashUnreachable(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	rep, err := Check(ctx, nil, l, &config.Config{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Age.StashErr == "" {
		t.Fatal("Age.StashErr is empty, want a message for a missing client")
	}
	line := ageLine(rep.Age)
	if line == "" {
		t.Fatal("ageLine returned empty string")
	}
}

func TestCheckAgeGapMatchesStash(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}}}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{ScenesSeen: 1}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	f, client := newFakeStash(t)
	f.sceneCount = 1

	rep, err := Check(ctx, client, l, &config.Config{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Age.LastSync == "" {
		t.Fatal("Age.LastSync is empty after a sync")
	}
	if rep.Age.StashScenes != 1 || rep.Age.LedgerScenes != 1 {
		t.Fatalf("Age = %+v, want both counts at 1", rep.Age)
	}
	line := ageLine(rep.Age)
	if !strings.Contains(line, "matches Stash") {
		t.Errorf("ageLine = %q, want it to say the ledger matches Stash", line)
	}
}

func TestCheckJSONShapeIsPinned(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	_, client := newFakeStash(t)

	rep, err := Check(ctx, client, l, &config.Config{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	wantTopLevel := []string{"phash", "quarantine", "scenes_without_primary_file", "tombstoned_in_live_groups", "age"}
	for _, key := range wantTopLevel {
		if _, ok := got[key]; !ok {
			t.Errorf("top-level JSON missing key %q; got %v", key, got)
		}
	}
	if len(got) != len(wantTopLevel) {
		t.Errorf("top-level JSON has %d keys, want %d: %v", len(got), len(wantTopLevel), got)
	}

	var phash map[string]json.RawMessage
	if err := json.Unmarshal(got["phash"], &phash); err != nil {
		t.Fatalf("Unmarshal phash: %v", err)
	}
	for _, key := range []string{"count", "bytes", "with_stash_id", "dirs"} {
		if _, ok := phash[key]; !ok {
			t.Errorf("phash JSON missing key %q; got %v", key, phash)
		}
	}

	var quarantine map[string]json.RawMessage
	if err := json.Unmarshal(got["quarantine"], &quarantine); err != nil {
		t.Fatalf("Unmarshal quarantine: %v", err)
	}
	for _, key := range []string{"count", "bytes", "paths"} {
		if _, ok := quarantine[key]; !ok {
			t.Errorf("quarantine JSON missing key %q; got %v", key, quarantine)
		}
	}

	var age map[string]json.RawMessage
	if err := json.Unmarshal(got["age"], &age); err != nil {
		t.Fatalf("Unmarshal age: %v", err)
	}
	for _, key := range []string{"last_sync", "last_sync_kind", "ledger_scenes", "stash_scenes"} {
		if _, ok := age[key]; !ok {
			t.Errorf("age JSON missing key %q; got %v", key, age)
		}
	}
}

func TestCheckCapsTopDirsAndQuarantineList(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	var scenes []stash.Scene
	for i := 0; i < maxTopDirs+3; i++ {
		id := string(rune('a' + i))
		scenes = append(scenes, stash.Scene{
			ID:    "s" + id,
			Files: []stash.File{fileNoPhash("f"+id, "/data/dir"+id+"/x.mp4", int64(i+1))},
		})
	}
	for i := 0; i < maxQuarantineList+5; i++ {
		id := string(rune('a' + i))
		scenes = append(scenes, stash.Scene{
			ID:    "q" + id,
			Files: []stash.File{fileNoPhash("qf"+id, "/data/_quarantine/f"+id+".mp4", 1)},
		})
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	_, client := newFakeStash(t)
	cfg := &config.Config{}
	cfg.Quarantine.Dir = "/data/_quarantine"

	rep, err := Check(ctx, client, l, cfg)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(rep.Phash.Dirs) != maxTopDirs {
		t.Errorf("Phash.Dirs = %d entries, want %d", len(rep.Phash.Dirs), maxTopDirs)
	}
	if rep.Quarantine.Count != maxQuarantineList+5 {
		t.Errorf("Quarantine.Count = %d, want %d (uncapped total)", rep.Quarantine.Count, maxQuarantineList+5)
	}
	if len(rep.Quarantine.Paths) != maxQuarantineList {
		t.Errorf("Quarantine.Paths = %d entries, want %d (capped for display)", len(rep.Quarantine.Paths), maxQuarantineList)
	}
}
