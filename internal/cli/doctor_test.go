package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/doctor"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
	"github.com/spf13/cobra"
)

func resetDoctorFlags() {
	flagDoctorLedger, flagDoctorSample, flagDoctorPath, flagDoctorJSON = "", 5, "", false
	flagDoctorGenerate, flagDoctorCommit, flagDoctorYes, flagDoctorLimit = false, false, false, 0
}

func newDoctorTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func seedDoctorPhashGap(t *testing.T, l *ledger.Ledger, scenes ...stash.Scene) {
	t.Helper()
	ctx := context.Background()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{ScenesSeen: len(scenes)}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
}

func fileNoPhash(id, path string, size int64) stash.File {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	return stash.File{ID: id, Path: path, Basename: base, Size: size}
}

// doctorFakeStash answers just enough of Stash's API for the generate
// commands: metadataGenerate, findJob (always FINISHED — the polling loop
// itself is internal/doctor's to test) and findScene.
type doctorFakeStash struct {
	scenes map[string]stash.Scene
}

func newDoctorFakeStash(t *testing.T) *stash.Client {
	t.Helper()
	f := &doctorFakeStash{scenes: map[string]stash.Scene{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return stash.NewClient(srv.URL)
}

func (f *doctorFakeStash) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(req.Query, "metadataGenerate(input:"):
		_, _ = io.WriteString(w, `{"data":{"metadataGenerate":"job-1"}}`)
	case strings.Contains(req.Query, "findJob(input:"):
		_, _ = io.WriteString(w, `{"data":{"findJob":{"id":"job-1","status":"FINISHED","progress":1}}}`)
	case strings.Contains(req.Query, "findScene(id:"):
		id, _ := req.Variables["id"].(string)
		sc, ok := f.scenes[id]
		if !ok {
			_, _ = io.WriteString(w, `{"data":{"findScene":null}}`)
			return
		}
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"findScene": sc}})
		_, _ = w.Write(b)
	default:
		_, _ = io.WriteString(w, `{"data":{}}`)
	}
}

func newDoctorTestCmd(in string) (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(in))
	return cmd, &out
}

func TestDoctorPathsUsesFlagPathOverSampling(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)
	flagDoctorPath = "/data/explicit.mp4"

	got, err := doctorPaths(context.Background(), newDoctorTestLedger(t))
	if err != nil {
		t.Fatalf("doctorPaths: %v", err)
	}
	if len(got) != 1 || got[0] != "/data/explicit.mp4" {
		t.Fatalf("doctorPaths = %v, want [/data/explicit.mp4]", got)
	}
}

func TestDoctorPathsSamplesLedgerWhenNoFlag(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)
	flagDoctorSample = 1

	l := newDoctorTestLedger(t)
	seedDoctorPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}})

	got, err := doctorPaths(context.Background(), l)
	if err != nil {
		t.Fatalf("doctorPaths: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("doctorPaths = %v, want 1 path", got)
	}
}

func TestFormatGenerateReportDryRunNothingToDo(t *testing.T) {
	got := formatGenerateReport(doctor.GenerateReport{Count: 0})
	if !strings.Contains(got, "0 file(s)") || !strings.Contains(got, "nothing to do") {
		t.Errorf("formatGenerateReport = %q", got)
	}
}

func TestFormatGenerateReportDryRunWithSamples(t *testing.T) {
	got := formatGenerateReport(doctor.GenerateReport{Count: 3, SamplePaths: []string{"/data/a.mp4", "/data/b.mp4"}})
	for _, want := range []string{"3 file(s)", "/data/a.mp4", "/data/b.mp4", "DRY RUN"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatGenerateReport missing %q, got:\n%s", want, got)
		}
	}
}

func TestFormatGenerateReportCommitted(t *testing.T) {
	got := formatGenerateReport(doctor.GenerateReport{
		Count: 2, Committed: true, JobID: "job-1", JobStatus: "FINISHED", Resynced: 2,
	})
	for _, want := range []string{"job-1", "FINISHED", "resynced: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatGenerateReport missing %q, got:\n%s", want, got)
		}
	}
}

func TestFormatGenerateReportCommittedNothingToGenerate(t *testing.T) {
	got := formatGenerateReport(doctor.GenerateReport{Count: 0, Committed: false})
	if !strings.Contains(got, "nothing to do") {
		t.Errorf("formatGenerateReport = %q", got)
	}
}

