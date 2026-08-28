package doctor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func seedPhashGap(t *testing.T, l *ledger.Ledger, scenes ...stash.Scene) {
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

func TestGeneratePhashesDryRunSendsNothing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedPhashGap(t, l,
		stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/b.mp4", 1)}},
		stash.Scene{ID: "2", Files: []stash.File{fileNoPhash("f2", "/data/a.mp4", 1)}},
	)
	f, client := newFakeStash(t)

	rep, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: false})
	if err != nil {
		t.Fatalf("GeneratePhashes: %v", err)
	}
	if rep.Count != 2 {
		t.Errorf("Count = %d, want 2", rep.Count)
	}
	if rep.Committed {
		t.Error("Committed = true on a dry run")
	}
	want := []string{"/data/a.mp4", "/data/b.mp4"}
	if len(rep.SamplePaths) != 2 || rep.SamplePaths[0] != want[0] || rep.SamplePaths[1] != want[1] {
		t.Errorf("SamplePaths = %v, want %v", rep.SamplePaths, want)
	}
	if len(f.calls) != 0 {
		t.Errorf("dry run made %d calls to Stash, want 0: %v", len(f.calls), f.calls)
	}

	entries, err := l.Mutations(ctx, 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %d mutation log entries, want 0", len(entries))
	}
}

func TestGeneratePhashesDryRunOnCleanLedger(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	_, client := newFakeStash(t)

	rep, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: false})
	if err != nil {
		t.Fatalf("GeneratePhashes: %v", err)
	}
	if rep.Count != 0 || len(rep.SamplePaths) != 0 {
		t.Errorf("rep = %+v, want zero", rep)
	}
}

func TestGeneratePhashesCommitPollsThenFinishesAndResyncs(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedPhashGap(t, l,
		stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 100)}},
	)
	f, client := newFakeStash(t)
	f.pollsBefore = 2 // two RUNNING polls, then FINISHED
	f.scenes["1"] = stash.Scene{
		ID: "1", UpdatedAt: "2026-01-01T00:00:00Z",
		Files: []stash.File{{
			ID: "f1", Path: "/data/a.mp4", Basename: "a.mp4", Size: 100,
			Fingerprints: []stash.Fingerprint{{Type: "phash", Value: "abc"}},
		}},
	}

	var progressCalls []stash.JobStatus
	rep, err := GeneratePhashes(ctx, client, l, GenerateOptions{
		Commit:       true,
		PollInterval: time.Millisecond,
		Progress:     func(j stash.Job) { progressCalls = append(progressCalls, j.Status) },
	})
	if err != nil {
		t.Fatalf("GeneratePhashes: %v", err)
	}
	if !rep.Committed {
		t.Error("Committed = false on a commit run")
	}
	if rep.JobID != "job-1" {
		t.Errorf("JobID = %q, want job-1", rep.JobID)
	}
	if rep.JobStatus != "FINISHED" {
		t.Errorf("JobStatus = %q, want FINISHED", rep.JobStatus)
	}
	if rep.Resynced != 1 {
		t.Errorf("Resynced = %d, want 1", rep.Resynced)
	}
	if f.pollCount() != 3 {
		t.Errorf("poll count = %d, want 3 (2 RUNNING + 1 FINISHED)", f.pollCount())
	}
	if len(progressCalls) != 3 {
		t.Errorf("progress called %d times, want 3", len(progressCalls))
	}

	// The scene was re-fetched and upserted: its file now has a phash.
	gap, err := l.PhashGap(ctx)
	if err != nil {
		t.Fatalf("PhashGap: %v", err)
	}
	if gap.Count != 0 {
		t.Errorf("PhashGap after resync = %+v, want none left", gap)
	}

	entries, err := l.Mutations(ctx, 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	var found bool
	for _, m := range entries {
		if m.Kind != "generate" {
			continue
		}
		found = true
		if m.OK == nil || !*m.OK {
			t.Errorf("generate mutation OK = %v, want true", m.OK)
		}
		if m.Note != "job-1" {
			t.Errorf("generate mutation note = %q, want job id job-1", m.Note)
		}
		if m.Target != "1 scenes" {
			t.Errorf("generate mutation target = %q, want a count", m.Target)
		}
	}
	if !found {
		t.Error("no 'generate' mutation row was logged")
	}
}

