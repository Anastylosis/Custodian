package identify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	stash "github.com/Anastylosis/stash-go"
)

// --- fillEmptyFields: pure FillEmpty semantics, no server involved ---

func TestFillEmptyFieldsFillsOnlyWhatIsEmpty(t *testing.T) {
	current := Scalars{Title: "", Date: "2024-01-01", Details: ""}
	scraped := Scalars{Title: "Scraped Title", Date: "2099-01-01", Details: "Scraped details"}

	update, fields := fillEmptyFields(current, scraped)

	if len(fields) != 2 || fields[0] != "title" || fields[1] != "details" {
		t.Fatalf("fields = %v, want [title details] (date was already set)", fields)
	}
	if update.Title == nil || *update.Title != "Scraped Title" {
		t.Errorf("Title = %v, want Scraped Title", update.Title)
	}
	if update.Details == nil || *update.Details != "Scraped details" {
		t.Errorf("Details = %v, want Scraped details", update.Details)
	}
	if update.Date != nil {
		t.Errorf("Date = %v, want nil — the scene already had one, FillEmpty never overwrites", update.Date)
	}
}

func TestFillEmptyFieldsWritesNothingWhenSceneIsAlreadyFull(t *testing.T) {
	current := Scalars{Title: "A", Code: "B", Date: "C", Details: "D", Director: "E"}
	scraped := Scalars{Title: "X", Code: "Y", Date: "Z", Details: "W", Director: "V"}

	_, fields := fillEmptyFields(current, scraped)
	if len(fields) != 0 {
		t.Errorf("fields = %v, want none", fields)
	}
}

func TestFillEmptyFieldsWritesNothingWhenScrapeIsEmpty(t *testing.T) {
	current := Scalars{}
	scraped := Scalars{}
	update, fields := fillEmptyFields(current, scraped)
	if len(fields) != 0 || update.Title != nil {
		t.Errorf("fields = %v, update = %+v, want nothing written", fields, update)
	}
}

// --- screenCandidate: the stash-box pre-screen filter table ---

func TestScreenCandidateOneMatchWithinToleranceIsSelected(t *testing.T) {
	sr := screenCandidate("1", 1000, []stash.ScrapedScene{{RemoteSiteID: "sid", Duration: 1010}})
	if !sr.Selected {
		t.Errorf("Selected = false, reason %q, want selected (10s off 1000s is within 5%%/90s)", sr.Reason)
	}
}

func TestScreenCandidateTwoCandidatesAreHeld(t *testing.T) {
	sr := screenCandidate("1", 1000, []stash.ScrapedScene{{Duration: 1000}, {Duration: 1005}})
	if sr.Selected {
		t.Error("Selected = true, want held: ambiguous")
	}
	if !strings.Contains(sr.Reason, "ambiguous") {
		t.Errorf("Reason = %q, want it to mention ambiguous", sr.Reason)
	}
}

func TestScreenCandidateDurationOffToleranceIsHeld(t *testing.T) {
	// 1000s * 5% = 50s tolerance (below the 90s cap); 200s off is well past it.
	sr := screenCandidate("1", 1000, []stash.ScrapedScene{{Duration: 1200}})
	if sr.Selected {
		t.Error("Selected = true, want held: duration mismatch")
	}
	if !strings.Contains(sr.Reason, "duration mismatch") {
		t.Errorf("Reason = %q, want it to mention a duration mismatch", sr.Reason)
	}
}

func TestScreenCandidateNoCandidatesIsHeld(t *testing.T) {
	sr := screenCandidate("1", 1000, nil)
	if sr.Selected || sr.Reason != "no candidate" {
		t.Errorf("sr = %+v, want held: no candidate", sr)
	}
}

