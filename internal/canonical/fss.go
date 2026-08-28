package canonical

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Anastylosis/Custodian/internal/ledger"
	"github.com/Anastylosis/Custodian/internal/textmatch"

	// Registers the "sqlite" driver used to open an FSS db read-only.
	// internal/ledger already imports this; re-importing it here keeps
	// this package buildable and testable on its own.
	_ "modernc.org/sqlite"
)

// FSSSource looks up canonical durations from an FSS catalogue: its SQLite
// db, its JSON export directory, or both. Matching is stash-catalog-sync's
// tested rule (PITFALLS.md, "Matching"): title, corroborated by date — never
// by duration, since the FSS duration is the very canonical being fetched.
type FSSSource struct {
	// DBPath is FSS's sqlite database (schema's `scenes` table). Empty
	// skips it.
	DBPath string
	// JSONDir holds FSS's *.json exports, one file per studio, each
	// {"scenes": [...]}. Empty skips it.
	JSONDir string

	// ambiguous counts, from the most recent Lookup, the scenes whose
	// title matched more than one FSS row and could not be disambiguated
	// by date — read via Ambiguous() after Lookup returns. Pointer
	// receivers throughout so this survives past the call.
	ambiguous int
}

// Name identifies this source in a Fetch report.
func (s *FSSSource) Name() string { return "fss" }

// Ambiguous reports how many scenes the most recent Lookup skipped because
// several FSS rows shared a title key and date could not pick one.
func (s *FSSSource) Ambiguous() int { return s.ambiguous }

// Lookup matches each scene's title (and, when several FSS rows share that
// title, its date) against the catalogue and returns a canonical for every
// scene it could place unambiguously.
func (s *FSSSource) Lookup(ctx context.Context, scenes []ledger.SceneRef) ([]ledger.Canonical, error) {
	s.ambiguous = 0
	rows, err := s.loadRows(ctx)
	if err != nil {
		return nil, err
	}
	index := indexByTitle(rows)
	origin := s.origin()

	var out []ledger.Canonical
	for _, sc := range scenes {
		row, matchedBy, ok := s.matchOne(sc, index)
		if !ok || !row.HasDuration {
			continue
		}
		out = append(out, ledger.Canonical{
			SceneID: sc.ID, Source: "fss", Origin: origin,
			Duration: float64(row.Duration), Title: row.Title, MatchedBy: matchedBy,
		})
	}
	return out, nil
}

// matchOne finds the FSS row for one scene: every row sharing its
// textmatch.Key(title), disambiguated by date when there is more than one.
// Exactly one title match needs no date check (matched_by "title"); several
// need exactly one whose date agrees — equal, or the row undated — else the
// match is ambiguous and skipped.
func (s *FSSSource) matchOne(sc ledger.SceneRef, index map[string][]fssRow) (fssRow, string, bool) {
	titleMatches := candidatesByTitle(sc.Title, index)
	switch len(titleMatches) {
	case 0:
		return fssRow{}, "", false
	case 1:
		return titleMatches[0], "title", true
	}

	dateMatches := filterByDate(titleMatches, sc.Date)
	if len(dateMatches) == 1 {
		return dateMatches[0], "title+date", true
	}
	s.ambiguous++
	return fssRow{}, "", false
}

// origin is the ledger.Canonical.Origin recorded for a match: the
// catalogue location(s) actually read.
func (s *FSSSource) origin() string {
	switch {
	case s.DBPath != "" && s.JSONDir != "":
		return s.DBPath + ";" + s.JSONDir
	case s.DBPath != "":
		return s.DBPath
	default:
		return s.JSONDir
	}
}

func (s *FSSSource) loadRows(ctx context.Context) ([]fssRow, error) {
	if s.DBPath == "" && s.JSONDir == "" {
		return nil, errors.New("canonical: FSSSource needs DBPath or JSONDir")
	}
	var rows []fssRow
	if s.DBPath != "" {
		dbRows, err := loadFromDB(ctx, s.DBPath)
		if err != nil {
			return nil, err
		}
		rows = append(rows, dbRows...)
	}
	if s.JSONDir != "" {
		jsonRows, err := loadFromJSON(s.JSONDir)
		if err != nil {
			return nil, err
		}
		rows = append(rows, jsonRows...)
	}
	return rows, nil
}

// fssRow is one FSS scene as matching needs it: title, date (RFC3339,
// possibly empty — "undated") and duration (seconds, possibly absent).
type fssRow struct {
	Title       string
	Date        string
	Duration    int
	HasDuration bool
}

func loadFromDB(ctx context.Context, path string) ([]fssRow, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("canonical: opening fss db %s: %w", path, err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(ctx, `SELECT title, date, duration FROM scenes WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("canonical: querying fss db %s: %w", path, err)
	}
	defer func() { _ = rows.Close() }()

	var out []fssRow
	for rows.Next() {
		var title, date string
		var duration sql.NullInt64
		if err := rows.Scan(&title, &date, &duration); err != nil {
			return nil, fmt.Errorf("canonical: scanning fss db row: %w", err)
		}
		out = append(out, fssRow{Title: title, Date: date, Duration: int(duration.Int64), HasDuration: duration.Valid})
	}
	return out, rows.Err()
}

// fssJSONFile is the wrapper FSS's JSON export uses: {"studioUrl": ...,
// "scenes": [...]}.
type fssJSONFile struct {
	Scenes []fssJSONScene `json:"scenes"`
}

type fssJSONScene struct {
	Title     string `json:"title"`
	Date      string `json:"date"`
	Duration  *int   `json:"duration"`
	DeletedAt string `json:"deletedAt"`
}

func loadFromJSON(dir string) ([]fssRow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("canonical: reading fss json dir %s: %w", dir, err)
	}

	var out []fssRow
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("canonical: reading %s: %w", path, err)
		}
		var doc fssJSONFile
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("canonical: parsing %s: %w", path, err)
		}
		for _, sc := range doc.Scenes {
			if sc.DeletedAt != "" {
				continue
			}
			row := fssRow{Title: sc.Title, Date: sc.Date}
			if sc.Duration != nil {
				row.Duration, row.HasDuration = *sc.Duration, true
			}
			out = append(out, row)
		}
	}
	return out, nil
}

// indexByTitle buckets rows by textmatch.Key(title) — the fast path for
// candidatesByTitle.
func indexByTitle(rows []fssRow) map[string][]fssRow {
	idx := make(map[string][]fssRow, len(rows))
	for _, r := range rows {
		k := textmatch.Key(r.Title)
		idx[k] = append(idx[k], r)
	}
	return idx
}

// candidatesByTitle returns the rows sharing title's textmatch.Key — an
// index lookup, O(1) per scene.
//
// Exact key equality only: Equivalent's plural/truncation classes need a
// linear scan per miss, which a 128k-row catalogue turns into minutes.
func candidatesByTitle(title string, index map[string][]fssRow) []fssRow {
	return index[textmatch.Key(title)]
}

// filterByDate keeps the rows whose date agrees with sceneDate: equal (by
// the date portion alone — an FSS date is RFC3339, a Stash scene date is
// not), or the row itself is undated.
func filterByDate(rows []fssRow, sceneDate string) []fssRow {
	want := dateOnly(sceneDate)
	var out []fssRow
	for _, r := range rows {
		if r.Date == "" || dateOnly(r.Date) == want {
			out = append(out, r)
		}
	}
	return out
}

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}
