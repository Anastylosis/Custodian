package organize

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/sidecar"
	"github.com/Anastylosis/Custodian/internal/vfs"
	stash "github.com/Anastylosis/stash-go"
)

// fakeStash is a minimal GraphQL server answering only moveFiles — the one
// mutation organize.Execute sends. Modelled on internal/reclaim's own fake
// (internal/reclaim/fake_test.go), trimmed to what this package needs rather
// than importing that package's test file.
type fakeStash struct {
	mu     sync.Mutex
	moves  []moveCall
	failOn map[string]string
}

type moveCall struct {
	FileID   string
	Folder   string
	Basename string
}

func newFakeStash(t *testing.T) (*fakeStash, *stash.Client) {
	t.Helper()
	f := &fakeStash{failOn: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, stash.NewClient(srv.URL)
}

func (f *fakeStash) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	op := "unknown"
	if strings.Contains(req.Query, "moveFiles(input:") {
		op = "moveFiles"
	}

	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	msg, fail := f.failOn[op]
	f.mu.Unlock()
	if fail {
		fmt.Fprintf(w, `{"errors":[{"message":%q}]}`, msg)
		return
	}

	switch op {
	case "moveFiles":
		f.applyMoveFiles(req.Variables)
		_, _ = io.WriteString(w, `{"data":{"moveFiles":true}}`)
	default:
		_, _ = io.WriteString(w, `{"data":{}}`)
	}
}

func (f *fakeStash) applyMoveFiles(vars map[string]any) {
	input, _ := vars["input"].(map[string]any)
	ids, _ := input["ids"].([]any)
	folder, _ := input["destination_folder"].(string)
	basename, _ := input["destination_basename"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		s, _ := id.(string)
		f.moves = append(f.moves, moveCall{FileID: s, Folder: folder, Basename: basename})
	}
}

func (f *fakeStash) moveCalls() []moveCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]moveCall{}, f.moves...)
}

func testMover(t *testing.T, fs *vfs.Mem) *sidecar.Mover {
	t.Helper()
	mp, err := pathmap.New(nil) // identity mapping: stash paths are already local
	if err != nil {
		t.Fatalf("pathmap.New: %v", err)
	}
	mv, err := sidecar.NewMover(fs, mp, nil)
	if err != nil {
		t.Fatalf("sidecar.NewMover: %v", err)
	}
	return mv
}

func onePlan(sceneID, fileID, from, to string) *ApplyPlan {
	return &ApplyPlan{Moves: []Entry{{SceneID: sceneID, FileID: fileID, CurrentPath: from, TargetPath: to, Status: "move"}}}
}

