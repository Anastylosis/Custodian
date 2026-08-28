package ledger

import (
	"context"
	"sort"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

func TestMultiFileScenesFindsOnlyScenesWithMoreThanOneFile(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.MultiFileScenes(ctx)
	if err != nil {
		t.Fatalf("MultiFileScenes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("MultiFileScenes = %d scenes, want 1 (only scene 1 has >1 file)", len(got))
	}
	files, ok := got["1"]
	if !ok {
		t.Fatalf("MultiFileScenes missing scene 1: %+v", got)
	}
	if len(files) != 2 {
		t.Fatalf("scene 1 files = %d, want 2", len(files))
	}
	if files[0].ID != "f1" || files[0].Path != "/lib/a.mp4" || files[0].Size != 100 {
		t.Errorf("files[0] = %+v", files[0])
	}
	if files[1].ID != "f2" || files[1].Size != 90 {
		t.Errorf("files[1] = %+v", files[1])
	}
}

func TestMultiFileScenesIgnoresGoneFiles(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	// A second full sync that only re-sees scene 1's first file tombstones
	// the second — scene 1 should drop out of MultiFileScenes.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	solo := twoScenes()
	solo[0].Files = solo[0].Files[:1]
	if err := l.UpsertScenes(ctx, run2, solo); err != nil {
		t.Fatalf("UpsertScenes 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	got, err := l.MultiFileScenes(ctx)
	if err != nil {
		t.Fatalf("MultiFileScenes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("MultiFileScenes = %v, want none (the second file is now gone)", got)
	}
}

func TestLiveSceneSignals(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.LiveSceneSignals(ctx)
	if err != nil {
		t.Fatalf("LiveSceneSignals: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LiveSceneSignals = %d, want 2", len(got))
	}
	sort.Slice(got, func(i, j int) bool { return got[i].SceneID < got[j].SceneID })

	if got[0].SceneID != "1" || got[0].Title != "First" || got[0].Date != "2024-01-01" ||
		got[0].StudioID != "s1" || got[0].Basename != "a.mp4" || got[0].Oshash != "hash1" || got[0].Size != 100 {
		t.Errorf("LiveSceneSignals[0] = %+v", got[0])
	}
	if got[1].SceneID != "2" || got[1].Title != "Second" || got[1].StudioID != "" || got[1].Oshash != "" {
		t.Errorf("LiveSceneSignals[1] = %+v", got[1])
	}
}

func TestLiveSceneSignalsExcludesGoneScenes(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.UpsertScenes(ctx, run2, twoScenes()[:1]); err != nil {
		t.Fatalf("UpsertScenes 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	got, err := l.LiveSceneSignals(ctx)
	if err != nil {
		t.Fatalf("LiveSceneSignals: %v", err)
	}
	if len(got) != 1 || got[0].SceneID != "1" {
		t.Errorf("LiveSceneSignals = %+v, want just scene 1", got)
	}
}

func TestMultiFileScenesFailsWhenFileTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE file`); err != nil {
		t.Fatalf("dropping file: %v", err)
	}
	if _, err := l.MultiFileScenes(ctx); err == nil {
		t.Error("MultiFileScenes should fail when file is missing")
	}
}

func TestLiveSceneSignalsFailsWhenSceneTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE scene`); err != nil {
		t.Fatalf("dropping scene: %v", err)
	}
	if _, err := l.LiveSceneSignals(ctx); err == nil {
		t.Error("LiveSceneSignals should fail when scene is missing")
	}
}

func TestLiveSceneSignalsSkipsAScenesWithoutAPrimaryFile(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	noFiles := stash.Scene{ID: "9", Title: "No Files", UpdatedAt: "2024-01-01"}
	if err := l.UpsertScenes(ctx, runID, []stash.Scene{noFiles}); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.LiveSceneSignals(ctx)
	if err != nil {
		t.Fatalf("LiveSceneSignals: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LiveSceneSignals = %+v, want none (no primary file)", got)
	}
}
