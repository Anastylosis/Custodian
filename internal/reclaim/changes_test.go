package reclaim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func TestChangesJSONIsAppendOnlyAcrossRuns(t *testing.T) {
	backupDir := t.TempDir()

	scenes1 := []stash.Scene{
		scene("10", "/data/a/keep.mp4", 4<<30),
		scene("11", "/data/a/clip.mp4", 2<<30, withTitle("First")),
	}
	_, c1 := newFakeStash(t, scenes1...)
	l1 := newLedger(t)
	seedGroups(t, l1, planned{"sig-a", "10", scenes1})
	rep1, err := Apply(t.Context(), c1, l1, Options{Commit: true, BackupDir: backupDir})
	if err != nil {
		t.Fatalf("Apply 1: %v", err)
	}

	scenes2 := []stash.Scene{
		scene("20", "/data/b/keep.mp4", 4<<30),
		scene("21", "/data/b/clip.mp4", 2<<30, withTitle("Second")),
	}
	_, c2 := newFakeStash(t, scenes2...)
	l2 := newLedger(t)
	seedGroups(t, l2, planned{"sig-b", "20", scenes2})
	rep2, err := Apply(t.Context(), c2, l2, Options{Commit: true, BackupDir: backupDir})
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}

	if rep1.ChangesFile != rep2.ChangesFile {
		t.Fatalf("changes files differ: %q vs %q — the fake backup name should be stable", rep1.ChangesFile, rep2.ChangesFile)
	}

	data, err := os.ReadFile(rep2.ChangesFile)
	if err != nil {
		t.Fatalf("reading changes JSON: %v", err)
	}
	var entries []ChangeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("decoding changes JSON: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (one appended after the other)", len(entries))
	}
	if entries[0].Signature != "sig-a" || entries[1].Signature != "sig-b" {
		t.Errorf("signatures = %s, %s, want sig-a then sig-b", entries[0].Signature, entries[1].Signature)
	}
}

func TestWriteChangesIsANoOpForNoEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.changes.json")
	if err := writeChanges(path, nil); err != nil {
		t.Fatalf("writeChanges: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("writeChanges with no entries created %s", path)
	}
}