func TestGeneratePhashesCommitOnCleanLedgerSendsNothing(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	f, client := newFakeStash(t)

	rep, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: true})
	if err != nil {
		t.Fatalf("GeneratePhashes: %v", err)
	}
	if rep.Committed {
		t.Error("Committed = true with nothing to generate")
	}
	if len(f.calls) != 0 {
		t.Errorf("made %d calls to Stash with nothing to generate, want 0: %v", len(f.calls), f.calls)
	}
	entries, err := l.Mutations(ctx, 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote %d mutation log entries with nothing to generate, want 0", len(entries))
	}
}

func TestGeneratePhashesCommitJobFails(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}})
	f, client := newFakeStash(t)
	f.jobFails = true

	_, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: true, PollInterval: time.Millisecond})
	if err == nil {
		t.Fatal("GeneratePhashes returned nil error for a FAILED job")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to mention the job's error message", err)
	}

	entries, err := l.Mutations(ctx, 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	var found bool
	for _, m := range entries {
		if m.Kind != "generate" {
			continue
		}
		found = true
		if m.OK == nil || *m.OK {
			t.Errorf("generate mutation OK = %v, want false", m.OK)
		}
	}
	if !found {
		t.Error("no 'generate' mutation row was logged for the failed job")
	}
}

func TestGeneratePhashesStartJobFails(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}})
	f, client := newFakeStash(t)
	f.generateFails = true

	_, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: true})
	if err == nil {
		t.Fatal("GeneratePhashes returned nil error when MetadataGenerate itself failed")
	}

	entries, err := l.Mutations(ctx, 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	var found bool
	for _, m := range entries {
		if m.Kind != "generate" {
			continue
		}
		found = true
		if m.OK == nil || *m.OK {
			t.Errorf("generate mutation OK = %v, want false", m.OK)
		}
	}
	if !found {
		t.Error("no 'generate' mutation row was logged when the job failed to start")
	}
}

func TestWaitForJobAgesOutOfQueue(t *testing.T) {
	ctx := context.Background()
	f, client := newFakeStash(t)
	f.jobMissing = true

	_, err := waitForJob(ctx, client, "job-1", time.Millisecond, nil)
	if err == nil {
		t.Fatal("waitForJob returned nil error for a job that aged out of the queue")
	}
}

func TestPollIntervalDefaultsWhenUnset(t *testing.T) {
	if got := pollInterval(0); got != defaultPollInterval {
		t.Errorf("pollInterval(0) = %v, want %v", got, defaultPollInterval)
	}
	if got := pollInterval(5 * time.Second); got != 5*time.Second {
		t.Errorf("pollInterval(5s) = %v, want 5s", got)
	}
}

func TestGeneratePhashesResyncSkipsSceneGoneFromStash(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedPhashGap(t, l, stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}})
	_, client := newFakeStash(t)
	// f.scenes has no entry for "1" — findScene answers not-found.

	rep, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: true, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("GeneratePhashes: %v", err)
	}
	if rep.Resynced != 0 {
		t.Errorf("Resynced = %d, want 0 (scene is gone from Stash)", rep.Resynced)
	}
}

func TestGeneratePhashesRespectsLimit(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)
	seedPhashGap(t, l,
		stash.Scene{ID: "1", Files: []stash.File{fileNoPhash("f1", "/data/a.mp4", 1)}},
		stash.Scene{ID: "2", Files: []stash.File{fileNoPhash("f2", "/data/b.mp4", 1)}},
		stash.Scene{ID: "3", Files: []stash.File{fileNoPhash("f3", "/data/c.mp4", 1)}},
	)
	_, client := newFakeStash(t)

	rep, err := GeneratePhashes(ctx, client, l, GenerateOptions{Commit: false, Limit: 2})
	if err != nil {
		t.Fatalf("GeneratePhashes: %v", err)
	}
	if rep.Count != 2 {
		t.Errorf("Count = %d, want 2 (limited)", rep.Count)
	}
}
