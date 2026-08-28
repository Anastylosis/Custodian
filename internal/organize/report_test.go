package organize

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/vfs"
)

func TestPrintReportEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintReport(&buf, &ScanResult{}); err != nil {
		t.Fatalf("PrintReport: %v", err)
	}
	if !strings.Contains(buf.String(), "no live scenes to file") {
		t.Errorf("output = %q", buf.String())
	}
}

func TestPrintReportListsEntriesAndCounts(t *testing.T) {
	res := &ScanResult{
		ScenesProcessed:  4,
		Moves:            1,
		Renames:          1,
		AlreadyCorrect:   0,
		SkipNoMetadata:   1,
		Conflicts:        1,
		SkippedDuplicate: 1,
		MultiFileWarning: 1,
		Entries: []Entry{
			{SceneID: "1", FileID: "f1", CurrentPath: "/a.mp4", TargetPath: "/data/a.mp4", Status: "move"},
			{SceneID: "2", FileID: "f2", CurrentPath: "/b.mp4", TargetPath: "/b2.mp4", Status: "rename"},
			{SceneID: "3", FileID: "f3", CurrentPath: "/c.mp4", TargetPath: "/c.mp4", Status: "conflict", Reason: "taken"},
			{SceneID: "4", FileID: "f4", CurrentPath: "/d.mp4", TargetPath: "/d.mp4", Status: "skip_no_metadata", Reason: "missing required field: date"},
		},
	}
	var buf bytes.Buffer
	if err := PrintReport(&buf, res); err != nil {
		t.Fatalf("PrintReport: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"[move] scene 1", "[rename] scene 2", "[CONFLICT] scene 3", "reason: taken",
		"[skip] scene 4", "missing required field: date",
		"to move:       1", "to rename:     1", "already filed: 0",
		"no metadata:   1", "conflicts:     1",
		"skipped (duplicate, about to leave): 1",
		"multiple files",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestPrintApplyReportDryRunWording(t *testing.T) {
	var buf bytes.Buffer
	r := &ApplyReport{DryRun: true, Succeeded: 2, SidecarsMoved: 1}
	if err := PrintApplyReport(&buf, r); err != nil {
		t.Fatalf("PrintApplyReport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DRY RUN") || !strings.Contains(out, "would move:    2") {
		t.Errorf("output = %q", out)
	}
	if strings.Contains(out, "Run a Stash scan") {
		t.Errorf("dry run should not tell the user to rescan: %q", out)
	}
}

func TestPrintApplyReportCommitWithFailuresAndNotes(t *testing.T) {
	var buf bytes.Buffer
	r := &ApplyReport{
		Succeeded: 1, Failed: 1, Failures: []string{"file f9 → /x: boom"},
		SidecarsMoved: 1, SidecarsSkipped: 1, SidecarsFailed: 1,
		SidecarNotes: []string{"skipped /a.srt: destination already exists"},
	}
	if err := PrintApplyReport(&buf, r); err != nil {
		t.Fatalf("PrintApplyReport: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"COMMIT", "moved:    1", "failed:    1", "file f9 → /x: boom",
		"sidecars skipped: 1", "sidecars failed:  1",
		"Run a Stash scan over the affected folders",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestPreviewSidecarsNilMoverOrPlan(t *testing.T) {
	if n, notes := PreviewSidecars(context.Background(), nil, nil); n != 0 || notes != nil {
		t.Errorf("nil plan/mover: got (%d, %v)", n, notes)
	}
}

func TestPreviewSidecarsCountsAcrossMovesAndRenames(t *testing.T) {
	fs := vfs.NewMem("/incoming/x.mp4", "/incoming/x.en.srt", "/data/a/y.mp4", "/data/a/y.en.srt")
	mover := testMover(t, fs)
	plan := &ApplyPlan{
		Moves:   []Entry{{CurrentPath: "/incoming/x.mp4", TargetPath: "/data/p/x.mp4"}},
		Renames: []Entry{{CurrentPath: "/data/a/y.mp4", TargetPath: "/data/a/z.mp4"}},
	}
	n, notes := PreviewSidecars(context.Background(), plan, mover)
	if n != 2 || len(notes) != 0 {
		t.Fatalf("PreviewSidecars = (%d, %v), want (2, nil)", n, notes)
	}
	// Nothing on disk should have moved yet — this is only a preview.
	if !fs.Files["/incoming/x.en.srt"] {
		t.Error("preview must not move anything")
	}
}