func TestScreenCandidateWithinBothBoundsAtTheEdge(t *testing.T) {
	// 90s cap binds before 5% of a long scene would: 3000s * 5% = 150s, so
	// the 90s absolute cap is what actually limits it. 89s off must pass,
	// 91s off must not.
	if sr := screenCandidate("1", 3000, []stash.ScrapedScene{{Duration: 3089}}); !sr.Selected {
		t.Errorf("89s off a 3000s scene should be selected, got held: %s", sr.Reason)
	}
	if sr := screenCandidate("1", 3000, []stash.ScrapedScene{{Duration: 3091}}); sr.Selected {
		t.Error("91s off a 3000s scene should be held, the 90s cap binds")
	}
}

// --- Identify: --via requirement ---

func TestIdentifyRequiresVia(t *testing.T) {
	_, c := newFakeStash(t)
	l := newLedger(t)
	if _, err := Identify(context.Background(), c, l, Options{}); err == nil {
		t.Error("Identify with no --via should fail")
	}
	if _, err := Identify(context.Background(), c, l, Options{Via: "bogus"}); err == nil {
		t.Error("Identify with an unknown --via should fail")
	}
}

func TestIdentifyStashBoxRequiresEndpoint(t *testing.T) {
	_, c := newFakeStash(t)
	l := newLedger(t)
	if _, err := Identify(context.Background(), c, l, Options{Via: "stash-box"}); err == nil {
		t.Error("--via stash-box with no --endpoint should fail")
	}
}

// --- via url ---

func urlScene(id, url, title, date string) stash.Scene {
	return stash.Scene{
		ID: id, Title: title, Date: date, URLs: []string{url},
		Files:     []stash.File{{ID: "f" + id, Basename: id + ".mp4", Path: "/lib/" + id + ".mp4", Size: 10}},
		UpdatedAt: "2024-01-01 00:00:00",
	}
}

func TestIdentifyByURLDryRunWritesNothing(t *testing.T) {
	sc := urlScene("1", "https://example/1", "", "")
	f, c := newFakeStash(t, sc)
	f.urlAnswers["https://example/1"] = &stash.ScrapedScene{Title: "Scraped Title", Date: "2024-05-05"}

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if !rep.DryRun {
		t.Error("DryRun = false, want true")
	}
	if len(rep.URLResults) != 1 || !rep.URLResults[0].Found {
		t.Fatalf("URLResults = %+v, want one found result", rep.URLResults)
	}
	if got := rep.URLResults[0].Wrote; len(got) != 2 || got[0] != "title" || got[1] != "date" {
		t.Errorf("Wrote = %v, want [title date]", got)
	}

	for _, call := range f.calls {
		if call.Op == "sceneUpdate" || call.Op == "metadataIdentify" {
			t.Errorf("dry run made a write call: %s", call.Op)
		}
	}
	if kinds := mutationKinds(t, l); len(kinds) != 0 {
		t.Errorf("mutation log = %v, want empty on a dry run", kinds)
	}
}

func TestIdentifyByURLCommitFillsOnlyEmptyFields(t *testing.T) {
	// Title is empty (fillable); Date is already set (must survive untouched).
	sc := urlScene("1", "https://example/1", "", "2020-01-01")
	f, c := newFakeStash(t, sc)
	f.urlAnswers["https://example/1"] = &stash.ScrapedScene{Title: "Scraped Title", Date: "2099-09-09"}

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Commit: true, Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if rep.DryRun {
		t.Error("DryRun = true on a commit run")
	}

	got, ok := f.scenes["1"]
	if !ok {
		t.Fatal("scene 1 vanished")
	}
	if got.Title != "Scraped Title" {
		t.Errorf("Title = %q, want the scraped title (was empty)", got.Title)
	}
	if got.Date != "2020-01-01" {
		t.Errorf("Date = %q, want the original date untouched (FillEmpty never overwrites)", got.Date)
	}

	kinds := mutationKinds(t, l)
	if len(kinds) != 1 || kinds[0] != "identify:update" {
		t.Fatalf("mutation log = %v, want exactly one identify:update", kinds)
	}
}

