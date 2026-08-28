package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func newCaptionsTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestSceneRefsFromLedger(t *testing.T) {
	ctx := context.Background()
	l := newCaptionsTestLedger(t)

	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	scenes := []stash.Scene{
		{
			ID: "1", Title: "T1", Date: "2024-01-01",
			Studio:     &stash.Studio{Name: "Studio One"},
			Performers: []stash.Performer{{Name: "Alice"}},
			Files: []stash.File{{
				ID: "f1", Path: "/lib/one.mp4", Basename: "one.mp4", Duration: 754.2,
			}},
		},
		{
			ID: "2", Title: "T2", Date: "2024-02-02",
			Files: []stash.File{{ID: "f2", Path: "/lib/two.mp4", Basename: "two.mp4", Duration: 60}},
		},
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	refs, err := sceneRefsFromLedger(ctx, l)
	if err != nil {
		t.Fatalf("sceneRefsFromLedger: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("len(refs) = %d, want 2", len(refs))
	}

	one := refs[0]
	if one.ID != "1" || one.Title != "T1" || one.Date != "2024-01-01" || one.Studio != "Studio One" {
		t.Errorf("refs[0] = %+v", one)
	}
	if len(one.Performers) != 1 || one.Performers[0] != "Alice" {
		t.Errorf("refs[0].Performers = %v", one.Performers)
	}
	if one.Path != "/lib/one.mp4" || one.Stem != "one" {
		t.Errorf("refs[0] path/stem = %q/%q", one.Path, one.Stem)
	}
	if one.Duration != time.Duration(754.2*float64(time.Second)) {
		t.Errorf("refs[0].Duration = %v", one.Duration)
	}

	two := refs[1]
	if two.Studio != "" || len(two.Performers) != 0 {
		t.Errorf("refs[1] = %+v, want no studio/performers", two)
	}
}
