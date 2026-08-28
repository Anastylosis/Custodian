package subs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

// graphqlBody is the minimal shape needed to tell one query from another.
type graphqlBody struct {
	Query string `json:"query"`
}

// fakeStash answers findPerformers and findStudios with one canned page each,
// enough to exercise CreatorNames' two paginated loops and its alias/blank
// handling without a live server.
func fakeStash(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body graphqlBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body.Query, "findPerformers"):
			_, _ = w.Write([]byte(`{"data":{"findPerformers":{"count":2,"performers":[
				{"id":"1","name":"Performer One","alias_list":["P1","",""]},
				{"id":"2","name":"","alias_list":[]}
			]}}}`))
		case strings.Contains(body.Query, "findStudios"):
			_, _ = w.Write([]byte(`{"data":{"findStudios":{"count":1,"studios":[
				{"id":"1","name":"Studio One","aliases":["S1"]}
			]}}}`))
		default:
			t.Fatalf("unexpected query: %s", body.Query)
		}
	}))
}

func TestCreatorNamesUnionsPerformersAndStudiosAndDropsBlanks(t *testing.T) {
	srv := fakeStash(t)
	defer srv.Close()

	names, err := CreatorNames(context.Background(), stash.NewClient(srv.URL))
	if err != nil {
		t.Fatalf("CreatorNames: %v", err)
	}

	want := map[string]bool{"Performer One": true, "P1": true, "Studio One": true, "S1": true}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want exactly %v", names, want)
	}
	for _, n := range names {
		if n == "" {
			t.Fatal("an empty name reached the vocabulary")
		}
		if !want[n] {
			t.Errorf("unexpected name %q", n)
		}
	}
}

func TestCreatorNamesPropagatesAPerformerQueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := CreatorNames(context.Background(), stash.NewClient(srv.URL))
	if err == nil {
		t.Fatal("want an error when the performer query fails")
	}
}
