package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// gqlRequest is the shape of a request body this test decodes; stash-go
// does not export its own request type.
type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fakeStash answers findScenes and version with fixed pages, recording every
// findScenes request it receives. failOnCall, if set, answers that 1-indexed
// findScenes call with a GraphQL error instead of a page.
type fakeStash struct {
	pages      []string // one findScenes response body per call, last one repeats
	calls      []gqlRequest
	failOnCall int
}

func (f *fakeStash) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req gqlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.Contains(req.Query, "version"):
		fmt.Fprint(w, `{"data":{"version":{"version":"v0.31.1"}}}`)
	case strings.Contains(req.Query, "findScenes"):
		f.calls = append(f.calls, req)
		n := len(f.calls)
		if f.failOnCall > 0 && n == f.failOnCall {
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
			return
		}
		i := n - 1
		if i >= len(f.pages) {
			i = len(f.pages) - 1
		}
		fmt.Fprint(w, f.pages[i])
	default:
		http.Error(w, "unexpected query: "+req.Query, http.StatusBadRequest)
	}
}

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(filepath.Join(t.TempDir(), "custodian.ledger.sqlite"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func scenePage(count int, ids ...string) string {
	scenes := ""
	for _, id := range ids {
		if scenes != "" {
			scenes += ","
		}
		scenes += fmt.Sprintf(`{"id":%q,"title":"scene %s","updated_at":"2024-0%s-01 00:00:00","files":[]}`, id, id, id)
	}
	return fmt.Sprintf(`{"data":{"findScenes":{"count":%d,"scenes":[%s]}}}`, count, scenes)
}

func TestSyncFullTombstonesGoneScenes(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(2, "1", "2")}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	ctx := context.Background()

	// Seed the ledger with a scene that will NOT appear in the full sync.
	seedRun, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun (seed): %v", err)
	}
	if err := l.UpsertScenes(ctx, seedRun, []stash.Scene{
		{ID: "99", Title: "vanishing", UpdatedAt: "2023-01-01 00:00:00"},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	client := stash.NewClient(srv.URL)
	report, err := Sync(ctx, client, l, Options{Full: true, PerPage: 100})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if report.Kind != "full" {
		t.Errorf("Kind = %q, want full", report.Kind)
	}
	if report.Scenes != 2 {
		t.Errorf("Scenes = %d, want 2", report.Scenes)
	}
	if report.Gone != 1 {
		t.Errorf("Gone = %d, want 1 (scene 99 should be tombstoned)", report.Gone)
	}

	var goneRun *int64
	if err := l.DB().QueryRow(`SELECT gone_run FROM scene WHERE id = '99'`).Scan(&goneRun); err != nil {
		t.Fatalf("reading gone_run: %v", err)
	}
	if goneRun == nil {
		t.Error("scene 99 was not tombstoned")
	}
}

func TestSyncIncrementalPassesUpdatedAfter(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(1, "5")}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	ctx := context.Background()

	// Seed a scene so NewestUpdatedAt is non-empty, forcing incremental mode.
	seedRun, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun (seed): %v", err)
	}
	if err := l.UpsertScenes(ctx, seedRun, []stash.Scene{
		{ID: "1", UpdatedAt: "2024-06-15 00:00:00"},
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	client := stash.NewClient(srv.URL)
	report, err := Sync(ctx, client, l, Options{PerPage: 100})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if report.Kind != "incremental" {
		t.Errorf("Kind = %q, want incremental", report.Kind)
	}
	if report.Since != "2024-06-15 00:00:00" {
		t.Errorf("Since = %q, want %q", report.Since, "2024-06-15 00:00:00")
	}
	if report.Gone != 0 {
		t.Errorf("Gone = %d, want 0 (incremental sync never tombstones)", report.Gone)
	}

	if len(f.calls) != 1 {
		t.Fatalf("findScenes calls = %d, want 1", len(f.calls))
	}
	sceneFilter, ok := f.calls[0].Variables["scene_filter"].(map[string]any)
	if !ok {
		t.Fatalf("scene_filter not sent: %+v", f.calls[0].Variables)
	}
	updatedAt, ok := sceneFilter["updated_at"].(map[string]any)
	if !ok {
		t.Fatalf("scene_filter.updated_at not sent: %+v", sceneFilter)
	}
	if updatedAt["value"] != "2024-06-15 00:00:00" {
		t.Errorf("updated_at.value = %v, want 2024-06-15 00:00:00", updatedAt["value"])
	}
}

func TestSyncEmptyLedgerBehavesAsFull(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(0)}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	client := stash.NewClient(srv.URL)

	report, err := Sync(context.Background(), client, l, Options{PerPage: 100})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if report.Kind != "full" {
		t.Errorf("Kind = %q, want full (empty ledger has no high-water mark)", report.Kind)
	}

	if _, ok := f.calls[0].Variables["scene_filter"]; ok {
		t.Errorf("scene_filter should be omitted on a full sync with no other filters, got %+v", f.calls[0].Variables)
	}
}

func TestSyncMidPageErrorStillFinishesTheRun(t *testing.T) {
	f := &fakeStash{
		pages:      []string{scenePage(2, "1")}, // one scene fills the page, so a second page is fetched
		failOnCall: 2,
	}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	ctx := context.Background()
	client := stash.NewClient(srv.URL)

	report, err := Sync(ctx, client, l, Options{PerPage: 1})
	if err == nil {
		t.Fatal("Sync should return the mid-page error")
	}
	if report.Scenes != 1 {
		t.Errorf("Scenes = %d, want 1 (page 1 succeeded before the failure)", report.Scenes)
	}

	run, rerr := l.LastRun(ctx)
	if rerr != nil {
		t.Fatalf("LastRun: %v", rerr)
	}
	if run == nil || run.FinishedAt == "" {
		t.Fatal("the run row should be finished even though the sync errored")
	}
	if run.ScenesSeen != 1 {
		t.Errorf("run.ScenesSeen = %d, want 1", run.ScenesSeen)
	}
	if run.Notes == "" {
		t.Error("run.Notes should record the error")
	}
}

func TestSyncCallsProgress(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(2, "1", "2")}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	client := stash.NewClient(srv.URL)

	var done, total int
	calls := 0
	_, err := Sync(context.Background(), client, l, Options{PerPage: 100, Progress: func(d, tot int) {
		calls++
		done, total = d, tot
	}})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Progress calls = %d, want 1", calls)
	}
	if done != 2 || total != 2 {
		t.Errorf("Progress(done, total) = (%d, %d), want (2, 2)", done, total)
	}
}

