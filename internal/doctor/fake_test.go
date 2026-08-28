package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	stash "github.com/Anastylosis/stash-go"
)

// fakeStash is a tiny GraphQL server keyed on the operation the request
// selects — just enough of Stash's API for doctor's tests: stats (Check's
// Age comparison), and metadataGenerate/findJob/findScene (GeneratePhashes).
type fakeStash struct {
	mu sync.Mutex

	sceneCount int
	statsErr   bool

	jobID         string
	pollsBefore   int // how many RUNNING polls findJob answers before FINISHED
	polls         int
	jobFails      bool
	jobMissing    bool
	generateFails bool

	scenes map[string]stash.Scene

	calls []string
}

func newFakeStash(t *testing.T) (*fakeStash, *stash.Client) {
	t.Helper()
	f := &fakeStash{jobID: "job-1", scenes: map[string]stash.Scene{}}
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
	f.calls = append(f.calls, op)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, f.answer(op, req.Variables))
}

func opOf(query string) string {
	for _, probe := range []struct{ needle, op string }{
		{"stats {", "stats"},
		{"metadataGenerate(input:", "metadataGenerate"},
		{"findJob(input:", "findJob"},
		{"findScene(id:", "findScene"},
	} {
		if strings.Contains(query, probe.needle) {
			return probe.op
		}
	}
	return "unknown"
}

func (f *fakeStash) answer(op string, vars map[string]any) string {
	switch op {
	case "stats":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.statsErr {
			return `{"errors":[{"message":"stats: boom"}]}`
		}
		return fmt.Sprintf(`{"data":{"stats":{"scene_count":%d}}}`, f.sceneCount)
	case "metadataGenerate":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.generateFails {
			return `{"errors":[{"message":"metadataGenerate: boom"}]}`
		}
		return fmt.Sprintf(`{"data":{"metadataGenerate":%q}}`, f.jobID)
	case "findJob":
		return f.findJob()
	case "findScene":
		return f.findScene(vars)
	default:
		return `{"data":{}}`
	}
}

func (f *fakeStash) findJob() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	if f.jobMissing {
		return `{"data":{"findJob":null}}`
	}
	if f.jobFails {
		return fmt.Sprintf(`{"data":{"findJob":{"id":%q,"status":"FAILED","error":"boom"}}}`, f.jobID)
	}
	if f.polls <= f.pollsBefore {
		return fmt.Sprintf(`{"data":{"findJob":{"id":%q,"status":"RUNNING","progress":0.5}}}`, f.jobID)
	}
	return fmt.Sprintf(`{"data":{"findJob":{"id":%q,"status":"FINISHED","progress":1}}}`, f.jobID)
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

func (f *fakeStash) pollCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}
