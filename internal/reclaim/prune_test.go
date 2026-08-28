package reclaim

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// quarantined seeds a group `apply` has already acted on, with the applying
// run backdated by age so the retention window can be exercised.
func quarantinedGroup(t *testing.T, l *ledger.Ledger, signature, keeper string, scenes []stash.Scene, age time.Duration) {
	t.Helper()
	ctx := t.Context()
	seedGroups(t, l, planned{signature, keeper, scenes})

	runID, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	when := time.Now().UTC().Add(-age).Format(time.RFC3339)
	if _, err := l.DB().ExecContext(ctx, `UPDATE run SET started_at = ? WHERE id = ?`, when, runID); err != nil {
		t.Fatalf("backdating the apply run: %v", err)
	}
	if err := l.SetOutcome(ctx, signature, ledger.VerdictQuarantined, runID); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
}

// oldQuarantine is the shape every prune test starts from: a group
// quarantined 60 days ago whose loser still carries the tag.
func oldQuarantine(t *testing.T) (*fakeStash, *stash.Client, *ledger.Ledger) {
	t.Helper()
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/_quarantine/worse.mp4", 2<<30, withTag(DefaultTag)),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	quarantinedGroup(t, l, "sig-1", "10", scenes, 60*24*time.Hour)
	return f, c, l
}

func TestPruneDryRunSendsNoMutations(t *testing.T) {
	f, c, l := oldQuarantine(t)

	rep, err := Prune(t.Context(), c, l, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := f.writes(); len(got) != 0 {
		t.Errorf("a dry run sent mutations: %v", got)
	}
	if kinds := mutationKinds(t, l); len(kinds) != 0 {
		t.Errorf("a dry run wrote to the mutation log: %v", kinds)
	}
	if done, _, _, bytes := rep.Counts(); done != 1 || bytes != 2<<30 {
		t.Errorf("Counts() = %d / %d bytes", done, bytes)
	}
	out := rep.String()
	for _, want := range []string{"DRY RUN", "would sceneMerge 11 → 10", "delete:  /data/_quarantine/worse.mp4"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictQuarantined)
}

func TestPruneMergesThenDeletes(t *testing.T) {
	f, c, l := oldQuarantine(t)

	if _, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()}); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	want := []string{"backupDatabase", "sceneMerge", "deleteFiles"}
	if got := f.writes(); !slices.Equal(got, want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}

	merge, _ := f.input(t, "sceneMerge", 0)["input"].(map[string]any)
	if merge["destination"] != "10" {
		t.Errorf("merge destination = %v", merge["destination"])
	}
	if src, _ := merge["source"].([]any); len(src) != 1 || src[0] != "11" {
		t.Errorf("merge source = %v", merge["source"])
	}
	// The whole point of merging before deleting is that play history follows.
	if merge["play_history"] != true || merge["o_history"] != true {
		t.Errorf("merge history flags = %v / %v", merge["play_history"], merge["o_history"])
	}

	del := f.input(t, "deleteFiles", 0)
	if ids, _ := del["ids"].([]any); len(ids) != 1 || ids[0] != "f11" {
		t.Errorf("deleteFiles ids = %v", del["ids"])
	}

	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup", "merge", "delete"}) {
		t.Errorf("mutation log = %v", kinds)
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictReclaimed)
	assertAllOK(t, l)
}

func TestPruneLeavesAYoungQuarantineAlone(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/_quarantine/worse.mp4", 2<<30, withTag(DefaultTag)),
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	quarantinedGroup(t, l, "sig-1", "10", scenes, 24*time.Hour)

	rep, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(rep.Groups) != 0 {
		t.Errorf("groups = %+v, want none — it is only a day old", rep.Groups)
	}
	if slices.Contains(f.writes(), "sceneMerge") {
		t.Errorf("a one-day-old quarantine was pruned: %v", f.writes())
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictQuarantined)
}

func TestPruneAgeIsConfigurable(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/_quarantine/worse.mp4", 2<<30, withTag(DefaultTag)),
	}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	quarantinedGroup(t, l, "sig-1", "10", scenes, 48*time.Hour)

	rep, err := Prune(t.Context(), c, l, PruneOptions{Age: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(rep.Groups) != 1 {
		t.Errorf("groups = %d, want 1 at --age 24h", len(rep.Groups))
	}
}

func TestPruneSkipsARescuedLoser(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/best.mp4", 4<<30),
		scene("11", "/data/_quarantine/worse.mp4", 2<<30), // the tag was removed by hand
	}
	f, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	quarantinedGroup(t, l, "sig-1", "10", scenes, 60*24*time.Hour)

	rep, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	g := rep.Groups[0]
	if !g.Skipped || !strings.Contains(g.Reason, "rescued") {
		t.Fatalf("group = %+v, want a rescue skip", g)
	}
	if !slices.Contains(g.Rescued, "11") {
		t.Errorf("Rescued = %v", g.Rescued)
	}
	if slices.Contains(f.writes(), "deleteFiles") {
		t.Errorf("a rescued loser's file was deleted: %v", f.writes())
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictQuarantined)
	if !strings.Contains(rep.String(), "SKIPPED") {
		t.Errorf("report does not show the skip:\n%s", rep.String())
	}
}

