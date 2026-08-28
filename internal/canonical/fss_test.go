package canonical

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
)

// newFSSDB builds a minimal FSS-schema sqlite db (the `scenes` table's
// title/date/duration/deleted_at columns — see fss/docs/usage.md) at path,
// with one row per given fixture.
func newFSSDB(t *testing.T, rows ...fssRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fss.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening fss test db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Exec(`CREATE TABLE scenes (
		title TEXT, date TEXT, duration INTEGER, deleted_at TEXT
	)`); err != nil {
		t.Fatalf("creating scenes table: %v", err)
	}
	for _, r := range rows {
		var duration any
		if r.HasDuration {
			duration = r.Duration
		}
		if _, err := db.Exec(`INSERT INTO scenes(title, date, duration, deleted_at) VALUES(?, ?, ?, NULL)`,
			r.Title, r.Date, duration); err != nil {
			t.Fatalf("inserting fss row: %v", err)
		}
	}
	return path
}

// newFSSJSONDir writes one FSS JSON export file wrapping scenes.
func newFSSJSONDir(t *testing.T, scenes ...fssJSONScene) string {
	t.Helper()
	dir := t.TempDir()
	doc := fssJSONFile{Scenes: scenes}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshalling fss json fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "studio.json"), b, 0o644); err != nil {
		t.Fatalf("writing fss json fixture: %v", err)
	}
	return dir
}

func intp(n int) *int { return &n }

