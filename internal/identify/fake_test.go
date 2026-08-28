package identify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// call is one GraphQL operation the fake server answered, in order.
type call struct {
	Op    string
	Input map[string]any
}

// fakeStash answers findScene, sceneUpdate, the URL scraper, scrapeMultiScenes,
// metadataIdentify and findJob — everything Identify touches.
type fakeStash struct {
	mu     sync.Mutex
	scenes map[string]stash.Scene
	calls  []call

	// urlAnswers keys a scrapeSceneURL answer by the url requested. A url
	// missing here answers null, the ordinary "this scraper knows nothing
	// about this page" case.
	urlAnswers map[string]*stash.ScrapedScene

	// multiAnswers computes scrapeMultiScenes' per-scene candidate lists.
	// Defaults to "no candidates for anyone" when nil.
	multiAnswers func(ids []string) [][]stash.ScrapedScene

	// jobStatuses is the sequence FindJob reports for the job started by
	// metadataIdentify, one per call; the last entry repeats after that.
	// Defaults to an immediately-FINISHED job.
	jobStatuses  []stash.JobStatus
	findJobCalls int

	identifyJobID string
	nextJobID     int

	// failOn makes the named operation answer with a GraphQL error.
	failOn map[string]string
	// jobNotFound makes findJob answer "no such job" instead of a status.
	jobNotFound bool
}

func newFakeStash(t *testing.T, scenes ...stash.Scene) (*fakeStash, *stash.Client) {
	t.Helper()
	f := &fakeStash{
		scenes:     make(map[string]stash.Scene, len(scenes)),
		urlAnswers: map[string]*stash.ScrapedScene{},
		failOn:     map[string]string{},
	}
	for _, sc := range scenes {
		f.scenes[sc.ID] = sc
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, stash.NewClient(srv.URL)
}

func (f *fakeStash) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	op := opOf(req.Query)

	f.mu.Lock()
	f.calls = append(f.calls, call{Op: op, Input: req.Variables})
	msg, fail := f.failOn[op]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if fail {
		fmt.Fprintf(w, `{"errors":[{"message":%q}]}`, msg)
		return
	}
	_, _ = io.WriteString(w, f.answer(op, req.Variables))
}

func opOf(query string) string {
	for _, probe := range []struct{ needle, op string }{
		{"findScene(id:", "findScene"},
		{"sceneUpdate(input:", "sceneUpdate"},
		{"scrapeSceneURL(url:", "scrapeSceneURL"},
		{"scrapeMultiScenes(source:", "scrapeMultiScenes"},
		{"metadataIdentify(input:", "metadataIdentify"},
		{"findJob(input:", "findJob"},
	} {
		if strings.Contains(query, probe.needle) {
			return probe.op
		}
	}
	return "unknown"
}

func (f *fakeStash) answer(op string, vars map[string]any) string {
	switch op {
	case "findScene":
		return f.findScene(vars)
	case "sceneUpdate":
		f.applySceneUpdate(vars)
		return `{"data":{"sceneUpdate":{"id":"1"}}}`
	case "scrapeSceneURL":
		return f.scrapeSceneURL(vars)
	case "scrapeMultiScenes":
		return f.scrapeMultiScenes(vars)
	case "metadataIdentify":
		return f.metadataIdentify(vars)
	case "findJob":
		return f.findJob(vars)
	default:
		return `{"data":{}}`
	}
}

func (f *fakeStash) findScene(vars map[string]any) string {
	id, _ := vars["id"].(string)
	f.mu.Lock()
	sc, ok := f.scenes[id]
	f.mu.Unlock()
	if !ok {
		return `{"data":{"findScene":null}}`
	}
	b, err := json.Marshal(map[string]any{"data": map[string]any{"findScene": sc}})
	if err != nil {
		return `{"data":{"findScene":null}}`
	}
	return string(b)
}

// applySceneUpdate mutates the fake's scene state the way a real sceneUpdate
// would, for the fields Identify ever sets.
func (f *fakeStash) applySceneUpdate(vars map[string]any) {
	input, _ := vars["input"].(map[string]any)
	id, _ := input["id"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.scenes[id]
	if !ok {
		return
	}
	if v, ok := input["title"].(string); ok {
		sc.Title = v
	}
	if v, ok := input["code"].(string); ok {
		sc.Code = v
	}
	if v, ok := input["date"].(string); ok {
		sc.Date = v
	}
	if v, ok := input["details"].(string); ok {
		sc.Details = v
	}
	if v, ok := input["director"].(string); ok {
		sc.Director = v
	}
	f.scenes[id] = sc
}

func (f *fakeStash) scrapeSceneURL(vars map[string]any) string {
	url, _ := vars["url"].(string)
	f.mu.Lock()
	scraped := f.urlAnswers[url]
	f.mu.Unlock()
	if scraped == nil {
		return `{"data":{"scrapeSceneURL":null}}`
	}
	b, err := json.Marshal(map[string]any{"data": map[string]any{"scrapeSceneURL": scraped}})
	if err != nil {
		return `{"data":{"scrapeSceneURL":null}}`
	}
	return string(b)
}

func (f *fakeStash) scrapeMultiScenes(vars map[string]any) string {
	input, _ := vars["input"].(map[string]any)
	ids := toStrings(input["scene_ids"])

	f.mu.Lock()
	answerFn := f.multiAnswers
	f.mu.Unlock()

	var results [][]stash.ScrapedScene
	if answerFn != nil {
		results = answerFn(ids)
	} else {
		results = make([][]stash.ScrapedScene, len(ids))
	}
	b, err := json.Marshal(map[string]any{"data": map[string]any{"scrapeMultiScenes": results}})
	if err != nil {
		return `{"data":{"scrapeMultiScenes":[]}}`
	}
	return string(b)
}

func (f *fakeStash) metadataIdentify(vars map[string]any) string {
	f.mu.Lock()
	f.nextJobID++
	id := fmt.Sprintf("job-%d", f.nextJobID)
	f.identifyJobID = id
	f.mu.Unlock()
	return fmt.Sprintf(`{"data":{"metadataIdentify":%q}}`, id)
}

func (f *fakeStash) findJob(vars map[string]any) string {
	f.mu.Lock()
	statuses := f.jobStatuses
	i := f.findJobCalls
	f.findJobCalls++
	notFound := f.jobNotFound
	f.mu.Unlock()

	if notFound {
		return `{"data":{"findJob":null}}`
	}

	status := stash.JobFinished
	if len(statuses) > 0 {
		if i >= len(statuses) {
			i = len(statuses) - 1
		}
		status = statuses[i]
	}
	return fmt.Sprintf(`{"data":{"findJob":{"id":"job","status":%q,"description":"identify"}}}`, status)
}

func toStrings(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// mutationKinds returns the mutation log's kinds, oldest first.
func mutationKinds(t *testing.T, l *ledger.Ledger) []string {
	t.Helper()
	entries, err := l.Mutations(t.Context(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	out := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, entries[i].Kind)
	}
	return out
}

func newLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.Open(t.TempDir() + "/custodian.ledger.sqlite")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// seedScenes upserts scenes into a fresh run so ledger queries can select
// them.
func seedScenes(t *testing.T, l *ledger.Ledger, scenes ...stash.Scene) {
	t.Helper()
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
}