func TestSyncUsesDefaultPerPage(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(0)}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	client := stash.NewClient(srv.URL)

	if _, err := Sync(context.Background(), client, l, Options{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("findScenes calls = %d, want 1", len(f.calls))
	}
	findFilter, ok := f.calls[0].Variables["filter"].(map[string]any)
	if !ok {
		t.Fatalf("filter not sent: %+v", f.calls[0].Variables)
	}
	if findFilter["per_page"] != float64(defaultPerPage) {
		t.Errorf("per_page = %v, want %d", findFilter["per_page"], defaultPerPage)
	}
}

func TestSyncRecordsErrorWhenTombstoningFails(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(1, "1")}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	ctx := context.Background()
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE file`); err != nil {
		t.Fatalf("dropping file: %v", err)
	}
	client := stash.NewClient(srv.URL)

	if _, err := Sync(ctx, client, l, Options{Full: true, PerPage: 100}); err == nil {
		t.Fatal("Sync should fail when tombstoning fails")
	}
}

func TestSyncFailsWhenNewestUpdatedAtErrors(t *testing.T) {
	f := &fakeStash{pages: []string{scenePage(0)}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	l := newTestLedger(t)
	ctx := context.Background()
	if _, err := l.DB().ExecContext(ctx, `DROP TABLE scene`); err != nil {
		t.Fatalf("dropping scene: %v", err)
	}
	client := stash.NewClient(srv.URL)

	if _, err := Sync(ctx, client, l, Options{PerPage: 100}); err == nil {
		t.Fatal("Sync should fail when NewestUpdatedAt errors")
	}
}

func TestSyncFailsWhenVersionErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	l := newTestLedger(t)
	client := stash.NewClient(srv.URL)
	if _, err := Sync(context.Background(), client, l, Options{PerPage: 100}); err == nil {
		t.Fatal("Sync should fail when the version query errors")
	}
}