func TestIdentifyByURLRemoteSiteIDHandsSceneToMetadataIdentify(t *testing.T) {
	sc := urlScene("1", "https://example/1", "Already Titled", "2020-01-01")
	f, c := newFakeStash(t, sc)
	f.urlAnswers["https://example/1"] = &stash.ScrapedScene{RemoteSiteID: "sb-123"}
	f.jobStatuses = []stash.JobStatus{stash.JobFinished}

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Commit: true, Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if rep.SelectedCount != 1 {
		t.Fatalf("SelectedCount = %d, want 1", rep.SelectedCount)
	}
	if rep.JobStatus != string(stash.JobFinished) {
		t.Errorf("JobStatus = %q, want FINISHED", rep.JobStatus)
	}

	var sawIdentify bool
	for _, call := range f.calls {
		if call.Op != "metadataIdentify" {
			continue
		}
		sawIdentify = true
		input, _ := call.Input["input"].(map[string]any)
		ids := toStrings(input["sceneIDs"])
		if len(ids) != 1 || ids[0] != "1" {
			t.Errorf("metadataIdentify sceneIDs = %v, want [1] (restricted to this scene)", ids)
		}
	}
	if !sawIdentify {
		t.Error("no metadataIdentify call was made")
	}

	kinds := mutationKinds(t, l)
	if len(kinds) != 1 || kinds[0] != "identify:job" {
		t.Fatalf("mutation log = %v, want exactly one identify:job (title was already set, no update needed)", kinds)
	}
}

func TestIdentifyByURLSkipsSceneWithNoScraperMatch(t *testing.T) {
	sc := urlScene("1", "https://example/1", "", "")
	_, c := newFakeStash(t, sc) // no urlAnswers entry: scraper returns null

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(rep.URLResults) != 1 || rep.URLResults[0].Found {
		t.Fatalf("URLResults = %+v, want one not-found result", rep.URLResults)
	}
	if rep.SelectedCount != 0 {
		t.Errorf("SelectedCount = %d, want 0", rep.SelectedCount)
	}
}

func TestIdentifyByURLConsidersOnlyScenesWithAUrlAndNoStashID(t *testing.T) {
	withURLAndStashID := urlScene("1", "https://example/1", "", "")
	withURLAndStashID.StashIDs = []stash.StashID{{Endpoint: "https://sb.test", ID: "x"}}
	noURL := stash.Scene{ID: "2", Files: []stash.File{{ID: "f2", Path: "/lib/2.mp4"}}, UpdatedAt: "2024-01-01 00:00:00"}
	candidate := urlScene("3", "https://example/3", "", "")

	_, c := newFakeStash(t, withURLAndStashID, noURL, candidate)
	l := newLedger(t)
	seedScenes(t, l, withURLAndStashID, noURL, candidate)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if rep.Considered != 1 || len(rep.URLResults) != 1 || rep.URLResults[0].SceneID != "3" {
		t.Fatalf("Identify considered %+v, want only scene 3", rep.URLResults)
	}
}

// --- via stash-box ---

func withDuration(sc stash.Scene, seconds float64) stash.Scene {
	for i := range sc.Files {
		sc.Files[i].Duration = seconds
	}
	return sc
}

func noStashIDScene(id string, seconds float64) stash.Scene {
	sc := stash.Scene{
		ID: id, Files: []stash.File{{ID: "f" + id, Basename: id + ".mp4", Path: "/lib/" + id + ".mp4", Size: 10}},
		UpdatedAt: "2024-01-01 00:00:00",
	}
	return withDuration(sc, seconds)
}