func TestPruneSkipsAGoneKeeper(t *testing.T) {
	f, c, l := oldQuarantine(t)
	delete(f.scenes, "10")

	rep, err := Prune(t.Context(), c, l, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !rep.Groups[0].Skipped || !strings.Contains(rep.Groups[0].Reason, "keeper 10 is gone") {
		t.Errorf("group = %+v", rep.Groups[0])
	}
}

func TestPruneSkipsWhenTheLosersAreAlreadyGone(t *testing.T) {
	f, c, l := oldQuarantine(t)
	delete(f.scenes, "11")

	rep, err := Prune(t.Context(), c, l, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !rep.Groups[0].Skipped || !strings.Contains(rep.Groups[0].Reason, "nothing left to reclaim") {
		t.Errorf("group = %+v", rep.Groups[0])
	}
}

func TestPruneBackupFailureAbortsBeforeAnyDelete(t *testing.T) {
	f, c, l := oldQuarantine(t)
	f.failOn["backupDatabase"] = "server is unreachable"

	rep, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err == nil {
		t.Fatal("Prune deleted without a backup")
	}
	if got := f.writes(); !slices.Equal(got, []string{"backupDatabase"}) {
		t.Errorf("writes = %v", got)
	}
	if len(rep.Groups) != 0 {
		t.Errorf("groups = %d, want none", len(rep.Groups))
	}
}

func TestPruneRecordsAFailedMerge(t *testing.T) {
	f, c, l := oldQuarantine(t)
	f.failOn["sceneMerge"] = "destination not found"

	rep, err := Prune(t.Context(), c, l, PruneOptions{Commit: true, BackupDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Prune returned an error for a per-group failure: %v", err)
	}
	if len(rep.Groups[0].Errs) == 0 {
		t.Fatal("the failed merge was not reported")
	}
	if slices.Contains(f.writes(), "deleteFiles") {
		t.Error("files were deleted after the merge failed")
	}
	if kinds := mutationKinds(t, l); !slices.Equal(kinds, []string{"backup", "merge"}) {
		t.Errorf("mutation log = %v, want the merge attempt and no delete", kinds)
	}
	assertVerdict(t, l, "sig-1", ledger.VerdictQuarantined)
}

func TestPruneLimit(t *testing.T) {
	scenes := []stash.Scene{
		scene("10", "/data/keep/a.mp4", 4<<30),
		scene("11", "/data/_quarantine/a.mp4", 2<<30, withTag(DefaultTag)),
		scene("20", "/data/keep/b.mp4", 4<<30),
		scene("21", "/data/_quarantine/b.mp4", 1<<30, withTag(DefaultTag)),
	}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	ctx := t.Context()
	seedGroups(t, l, planned{"sig-a", "10", scenes[:2]}, planned{"sig-b", "20", scenes[2:]})
	runID, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	when := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := l.DB().ExecContext(ctx, `UPDATE run SET started_at = ? WHERE id = ?`, when, runID); err != nil {
		t.Fatalf("backdating: %v", err)
	}
	for _, sig := range []string{"sig-a", "sig-b"} {
		if err := l.SetOutcome(ctx, sig, ledger.VerdictQuarantined, runID); err != nil {
			t.Fatalf("SetOutcome: %v", err)
		}
	}

	rep, err := Prune(ctx, c, l, PruneOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(rep.Groups) != 1 {
		t.Errorf("groups = %d, want 1", len(rep.Groups))
	}
}

func TestPruneRejectsMissingDependencies(t *testing.T) {
	if _, err := Prune(context.Background(), nil, nil, PruneOptions{}); err == nil {
		t.Error("Prune with no client or ledger should fail")
	}
}

func TestPruneSummaryAndEmptyReport(t *testing.T) {
	_, c := newFakeStash(t)
	l := newLedger(t)

	rep, err := Prune(t.Context(), c, l, PruneOptions{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !strings.Contains(rep.String(), "would reclaim: 0 groups") {
		t.Errorf("empty report:\n%s", rep.String())
	}
	if !strings.Contains(rep.Summary(), "reclaimed 0") {
		t.Errorf("Summary() = %q", rep.Summary())
	}
}

func TestFormatAge(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"720h", "30d"},
		{"36h", "36h0m0s"},
		{"1h", "1h0m0s"},
	} {
		d, err := time.ParseDuration(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatAge(d); got != tc.want {
			t.Errorf("formatAge(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
