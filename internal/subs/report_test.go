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