func TestExecuteDryRunSendsNothing(t *testing.T) {
	f, client := newFakeStash(t)
	l := newTestLedger(t)
	fs := vfs.NewMem("/incoming/x.mp4")
	mover := testMover(t, fs)

	plan := onePlan("1", "f1", "/incoming/x.mp4", "/data/P/2024-01-01_P-T_1080p.mp4")
	report, err := Execute(context.Background(), client, l, plan, mover, Options{Commit: false})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !report.DryRun || report.Succeeded != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(f.moveCalls()) != 0 {
		t.Errorf("dry run must not call moveFiles, got %v", f.moveCalls())
	}
	entries, err := l.Mutations(context.Background(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run must leave the mutation log empty, got %d entries", len(entries))
	}
}

func TestExecuteCommitMovesAndLogs(t *testing.T) {
	f, client := newFakeStash(t)
	l := newTestLedger(t)
	fs := vfs.NewMem(
		"/incoming/x.mp4",
		"/incoming/x.en.srt",
	)
	mover := testMover(t, fs)

	plan := onePlan("1", "f1", "/incoming/x.mp4", "/data/P/2024-01-01_P-T_1080p.mp4")
	report, err := Execute(context.Background(), client, l, plan, mover, Options{Commit: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if report.DryRun || report.Succeeded != 1 || report.Failed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.SidecarsMoved != 1 {
		t.Fatalf("report.SidecarsMoved = %d, want 1: %+v", report.SidecarsMoved, report)
	}

	calls := f.moveCalls()
	if len(calls) != 1 || calls[0].FileID != "f1" || calls[0].Folder != "/data/P" || calls[0].Basename != "2024-01-01_P-T_1080p.mp4" {
		t.Fatalf("moveFiles calls = %+v", calls)
	}
	if !fs.Files["/data/P/2024-01-01_P-T_1080p.en.srt"] {
		t.Errorf("sidecar did not follow the video. fs:\n%s", fs)
	}

	entries, err := l.Mutations(context.Background(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	var kinds []string
	for i := len(entries) - 1; i >= 0; i-- {
		kinds = append(kinds, entries[i].Kind)
		if entries[i].OK == nil || !*entries[i].OK {
			t.Errorf("mutation %+v not marked ok", entries[i])
		}
	}
	if len(kinds) != 2 || kinds[0] != "move" || kinds[1] != "sidecar" {
		t.Fatalf("mutation kinds = %v, want [move sidecar]", kinds)
	}
}

func TestExecuteLogsFailedMove(t *testing.T) {
	f, client := newFakeStash(t)
	f.failOn["moveFiles"] = "disk full"
	l := newTestLedger(t)
	fs := vfs.NewMem("/incoming/x.mp4")
	mover := testMover(t, fs)

	plan := onePlan("1", "f1", "/incoming/x.mp4", "/data/P/2024-01-01_P-T_1080p.mp4")
	report, err := Execute(context.Background(), client, l, plan, mover, Options{Commit: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if report.Failed != 1 || report.Succeeded != 0 {
		t.Fatalf("report = %+v", report)
	}

	entries, err := l.Mutations(context.Background(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(entries) != 1 || entries[0].Kind != "move" || entries[0].OK == nil || *entries[0].OK {
		t.Fatalf("entries = %+v, want one failed move mutation", entries)
	}
}

func TestExecuteNilDependenciesError(t *testing.T) {
	l := newTestLedger(t)
	_, client := newFakeStash(t)
	if _, err := Execute(context.Background(), nil, l, &ApplyPlan{}, nil, Options{}); err == nil {
		t.Error("want error for nil client")
	}
	if _, err := Execute(context.Background(), client, nil, &ApplyPlan{}, nil, Options{}); err == nil {
		t.Error("want error for nil ledger")
	}
	if _, err := Execute(context.Background(), client, l, nil, nil, Options{}); err == nil {
		t.Error("want error for nil plan")
	}
}

func TestExecuteRunKindIsFile(t *testing.T) {
	_, client := newFakeStash(t)
	l := newTestLedger(t)
	fs := vfs.NewMem("/incoming/x.mp4")
	mover := testMover(t, fs)

	plan := onePlan("1", "f1", "/incoming/x.mp4", "/data/P/2024-01-01_P-T_1080p.mp4")
	report, err := Execute(context.Background(), client, l, plan, mover, Options{Commit: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	run, err := l.LastRunOfKind(context.Background(), "file")
	if err != nil {
		t.Fatalf("LastRunOfKind: %v", err)
	}
	if run == nil || run.ID != report.RunID {
		t.Fatalf("run = %+v, report.RunID = %d", run, report.RunID)
	}
}

func TestBuildApplyPlanFiltersByStatus(t *testing.T) {
	entries := []Entry{
		{SceneID: "1", Status: "move"},
		{SceneID: "2", Status: "rename"},
		{SceneID: "3", Status: "already_correct"},
		{SceneID: "4", Status: "conflict"},
		{SceneID: "5", Status: "skip_no_metadata"},
	}
	plan := BuildApplyPlan(entries)
	if len(plan.Moves) != 1 || plan.Moves[0].SceneID != "1" {
		t.Errorf("Moves = %+v", plan.Moves)
	}
	if len(plan.Renames) != 1 || plan.Renames[0].SceneID != "2" {
		t.Errorf("Renames = %+v", plan.Renames)
	}
}
