package reclaim

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

// writeOps are the operations that change something on the server. Anything
// else the fake answers is a read, and a dry run is allowed to make those.
var writeOps = map[string]bool{
	"sceneUpdate":     true,
	"tagCreate":       true,
	"bulkSceneUpdate": true,
	"moveFiles":       true,
	"sceneMerge":      true,
	"deleteFiles":     true,
	"backupDatabase":  true,
}

// fakeStash is a GraphQL server keyed on the field name in the request body.
// It records every operation in order and can be told to fail any of them.
type fakeStash struct {
	mu     sync.Mutex
	scenes map[string]stash.Scene
	calls  []call
	// failOn makes the named operation answer with a GraphQL error.
	failOn map[string]string
	// tagID is what findTags/tagCreate answer with; empty findTags means the
	// tag does not exist yet.
	tagID     string
	tagExists bool
	// backup is the body served for the backup download: a 100-byte SQLite
	// header with a zero page count, which VerifySQLite accepts.
	backupServed bool
}

func newFakeStash(t *testing.T, scenes ...stash.Scene) (*fakeStash, *stash.Client) {
	t.Helper()
	f := &fakeStash{
		scenes: make(map[string]stash.Scene, len(scenes)),
		failOn: map[string]string{},
		tagID:  "tag-1",
	}
	for _, sc := range scenes {
		f.scenes[sc.ID] = sc
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, stash.NewClient(srv.URL)
}

// writes returns the names of the mutating operations only, in order.
func (f *fakeStash) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if writeOps[c.Op] {
			out = append(out, c.Op)
		}
	}
	return out
}

// input returns the variables of the nth call to op, and fails the test if
// there was no such call.
func (f *fakeStash) input(t *testing.T, op string, n int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := 0
	for _, c := range f.calls {
		if c.Op != op {
			continue
		}
		if seen == n {
			return c.Input
		}
		seen++
	}
	t.Fatalf("no call %d to %s; calls were %v", n, op, f.calls)
	return nil
}

func (f *fakeStash) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/graphql") {
		f.mu.Lock()
		f.backupServed = true
		f.mu.Unlock()
		_, _ = w.Write(sqliteHeader())
		return
	}

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

// opOf names the operation from the field the query selects. Keyed on the
// field name rather than parsed, which is all a fake needs.
func opOf(query string) string {
	for _, probe := range []struct{ needle, op string }{
		{"findScene(id:", "findScene"},
		{"sceneUpdate(input:", "sceneUpdate"},
		{"tag_filter: { aliases:", "findTagsByAlias"},
		{"findTags(tag_filter:", "findTags"},
		{"tagCreate(input:", "tagCreate"},
		{"bulkSceneUpdate(input:", "bulkSceneUpdate"},
		{"moveFiles(input:", "moveFiles"},
		{"sceneMerge(input:", "sceneMerge"},
		{"deleteFiles(ids:", "deleteFiles"},
		{"backupDatabase(input:", "backupDatabase"},
		{"systemStatus { os }", "systemStatusOS"},
		{"systemStatus {", "systemStatus"},
		{"version { version", "version"},
		{"stats {", "stats"},
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
	case "findTags":
		if !f.tagExists {
			return `{"data":{"findTags":{"tags":[]}}}`
		}
		return fmt.Sprintf(`{"data":{"findTags":{"tags":[{"id":%q,"name":"t"}]}}}`, f.tagID)
	case "findTagsByAlias":
		return `{"data":{"findTags":{"tags":[]}}}`
	case "tagCreate":
		return fmt.Sprintf(`{"data":{"tagCreate":{"id":%q}}}`, f.tagID)
	case "sceneUpdate":
		return `{"data":{"sceneUpdate":{"id":"1"}}}`
	case "bulkSceneUpdate":
		return `{"data":{"bulkSceneUpdate":[{"id":"1"}]}}`
	case "moveFiles":
		return `{"data":{"moveFiles":true}}`
	case "sceneMerge":
		return `{"data":{"sceneMerge":{"id":"1"}}}`
	case "deleteFiles":
		return `{"data":{"deleteFiles":true}}`
	case "backupDatabase":
		return `{"data":{"backupDatabase":"/downloadBackup?t=1&filename=local.sqlite.85.20260101"}}`
	case "systemStatus":
		return `{"data":{"systemStatus":{"status":"OK","databaseSchema":85,"appSchema":85,"databasePath":"/root/.stash/local.sqlite","configPath":"/root/.stash/config.yml"}}}`
	case "systemStatusOS":
		return `{"data":{"systemStatus":{"os":"linux"}}}`
	case "version":
		return `{"data":{"version":{"version":"v0.31.1","hash":"abc","build_time":"now"}}}`
	case "stats":
		return `{"data":{"stats":{"scene_count":2}}}`
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

// sqliteHeader is a 100-byte SQLite file header whose page count is zero, so
// VerifySQLite accepts the magic and skips the length check.
func sqliteHeader() []byte {
	b := make([]byte, 100)
	copy(b, stash.SQLiteMagic)
	return b
}

// scene builds a one-file scene for the fake to serve.
func scene(id, path string, size int64, opts ...func(*stash.Scene)) stash.Scene {
	sc := stash.Scene{
		ID: id,
		Files: []stash.File{{
			ID: "f" + id, Basename: baseOf(path), Path: path, Size: size,
			Width: 1920, Height: 1080,
		}},
	}
	for _, o := range opts {
		o(&sc)
	}
	return sc
}

func withTag(name string) func(*stash.Scene) {
	return func(sc *stash.Scene) { sc.Tags = append(sc.Tags, stash.Tag{ID: "tag-1", Name: name}) }
}

func withTitle(title string) func(*stash.Scene) {
	return func(sc *stash.Scene) { sc.Title = title }
}

func baseOf(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// planned is one group as `scan` and `plan` would have left it in the
// ledger: a signature, the keeper's scene id, and the member scenes.
type planned struct {
	signature string
	keeper    string
	scenes    []stash.Scene
}

// seedGroups writes scanned-and-planned groups into a fresh ledger in one
// run — ReplaceGroups tombstones anything an earlier run left, so they have
// to arrive together.
func seedGroups(t *testing.T, l *ledger.Ledger, groups ...planned) {
	t.Helper()
	ctx := t.Context()
	runID, err := l.BeginRun(ctx, "full", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}

	var scenes []stash.Scene
	rows := make([]ledger.Group, 0, len(groups))
	for _, g := range groups {
		scenes = append(scenes, g.scenes...)
		ids := make([]string, 0, len(g.scenes))
		for _, sc := range g.scenes {
			ids = append(ids, sc.ID)
		}
		rows = append(rows, ledger.Group{
			Signature: g.signature, SceneIDs: ids,
			Verdict: ledger.VerdictCertain, Reason: "shared stash_id",
		})
	}
	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		t.Fatalf("UpsertScenes: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, rows); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	for _, g := range groups {
		var losers []string
		for _, sc := range g.scenes {
			if sc.ID != g.keeper {
				losers = append(losers, sc.ID)
			}
		}
		if err := l.SetChoice(ctx, g.signature, g.keeper, losers, nil); err != nil {
			t.Fatalf("SetChoice: %v", err)
		}
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
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
