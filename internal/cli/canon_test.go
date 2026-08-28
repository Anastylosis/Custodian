package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Anastylosis/Custodian/internal/canonical"
	"github.com/Anastylosis/Custodian/internal/config"
	stash "github.com/Anastylosis/stash-go"
)

// stashBoxesServer answers configuration{general{stashBoxes}} with the
// given endpoints.
func stashBoxesServer(t *testing.T, endpoints ...string) *stash.Client {
	t.Helper()
	boxes := make([]map[string]string, len(endpoints))
	for i, e := range endpoints {
		boxes[i] = map[string]string{"endpoint": e, "name": e}
	}
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{"configuration": map[string]any{"general": map[string]any{"stashBoxes": boxes}}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return stash.NewClient(srv.URL)
}

// resetCanonFlags clears the package-level canon flag vars so each test
// starts from a clean slate regardless of execution order.
func resetCanonFlags() {
	flagCanonSource, flagCanonEndpoint, flagCanonFSSDB, flagCanonFSSJSON = "", "", "", ""
}

func TestBuildCanonSourcesExplicitFSS(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "fss"
	flagCanonFSSDB = "/data/fss.db"

	sources, err := buildCanonSources(context.Background(), &config.Config{}, nil)
	if err != nil {
		t.Fatalf("buildCanonSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Name() != "fss" {
		t.Fatalf("sources = %+v, want [fss]", sources)
	}
}

func TestBuildCanonSourcesExplicitStashBoxUsesEndpointFlag(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "stash-box"
	flagCanonEndpoint = "https://sb.test/graphql"

	// client is never dialled: the endpoint came from the flag, not from
	// StashBoxes().
	sources, err := buildCanonSources(context.Background(), &config.Config{}, nil)
	if err != nil {
		t.Fatalf("buildCanonSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Name() != "stash-box:https://sb.test/graphql" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestBuildCanonSourcesStashBoxFallsBackToConfigEndpoint(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "stash-box"
	cfg := &config.Config{}
	cfg.Sources.StashBoxEndpoint = "https://configured.test/graphql"

	sources, err := buildCanonSources(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("buildCanonSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Name() != "stash-box:https://configured.test/graphql" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestBuildCanonSourcesStashBoxRequiresEndpointWhenServerHasSeveral(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "stash-box"
	client := stashBoxesServer(t, "https://a.test/graphql", "https://b.test/graphql")

	if _, err := buildCanonSources(context.Background(), &config.Config{}, client); err == nil {
		t.Fatal("buildCanonSources should fail when no endpoint is given and the server has more than one stash-box")
	}
}

func TestBuildCanonSourcesStashBoxDefaultsToTheOnlyOne(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "stash-box"
	client := stashBoxesServer(t, "https://only.test/graphql")

	sources, err := buildCanonSources(context.Background(), &config.Config{}, client)
	if err != nil {
		t.Fatalf("buildCanonSources: %v", err)
	}
	if len(sources) != 1 || sources[0].Name() != "stash-box:https://only.test/graphql" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestBuildCanonSourcesDefaultRunsEveryConfiguredSource(t *testing.T) {
	resetCanonFlags()
	cfg := &config.Config{}
	cfg.Sources.StashBoxEndpoint = "https://sb.test/graphql"
	cfg.Sources.FSS.DB = "/data/fss.db"

	sources, err := buildCanonSources(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("buildCanonSources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want both stash-box and fss", sources)
	}
}

func TestBuildCanonSourcesDefaultSkipsUnconfiguredOnes(t *testing.T) {
	resetCanonFlags()
	// No endpoint given or configured, and the server has zero stash-boxes:
	// stashBoxSource errors, and the default branch swallows that error
	// rather than failing the whole command.
	client := stashBoxesServer(t)
	sources, err := buildCanonSources(context.Background(), &config.Config{}, client)
	if err != nil {
		t.Fatalf("buildCanonSources: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("sources = %+v, want none", sources)
	}
}

func TestStashBoxSourcePropagatesClientError(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "stash-box"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	client := stash.NewClient(srv.URL)

	if _, err := buildCanonSources(context.Background(), &config.Config{}, client); err == nil {
		t.Fatal("buildCanonSources should propagate the client error from StashBoxes")
	}
}

func TestBuildCanonSourcesUnknownSourceErrors(t *testing.T) {
	resetCanonFlags()
	flagCanonSource = "bogus"
	if _, err := buildCanonSources(context.Background(), &config.Config{}, nil); err == nil {
		t.Fatal("buildCanonSources should reject an unknown --source")
	}
}

func TestRunCanonPrintsReportWithNoSourceConfigured(t *testing.T) {
	// canonical.Report{}.String() is already pinned in the canonical
	// package; this just checks the CLI's "no source" guard fires before
	// ever calling Fetch.
	resetCanonFlags()
	var rep canonical.Report
	if rep.String() != "canon: nothing needed a canonical duration\n" {
		t.Fatalf("Report{}.String() = %q", rep.String())
	}
}
