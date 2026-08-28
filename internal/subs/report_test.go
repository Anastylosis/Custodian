package subs

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// WriteJSON is the corpus-replay contract for subtitlematch's regression
// tests (see subtitlematch's corpus_test.go): every field a replay needs to
// rebuild a SceneRef/Subtitle pair — including scene date/studio/performers,
// the level-5 date signal cannot otherwise be exercised — has to round-trip
// byte-for-byte through the JSON encoding.
func TestWriteJSONRoundTripsSceneAndSubtitleFields(t *testing.T) {
	matches := []Match{{
		Subtitle: Subtitle{
			Path:    "/loose/some.file.srt",
			Stem:    "some.file",
			Runtime: 61 * time.Minute,
			Lang:    "en",
			Dirs:    []string{"Performer Name"},
		},
		Verdict: Confirmed,
		Candidates: []Candidate{{
			Scene: SceneRef{
				ID:         "42",
				Title:      "Some Scene",
				Date:       "2024-01-02",
				Studio:     "Some Studio",
				Performers: []string{"Performer Name", "Second Performer"},
				Path:       "/stash/video/Some.Scene.mp4",
				Stem:       "Some.Scene",
				Duration:   61 * time.Minute,
			},
			Score:   187.5,
			Delta:   0,
			Reasons: []string{"filename match", "date 2024-01-02"},
		}},
	}}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, matches, Summarize(matches, 0)); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var out struct {
		Matches []struct {
			Subtitle string   `json:"subtitle"`
			Stem     string   `json:"stem"`
			Dirs     []string `json:"dirs"`
			Cands    []struct {
				Stem       string   `json:"stem"`
				Date       string   `json:"date"`
				Studio     string   `json:"studio"`
				Performers []string `json:"performers"`
			} `json:"candidates"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}

	if len(out.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(out.Matches))
	}
	m := out.Matches[0]
	if m.Stem != "some.file" {
		t.Errorf("subtitle stem = %q, want %q", m.Stem, "some.file")
	}
	if len(m.Dirs) != 1 || m.Dirs[0] != "Performer Name" {
		t.Errorf("dirs = %v, want [Performer Name]", m.Dirs)
	}
	if len(m.Cands) != 1 {
		t.Fatalf("candidates = %d, want 1", len(m.Cands))
	}
	c := m.Cands[0]
	if c.Stem != "Some.Scene" {
		t.Errorf("candidate stem = %q, want %q", c.Stem, "Some.Scene")
	}
	if c.Date != "2024-01-02" {
		t.Errorf("candidate date = %q, want %q", c.Date, "2024-01-02")
	}
	if c.Studio != "Some Studio" {
		t.Errorf("candidate studio = %q, want %q", c.Studio, "Some Studio")
	}
	if len(c.Performers) != 2 || c.Performers[0] != "Performer Name" {
		t.Errorf("candidate performers = %v, want [Performer Name Second Performer]", c.Performers)
	}
}

// Optional scene fields must not clutter output when the scene simply lacks
// them — an unstudio'd, unperformed scene is common, not an error.
func TestWriteJSONOmitsEmptyOptionalCandidateFields(t *testing.T) {
	matches := []Match{{
		Subtitle: Subtitle{Path: "/loose/x.srt", Stem: "x"},
		Verdict:  Unmatched,
		Candidates: []Candidate{{
			Scene: SceneRef{ID: "1", Path: "/stash/video/x.mp4", Stem: "x"},
		}},
	}}

	var buf bytes.Buffer
	if err := WriteJSON(&buf, matches, Summarize(matches, 0)); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	for _, field := range []string{`"date"`, `"studio"`, `"performers"`, `"dirs"`} {
		if bytes.Contains(buf.Bytes(), []byte(field)) {
			t.Errorf("output contains %s despite an empty value:\n%s", field, buf.String())
		}
	}
}

func TestSummarizeCountsByVerdict(t *testing.T) {
	matches := []Match{
		{Verdict: Confirmed}, {Verdict: Confirmed},
		{Verdict: Likely},
		{Verdict: Ambiguous},
		{Verdict: Unmatched},
	}
	got := Summarize(matches, 3)
	want := Summary{Total: 8, Attached: 3, Confirmed: 2, Likely: 1, Ambiguous: 1, Unmatched: 1}
	if got != want {
		t.Errorf("Summarize = %+v, want %+v", got, want)
	}
}

// PrintReport is read by a human deciding what to do about AMBIGUOUS rows, so
// the sections, ordering and per-candidate detail all matter.
func TestPrintReportOrdersStrongestVerdictsFirst(t *testing.T) {
	matches := []Match{
		{
			Subtitle: Subtitle{Path: "/loose/unmatched.srt", Lang: "en"},
			Verdict:  Unmatched,
		},
		{
			Subtitle: Subtitle{Path: "/loose/confirmed.srt", Lang: "en", Runtime: 90 * time.Minute},
			Verdict:  Confirmed,
			Candidates: []Candidate{{
				Scene:   SceneRef{ID: "1", Path: "/stash/A.mp4", Duration: 90 * time.Minute},
				Score:   200,
				Reasons: []string{"filename match"},
			}},
		},
	}
	sum := Summarize(matches, 1)

	var buf bytes.Buffer
	if err := PrintReport(&buf, matches, sum, 5); err != nil {
		t.Fatalf("PrintReport: %v", err)
	}
	out := buf.String()

	confirmedAt := bytes.Index(buf.Bytes(), []byte("--- CONFIRMED ---"))
	unmatchedAt := bytes.Index(buf.Bytes(), []byte("--- UNMATCHED ---"))
	if confirmedAt < 0 || unmatchedAt < 0 || confirmedAt > unmatchedAt {
		t.Errorf("CONFIRMED section did not come before UNMATCHED:\n%s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("already attached: 1")) {
		t.Errorf("missing already-attached line:\n%s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("confirmed.srt")) || !bytes.Contains(buf.Bytes(), []byte("filename match")) {
		t.Errorf("missing candidate detail:\n%s", out)
	}
}

func TestPrintReportShowsAPlaceholderLangAndNoCandidate(t *testing.T) {
	matches := []Match{{
		Subtitle: Subtitle{Path: "/loose/x.srt"}, // no Lang detected
		Verdict:  Unmatched,
	}}
	sum := Summarize(matches, 0)

	var buf bytes.Buffer
	if err := PrintReport(&buf, matches, sum, 5); err != nil {
		t.Fatalf("PrintReport: %v", err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("lang ??")) {
		t.Errorf("missing placeholder language:\n%s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("(no candidate)")) {
		t.Errorf("missing no-candidate marker:\n%s", out)
	}
}

// maxCandidates caps how many rivals are shown per subtitle, so a scene with
// many close candidates does not flood the report.
func TestPrintReportCapsCandidatesShown(t *testing.T) {
	m := Match{
		Subtitle: Subtitle{Path: "/loose/x.srt", Lang: "en"},
		Verdict:  Ambiguous,
		Candidates: []Candidate{
			{Scene: SceneRef{ID: "1", Path: "/a.mp4"}, Score: 3},
			{Scene: SceneRef{ID: "2", Path: "/b.mp4"}, Score: 2},
			{Scene: SceneRef{ID: "3", Path: "/c.mp4"}, Score: 1},
		},
	}
	sum := Summarize([]Match{m}, 0)

	var buf bytes.Buffer
	if err := PrintReport(&buf, []Match{m}, sum, 2); err != nil {
		t.Fatalf("PrintReport: %v", err)
	}
	out := buf.String()
	if bytes.Contains(buf.Bytes(), []byte("#3")) {
		t.Errorf("expected only 2 candidates shown, found a 3rd:\n%s", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("#1")) || !bytes.Contains(buf.Bytes(), []byte("#2")) {
		t.Errorf("missing an expected candidate:\n%s", out)
	}
}
