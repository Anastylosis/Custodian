package ledger

import (
	"context"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

// identifyFixture is twoScenes() (scene 1: a url and a stash_id; scene 2:
// neither) plus a third scene carrying a url but no stash_id — the case
// both queries need to tell apart.
func identifyFixture() []stash.Scene {
	scenes := twoScenes()
	scenes = append(scenes, stash.Scene{
		ID: "3", Title: "Third", Date: "2024-03-03",
		URLs:      []string{"https://example/3"},
		Files:     []stash.File{{ID: "f4", Basename: "c.mp4", Path: "/lib/c.mp4", Size: 10}},
		UpdatedAt: "2024-03-03 00:00:00",
	})
	return scenes
}

func TestScenesWithoutStashID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, identifyFixture()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.ScenesWithoutStashID(ctx, 0)
	if err != nil {
		t.Fatalf("ScenesWithoutStashID: %v", err)
	}
	if len(got) != 2 || got[0] != "2" || got[1] != "3" {
		t.Fatalf("ScenesWithoutStashID = %v, want [2 3] (scene 1 has a stash_id)", got)
	}
}

func TestScenesWithoutStashIDRespectsLimit(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, identifyFixture()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.ScenesWithoutStashID(ctx, 1)
	if err != nil {
		t.Fatalf("ScenesWithoutStashID: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ScenesWithoutStashID with limit 1 = %v, want 1 row", got)
	}
}

func TestScenesWithURLNoStashID(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, identifyFixture()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	got, err := l.ScenesWithURLNoStashID(ctx, 0)
	if err != nil {
		t.Fatalf("ScenesWithURLNoStashID: %v", err)
	}
	// Scene 1 has a url but also a stash_id (excluded); scene 2 has neither
	// (excluded); scene 3 has a url and no stash_id (the only match).
	if len(got) != 1 || got[0] != "3" {
		t.Fatalf("ScenesWithURLNoStashID = %v, want [3]", got)
	}
}

func TestScenesWithURLNoStashIDIgnoresGoneScenes(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	run1, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 1: %v", err)
	}
	if err := l.UpsertScenes(ctx, run1, identifyFixture()); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}

	// A second full sync that doesn't see scene 3 tombstones it.
	run2, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if err := l.UpsertScenes(ctx, run2, twoScenes()); err != nil {
		t.Fatalf("UpsertScenes 2: %v", err)
	}
	if _, _, err := l.TombstoneUnseen(ctx, run2); err != nil {
		t.Fatalf("TombstoneUnseen: %v", err)
	}

	got, err := l.ScenesWithURLNoStashID(ctx, 0)
	if err != nil {
		t.Fatalf("ScenesWithURLNoStashID: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ScenesWithURLNoStashID = %v, want none (scene 3 is now gone)", got)
	}
}

func TestScenesWithoutStashIDFailsWhenSceneTableIsMissing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE scene`); err != nil {
		t.Fatalf("dropping scene: %v", err)
	}
	if _, err := l.ScenesWithoutStashID(ctx, 0); err == nil {
		t.Error("ScenesWithoutStashID should fail when scene is missing")
	}
}