func TestFSSSourceDBMatchByTitleAlone(t *testing.T) {
	db := newFSSDB(t, fssRow{Title: "A Unique Scene Title", Date: "2024-03-01T00:00:00Z", Duration: 612, HasDuration: true})

	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{
		{ID: "1", Title: "A Unique Scene Title", Date: "2024-03-01"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d canonicals, want 1", len(got))
	}
	c := got[0]
	if c.Duration != 612 || c.MatchedBy != "title" || c.Source != "fss" || c.Origin != db {
		t.Errorf("canonical = %+v", c)
	}
	if src.Ambiguous() != 0 {
		t.Errorf("Ambiguous() = %d, want 0", src.Ambiguous())
	}
}

func TestFSSSourceDisambiguatesSharedTitleByDate(t *testing.T) {
	db := newFSSDB(t,
		fssRow{Title: "Common Title", Date: "2023-01-01T00:00:00Z", Duration: 500, HasDuration: true},
		fssRow{Title: "Common Title", Date: "2024-06-15T00:00:00Z", Duration: 700, HasDuration: true},
	)
	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{
		{ID: "1", Title: "Common Title", Date: "2024-06-15"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].Duration != 700 || got[0].MatchedBy != "title+date" {
		t.Fatalf("got %+v, want the 2024-06-15 row via title+date", got)
	}
}

// TestFSSSourceUndatedRowActsAsAWildcardAmongSharedTitles: when several rows
// share a title key, an undated FSS row still counts as agreeing with the
// scene's date, so it can be the one that survives disambiguation.
func TestFSSSourceUndatedRowActsAsAWildcardAmongSharedTitles(t *testing.T) {
	db := newFSSDB(t,
		fssRow{Title: "Common Title", Date: "2023-01-01T00:00:00Z", Duration: 500, HasDuration: true}, // wrong date, excluded
		fssRow{Title: "Common Title", Date: "", Duration: 900, HasDuration: true},                     // undated, survives
	)
	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{
		{ID: "1", Title: "Common Title", Date: "2024-06-15"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].Duration != 900 || got[0].MatchedBy != "title+date" {
		t.Fatalf("got %+v, want the undated row picked as the sole date-agreeing candidate", got)
	}
}

func TestFSSSourceAmbiguousSharedTitleIsSkippedAndCounted(t *testing.T) {
	db := newFSSDB(t,
		fssRow{Title: "Common Title", Date: "2024-06-15T00:00:00Z", Duration: 500, HasDuration: true},
		fssRow{Title: "Common Title", Date: "", Duration: 900, HasDuration: true}, // also agrees (undated)
	)
	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{
		{ID: "1", Title: "Common Title", Date: "2024-06-15"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no canonical (two rows still agree after date filtering)", got)
	}
	if src.Ambiguous() != 1 {
		t.Errorf("Ambiguous() = %d, want 1", src.Ambiguous())
	}
}

func TestFSSSourceNoTitleMatchIsUnmatchedNotAmbiguous(t *testing.T) {
	db := newFSSDB(t, fssRow{Title: "Something Else Entirely", Date: "2024-01-01T00:00:00Z", Duration: 500, HasDuration: true})
	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{{ID: "1", Title: "No Relation", Date: "2024-01-01"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
	if src.Ambiguous() != 0 {
		t.Errorf("Ambiguous() = %d, want 0 (never matched a title at all)", src.Ambiguous())
	}
}

func TestFSSSourceSkipsARowWithNoDuration(t *testing.T) {
	db := newFSSDB(t, fssRow{Title: "Undurationed Scene", Date: "2024-01-01T00:00:00Z"}) // HasDuration false
	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{{ID: "1", Title: "Undurationed Scene", Date: "2024-01-01"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none: a row with no duration has nothing to be canonical", got)
	}
}

func TestFSSSourceDeletedRowsAreExcluded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fss.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE scenes (title TEXT, date TEXT, duration INTEGER, deleted_at TEXT)`); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO scenes(title, date, duration, deleted_at) VALUES('Gone Scene', '2024-01-01T00:00:00Z', 500, '2024-02-01T00:00:00Z')`); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	_ = db.Close()

	src := &FSSSource{DBPath: path}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{{ID: "1", Title: "Gone Scene", Date: "2024-01-01"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want none: deleted_at rows are excluded", got)
	}
}

func TestFSSSourceJSONDirMatchesAndSkipsDeleted(t *testing.T) {
	dir := newFSSJSONDir(t,
		fssJSONScene{Title: "JSON Scene", Date: "2024-05-05T00:00:00Z", Duration: intp(300)},
		fssJSONScene{Title: "Removed Scene", Date: "2024-05-05T00:00:00Z", Duration: intp(400), DeletedAt: "2024-06-01T00:00:00Z"},
	)
	src := &FSSSource{JSONDir: dir}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{
		{ID: "1", Title: "JSON Scene", Date: "2024-05-05"},
		{ID: "2", Title: "Removed Scene", Date: "2024-05-05"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 1 || got[0].SceneID != "1" || got[0].Duration != 300 {
		t.Fatalf("got %+v, want only scene 1 matched (scene 2's row is deleted)", got)
	}
	if got[0].Origin != dir {
		t.Errorf("Origin = %q, want %q", got[0].Origin, dir)
	}
}

func TestFSSSourceReadsBothDBAndJSONWhenBothConfigured(t *testing.T) {
	db := newFSSDB(t, fssRow{Title: "From DB", Date: "2024-01-01T00:00:00Z", Duration: 111, HasDuration: true})
	dir := newFSSJSONDir(t, fssJSONScene{Title: "From JSON", Date: "2024-02-02T00:00:00Z", Duration: intp(222)})

	src := &FSSSource{DBPath: db, JSONDir: dir}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{
		{ID: "1", Title: "From DB", Date: "2024-01-01"},
		{ID: "2", Title: "From JSON", Date: "2024-02-02"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d canonicals, want 2 (one from each source)", len(got))
	}
}

func TestFSSSourceRequiresADBOrJSONDir(t *testing.T) {
	src := &FSSSource{}
	if _, err := src.Lookup(context.Background(), []ledger.SceneRef{{ID: "1", Title: "x"}}); err == nil {
		t.Fatal("Lookup should fail with neither DBPath nor JSONDir set")
	}
}

// TestFSSSourceMatchingIsKeyEqualityOnly documents the deliberate
// performance trade-off: candidatesByTitle is an index lookup by
// textmatch.Key alone, not full textmatch.Equivalent, so a plural/singular
// difference that changes the key ("Scene Titles" vs "Scene Title") does
// not match — a catalogue-wide linear scan per scene doesn't scale to an
// FSS db with hundreds of thousands of rows.
func TestFSSSourceMatchingIsKeyEqualityOnly(t *testing.T) {
	db := newFSSDB(t, fssRow{Title: "Scene Titles", Date: "2024-01-01T00:00:00Z", Duration: 480, HasDuration: true})
	src := &FSSSource{DBPath: db}
	got, err := src.Lookup(context.Background(), []ledger.SceneRef{{ID: "1", Title: "Scene Title", Date: "2024-01-01"}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no match: differing keys are never compared", got)
	}
}

func TestFSSSourceName(t *testing.T) {
	if (&FSSSource{}).Name() != "fss" {
		t.Errorf("Name() = %q, want fss", (&FSSSource{}).Name())
	}
}