func TestConfirmGenerateSummaryCountsFiles(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)
	flagDoctorYes = true

	l := newDoctorTestLedger(t)
	seedDoctorPhashGap(t, l,
		stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}},
		stash.Scene{ID: "2", Files: []stash.File{fileNoPhash("f2", "/data/b.mp4", 1)}},
	)
	cmd, out := newDoctorTestCmd("")

	ok, err := confirmGenerate(context.Background(), cmd, l, 0)
	if err != nil {
		t.Fatalf("confirmGenerate: %v", err)
	}
	if !ok {
		t.Fatal("confirmGenerate = false with --yes set")
	}
	if !strings.Contains(out.String(), "Scenes affected:    2") {
		t.Errorf("confirm summary missing scene count, got:\n%s", out.String())
	}
}

func TestConfirmGenerateRespectsLimit(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)
	flagDoctorYes = true

	l := newDoctorTestLedger(t)
	seedDoctorPhashGap(t, l,
		stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}},
		stash.Scene{ID: "2", Files: []stash.File{fileNoPhash("f2", "/data/b.mp4", 1)}},
	)
	cmd, out := newDoctorTestCmd("")

	if _, err := confirmGenerate(context.Background(), cmd, l, 1); err != nil {
		t.Fatalf("confirmGenerate: %v", err)
	}
	if !strings.Contains(out.String(), "Scenes affected:    1") {
		t.Errorf("confirm summary ignored limit, got:\n%s", out.String())
	}
}

func TestRunDoctorGenerateDryRunPrintsCountAndPaths(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)

	l := newDoctorTestLedger(t)
	seedDoctorPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}})
	client := newDoctorFakeStash(t)
	cmd, out := newDoctorTestCmd("")

	if err := runDoctorGenerate(context.Background(), cmd, l, client); err != nil {
		t.Fatalf("runDoctorGenerate: %v", err)
	}
	if !strings.Contains(out.String(), "1 file(s)") || !strings.Contains(out.String(), "/data/a.mp4") {
		t.Errorf("dry run output missing expected content:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "DRY RUN") {
		t.Errorf("dry run output missing DRY RUN marker:\n%s", out.String())
	}

	entries, err := l.Mutations(context.Background(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %d mutation log entries, want 0", len(entries))
	}
}

func TestRunDoctorGenerateCommitWithYesRunsTheJob(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)
	flagDoctorCommit = true
	flagDoctorYes = true

	l := newDoctorTestLedger(t)
	seedDoctorPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 100)}})
	fake := &doctorFakeStash{scenes: map[string]stash.Scene{
		"1": {ID: "1", Files: []stash.File{{
			ID: "f1", Path: "/data/a.mp4", Basename: "a.mp4", Size: 100,
			Fingerprints: []stash.Fingerprint{{Type: "phash", Value: "abc"}},
		}}},
	}}
	srv := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(srv.Close)
	client := stash.NewClient(srv.URL)

	cmd, out := newDoctorTestCmd("")

	if err := runDoctorGenerate(context.Background(), cmd, l, client); err != nil {
		t.Fatalf("runDoctorGenerate: %v", err)
	}
	got := out.String()
	for _, want := range []string{"job-1", "FINISHED", "resynced: 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("commit output missing %q, got:\n%s", want, got)
		}
	}

	gap, err := l.PhashGap(context.Background())
	if err != nil {
		t.Fatalf("PhashGap: %v", err)
	}
	if gap.Count != 0 {
		t.Errorf("PhashGap after commit = %+v, want none left (resynced with a phash)", gap)
	}
}

func TestRunDoctorGenerateCommitWithoutYesAborts(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)
	flagDoctorCommit = true

	l := newDoctorTestLedger(t)
	seedDoctorPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}})
	client := newDoctorFakeStash(t)
	cmd, out := newDoctorTestCmd("no\n")

	if err := runDoctorGenerate(context.Background(), cmd, l, client); err != nil {
		t.Fatalf("runDoctorGenerate: %v", err)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("expected an abort message, got:\n%s", out.String())
	}

	entries, err := l.Mutations(context.Background(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("aborted commit wrote %d mutation log entries, want 0", len(entries))
	}
}

func TestRunDoctorJSONEmitsReport(t *testing.T) {
	resetDoctorFlags()
	t.Cleanup(resetDoctorFlags)

	l := newDoctorTestLedger(t)
	client := newDoctorFakeStash(t)
	cmd, out := newDoctorTestCmd("")

	rep, err := doctor.Check(context.Background(), client, l, nil)
	if err != nil {
		t.Fatalf("doctor.Check: %v", err)
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output does not decode: %v\n%s", err, out.String())
	}
	if _, ok := got["phash"]; !ok {
		t.Errorf("JSON output missing phash key: %s", out.String())
	}
}