func TestIdentifyByStashBoxSelectsOnlyScreenedMatches(t *testing.T) {
	one := noStashIDScene("1", 1000) // one candidate, within tolerance -> selected
	two := noStashIDScene("2", 1000) // two candidates -> held
	off := noStashIDScene("3", 1000) // one candidate, way off -> held

	f, c := newFakeStash(t, one, two, off)
	f.multiAnswers = func(ids []string) [][]stash.ScrapedScene {
		out := make([][]stash.ScrapedScene, len(ids))
		for i, id := range ids {
			switch id {
			case "1":
				out[i] = []stash.ScrapedScene{{RemoteSiteID: "sid-1", Title: "Match", Duration: 1005}}
			case "2":
				out[i] = []stash.ScrapedScene{{Duration: 1000}, {Duration: 1002}}
			case "3":
				out[i] = []stash.ScrapedScene{{Duration: 2000}}
			}
		}
		return out
	}
	f.jobStatuses = []stash.JobStatus{stash.JobFinished}

	l := newLedger(t)
	seedScenes(t, l, one, two, off)

	rep, err := Identify(context.Background(), c, l, Options{
		Via: "stash-box", Endpoint: "https://sb.test/graphql", Commit: true, Delay: -1,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if rep.Considered != 3 {
		t.Fatalf("Considered = %d, want 3", rep.Considered)
	}
	if rep.SelectedCount != 1 {
		t.Fatalf("SelectedCount = %d, want 1 (only scene 1 passes the pre-screen)", rep.SelectedCount)
	}

	held := map[string]bool{}
	for _, sr := range rep.Screened {
		if !sr.Selected {
			held[sr.SceneID] = true
		}
	}
	if !held["2"] || !held["3"] {
		t.Errorf("held scenes = %v, want 2 and 3 held", held)
	}

	var sawIdentify bool
	for _, call := range f.calls {
		if call.Op != "metadataIdentify" {
			continue
		}
		sawIdentify = true
		input, _ := call.Input["input"].(map[string]any)
		ids := toStrings(input["sceneIDs"])
		if len(ids) != 1 || ids[0] != "1" {
			t.Errorf("metadataIdentify sceneIDs = %v, want [1]", ids)
		}
		sources := input["sources"]
		if sources == nil {
			t.Error("metadataIdentify sources is empty, want the endpoint")
		}
	}
	if !sawIdentify {
		t.Error("no metadataIdentify call was made")
	}
}

func TestIdentifyByStashBoxDryRunMakesNoWrites(t *testing.T) {
	one := noStashIDScene("1", 1000)
	f, c := newFakeStash(t, one)
	f.multiAnswers = func(ids []string) [][]stash.ScrapedScene {
		return [][]stash.ScrapedScene{{{RemoteSiteID: "sid-1", Duration: 1000}}}
	}

	l := newLedger(t)
	seedScenes(t, l, one)

	rep, err := Identify(context.Background(), c, l, Options{
		Via: "stash-box", Endpoint: "https://sb.test/graphql", Delay: -1,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if !rep.DryRun || rep.SelectedCount != 1 {
		t.Fatalf("rep = %+v, want a dry run that would select scene 1", rep)
	}

	for _, call := range f.calls {
		if call.Op == "metadataIdentify" {
			t.Error("dry run called metadataIdentify")
		}
	}
	if kinds := mutationKinds(t, l); len(kinds) != 0 {
		t.Errorf("mutation log = %v, want empty on a dry run", kinds)
	}
}

func TestIdentifyByStashBoxChunksAt25(t *testing.T) {
	var scenes []stash.Scene
	for i := 0; i < 30; i++ {
		scenes = append(scenes, noStashIDScene(string(rune('a'+i)), 1000))
	}
	f, c := newFakeStash(t, scenes...)
	var chunkSizes []int
	f.multiAnswers = func(ids []string) [][]stash.ScrapedScene {
		chunkSizes = append(chunkSizes, len(ids))
		return make([][]stash.ScrapedScene, len(ids))
	}

	l := newLedger(t)
	seedScenes(t, l, scenes...)

	_, err := Identify(context.Background(), c, l, Options{
		Via: "stash-box", Endpoint: "https://sb.test/graphql", Delay: -1,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(chunkSizes) != 2 || chunkSizes[0] != 25 || chunkSizes[1] != 5 {
		t.Errorf("chunk sizes = %v, want [25 5]", chunkSizes)
	}
}

// --- report caveat ---

func TestReportStringAlwaysStatesTheCaveatFirst(t *testing.T) {
	rep := Report{}
	got := rep.String()
	if !strings.HasPrefix(got, "identify: matches are not proof of identity") {
		t.Errorf("String() = %q, want it to open with the false-positive caveat", got)
	}
	if !strings.Contains(got, "Nothing is written without --commit") {
		t.Error("String() should say nothing is written without --commit")
	}
}

func TestReportStringRendersBothVias(t *testing.T) {
	url := Report{Via: "url", Considered: 1, SelectedCount: 1, URLResults: []URLResult{
		{SceneID: "1", Found: true, URL: "https://x", Wrote: []string{"title"}, RemoteSiteID: "sid"},
	}}
	if s := url.String(); !strings.Contains(s, "scene 1") {
		t.Errorf("url report missing scene: %s", s)
	}

	sb := Report{Via: "stash-box", Endpoint: "https://sb.test", Considered: 1, Screened: []ScreenResult{
		{SceneID: "1", Duration: 100, Reason: "no candidate"},
	}}
	if s := sb.String(); !strings.Contains(s, "held: no candidate") {
		t.Errorf("stash-box report missing held reason: %s", s)
	}
}

// --- Report.String(): the remaining branches (selected candidate, error
// result, nothing to fill, empty Via, and the Summary default) ---

func TestReportStringSelectedStashBoxCandidate(t *testing.T) {
	rep := Report{Via: "stash-box", Screened: []ScreenResult{
		{SceneID: "1", Duration: 100, Selected: true, Candidates: []stash.ScrapedScene{{Title: "Match", Duration: 105}}},
	}}
	got := rep.String()
	if !strings.Contains(got, "selected: Match") {
		t.Errorf("String() = %q, want the selected candidate's title", got)
	}
}

func TestReportStringURLErrorResult(t *testing.T) {
	rep := Report{Via: "url", URLResults: []URLResult{{SceneID: "1", Err: "boom"}}}
	got := rep.String()
	if !strings.Contains(got, "error: boom") {
		t.Errorf("String() = %q, want the error line", got)
	}
}

func TestReportStringURLNothingToFill(t *testing.T) {
	rep := Report{Via: "url", URLResults: []URLResult{{SceneID: "1", Found: true, URL: "https://x"}}}
	got := rep.String()
	if !strings.Contains(got, "fills empty: nothing") {
		t.Errorf("String() = %q, want the nothing-to-fill line", got)
	}
}

func TestReportStringEmptyReportStopsAfterCaveat(t *testing.T) {
	got := Report{}.String()
	if !strings.HasPrefix(got, caveat) || got != caveat {
		t.Errorf("String() on a zero Report = %q, want exactly the caveat", got)
	}
}

func TestReportSummaryDefault(t *testing.T) {
	if got := (Report{}).Summary(); got != "identify: nothing to do" {
		t.Errorf("Summary() = %q", got)
	}
}

// --- scrapeURLsForScene: studio/performers on a match, and a scraper error
// that does not stop the walk across a scene's other URLs ---

func TestIdentifyByURLReportsStudioAndPerformers(t *testing.T) {
	sc := stash.Scene{
		ID: "1", URLs: []string{"https://example/1"},
		Files:     []stash.File{{ID: "f1", Path: "/lib/1.mp4"}},
		UpdatedAt: "2024-01-01 00:00:00",
	}
	f, c := newFakeStash(t, sc)
	f.urlAnswers["https://example/1"] = &stash.ScrapedScene{
		Title:  "Scraped",
		Studio: &stash.ScrapedStudio{Name: "Some Studio"},
		Performers: []stash.ScrapedPerformer{
			{Name: "Performer A"}, {Name: "Performer B"},
		},
	}

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(rep.URLResults) != 1 {
		t.Fatalf("URLResults = %+v", rep.URLResults)
	}
	got := rep.URLResults[0]
	if got.Studio != "Some Studio" {
		t.Errorf("Studio = %q, want Some Studio", got.Studio)
	}
	if len(got.Performers) != 2 || got.Performers[0] != "Performer A" {
		t.Errorf("Performers = %v", got.Performers)
	}
}

func TestIdentifyByURLTriesEachURLAndStopsAtTheFirstMatch(t *testing.T) {
	sc := stash.Scene{
		ID: "1", URLs: []string{"https://example/bad", "https://example/good"},
		Files:     []stash.File{{ID: "f1", Path: "/lib/1.mp4"}},
		UpdatedAt: "2024-01-01 00:00:00",
	}
	f, c := newFakeStash(t, sc)
	// "bad" answers null (no urlAnswers entry); "good" matches.
	f.urlAnswers["https://example/good"] = &stash.ScrapedScene{Title: "Found It"}

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if len(rep.URLResults) != 1 || rep.URLResults[0].URL != "https://example/good" {
		t.Fatalf("URLResults = %+v, want the second url to have matched", rep.URLResults)
	}
}

// --- write failures: doMutation records the error and does not abort the run ---

func TestIdentifyByURLUpdateFailureIsRecordedButDoesNotAbort(t *testing.T) {
	sc := urlScene("1", "https://example/1", "", "")
	f, c := newFakeStash(t, sc)
	f.urlAnswers["https://example/1"] = &stash.ScrapedScene{Title: "Scraped Title"}
	f.failOn["sceneUpdate"] = "server exploded"

	l := newLedger(t)
	seedScenes(t, l, sc)

	rep, err := Identify(context.Background(), c, l, Options{Via: "url", Commit: true, Delay: -1})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if rep.URLResults[0].Err == "" {
		t.Error("URLResults[0].Err should carry the sceneUpdate failure")
	}
	kinds := mutationKinds(t, l)
	if len(kinds) != 1 || kinds[0] != "identify:update" {
		t.Fatalf("mutation log = %v, want the failed attempt logged", kinds)
	}
	entries, err := l.Mutations(context.Background(), 0)
	if err != nil {
		t.Fatalf("Mutations: %v", err)
	}
	if entries[0].OK == nil || *entries[0].OK {
		t.Error("the logged mutation should be stamped failed (ok=false)")
	}
}

func TestIdentifyByStashBoxMetadataIdentifyFailureIsRecorded(t *testing.T) {
	one := noStashIDScene("1", 1000)
	f, c := newFakeStash(t, one)
	f.multiAnswers = func(ids []string) [][]stash.ScrapedScene {
		return [][]stash.ScrapedScene{{{RemoteSiteID: "sid-1", Duration: 1000}}}
	}
	f.failOn["metadataIdentify"] = "stash-box unreachable"

	l := newLedger(t)
	seedScenes(t, l, one)

	rep, err := Identify(context.Background(), c, l, Options{
		Via: "stash-box", Endpoint: "https://sb.test/graphql", Commit: true, Delay: -1,
	})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if rep.JobErr == "" {
		t.Error("JobErr should carry the metadataIdentify failure")
	}
}

// --- waitForJob: the job vanishing from the queue, and context cancellation ---

func TestWaitForJobReturnsErrorWhenJobIsGone(t *testing.T) {
	fs, c := newFakeStash(t)
	fs.jobNotFound = true
	_, err := waitForJob(context.Background(), c, "ghost-job", -1)
	if err == nil {
		t.Error("waitForJob should fail when the job has aged out of the queue")
	}
}

func TestWaitForJobHonoursContextCancellation(t *testing.T) {
	fs, c := newFakeStash(t)
	fs.jobStatuses = []stash.JobStatus{stash.JobRunning}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := waitForJob(ctx, c, "job-1", 0)
	if err == nil {
		t.Error("waitForJob should fail once ctx is cancelled while the job is still running")
	}
}

// --- stashBoxWait: default delay path and context cancellation ---

func TestStashBoxWaitHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stashBoxWait(ctx, time.Millisecond); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("stashBoxWait error = %v, want context.Canceled", err)
	}
}

func TestStashBoxWaitZeroUsesDefaultDelay(t *testing.T) {
	// Exercise the "delay == 0 -> stashBoxDelay" branch without actually
	// sleeping 200ms: cancel the context first so the select returns
	// immediately via ctx.Done().
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stashBoxWait(ctx, 0); err == nil {
		t.Error("stashBoxWait(0) with a cancelled context should return an error")
	}
}
