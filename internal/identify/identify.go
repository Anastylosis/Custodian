// Package identify links scenes to a stash-box, opt-in and never silent
// (REBUILD.md §0's decision: "Custodian itself never sets stash_ids").
//
// Two routes, both read-only until --commit:
//
//   - "url": scenes that carry a URL but no stash_id are asked, through
//     Stash's own URL scraper, what that page says. Text fields are filled
//     only where the scene is empty; a stash-box id in the answer is handed
//     to Stash's own MetadataIdentify job rather than written directly.
//   - "stash-box": scenes with no stash_id are pre-screened with
//     scrapeMultiScenes; only an unambiguous, duration-matching candidate is
//     handed to MetadataIdentify. Everything else is held for the operator.
//
// In both cases the identity decision is Stash's own MetadataIdentify job,
// never a direct sceneUpdate of stash_ids — see stash-go's warning that
// identify applies whatever the server's field rules say.
package identify

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// Chunking and pacing for the stash-box pre-screen — matches
// internal/canonical/stashbox.go's "chunks of ≤ 25 scene ids", REBUILD.md.
const (
	stashBoxChunkSize = 25
	stashBoxDelay     = 200 * time.Millisecond
)

// Trimmed tolerance for "the pre-screened candidate matches the scene" —
// REBUILD.md §0's decision: a cut intro/outro is the same scene up to ≤ 5%
// and ≤ 90 s; both bounds must hold, which comparing against their minimum
// gives directly.
const (
	toleranceFraction = 0.05
	toleranceSeconds  = 90.0
)

// defaultPollInterval paces polling a started identify job.
const defaultPollInterval = 500 * time.Millisecond

// Options configures Identify. Via has no default — the caller must say
// which route, so identification is never silent.
type Options struct {
	// Via selects the route: "url" or "stash-box".
	Via string
	// Endpoint is the stash-box to scrape and to hand to MetadataIdentify.
	// Required for Via == "stash-box"; unused for "url".
	Endpoint string
	// Limit caps how many candidate scenes are considered (0 = no limit).
	Limit int
	// Commit sends writes. Without it Identify only scrapes (read-only) and
	// reports what it would do.
	Commit bool
	// Delay overrides the pause between stash-box scrape chunks. Zero uses
	// stashBoxDelay; negative skips the wait (tests).
	Delay time.Duration
	// PollInterval overrides how often a started identify job is polled.
	// Zero uses defaultPollInterval.
	PollInterval time.Duration
}

// Scalars is the plain-text scene fields identify compares and, via url,
// may fill — the same fields stash-go's DefaultUnionPolicy treats as
// FillEmpty.
type Scalars struct {
	Title, Code, Date, Details, Director string
}

// URLResult is one scene identify --via url looked at.
type URLResult struct {
	SceneID string
	// URL is the one that produced a match, or the last one tried.
	URL   string
	Found bool
	Err   string

	Current Scalars
	Scraped Scalars
	// Studio and Performers are shown for comparison only — never written.
	// Filling them means creating or matching Stash entities from a scrape,
	// a bigger commitment than the text-field fill REBUILD.md's decision
	// asks for.
	Studio     string
	Performers []string

	// Wrote names the Current fields that were empty and got Scraped's
	// value — what was written (--commit) or would be (dry run).
	Wrote []string

	RemoteSiteID string
	// Identified is true when RemoteSiteID was non-empty, so the scene was
	// (or would be) handed to MetadataIdentify.
	Identified bool
}

// ScreenResult is one scene identify --via stash-box pre-screened.
type ScreenResult struct {
	SceneID    string
	Duration   float64
	Candidates []stash.ScrapedScene
	Selected   bool
	// Reason explains why a scene was held, when Selected is false.
	Reason string
}

// Report is what one Identify call found and, in commit mode, did.
type Report struct {
	Via      string
	Endpoint string
	DryRun   bool

	// Considered is the size of the population identify looked at, before
	// any scrape or screen.
	Considered int

	URLResults []URLResult
	Screened   []ScreenResult

	// SelectedCount is how many scenes were (or would be) handed to
	// MetadataIdentify.
	SelectedCount int

	JobID     string
	JobStatus string
	JobErr    string
}

// Identify runs one route and, with Options.Commit, acts on what it finds.
// Without it, every Stash call it makes is read-only: scrapeSceneURL,
// scrapeMultiScenes, findScene. Nothing is written.
func Identify(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts Options) (Report, error) {
	if client == nil || l == nil {
		return Report{}, fmt.Errorf("identify: a Stash client and a ledger are both required")
	}
	switch opts.Via {
	case "url":
		return identifyByURL(ctx, client, l, opts)
	case "stash-box":
		return identifyByStashBox(ctx, client, l, opts)
	default:
		return Report{}, fmt.Errorf("identify: --via must be url or stash-box (no default), got %q", opts.Via)
	}
}

func identifyByURL(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts Options) (Report, error) {
	rep := Report{Via: "url", DryRun: !opts.Commit}

	ids, err := l.ScenesWithURLNoStashID(ctx, opts.Limit)
	if err != nil {
		return rep, err
	}
	rep.Considered = len(ids)
	if len(ids) == 0 {
		return rep, nil
	}

	runID, err := beginRunIfCommit(ctx, l, opts.Commit)
	if err != nil {
		return rep, err
	}

	var identifyIDs []string
	for _, sceneID := range ids {
		sc, found, err := client.FindScene(ctx, sceneID)
		if err != nil {
			return rep, fmt.Errorf("identify: finding scene %s: %w", sceneID, err)
		}
		if !found {
			rep.URLResults = append(rep.URLResults, URLResult{
				SceneID: sceneID, Err: "gone from Stash since the last sync",
			})
			continue
		}

		res, scraped := scrapeURLsForScene(ctx, client, sc)
		if scraped != nil {
			update, fields := fillEmptyFields(res.Current, res.Scraped)
			res.Wrote = fields
			if len(fields) > 0 {
				update.ID = sc.ID
				if err := doMutation(ctx, l, runID, opts.Commit, ledger.Mutation{
					Kind: "identify:update", Target: "scene " + sc.ID,
					To: strings.Join(fields, ","), Note: "sceneUpdate (fill-empty from url scrape)",
				}, func() error { return client.UpdateScene(ctx, update) }); err != nil {
					res.Err = err.Error()
				}
			}
			if res.RemoteSiteID != "" {
				res.Identified = true
				identifyIDs = append(identifyIDs, sc.ID)
			}
		}
		rep.URLResults = append(rep.URLResults, res)
	}

	rep.SelectedCount = len(identifyIDs)
	if len(identifyIDs) > 0 {
		runIdentifyJob(ctx, client, l, runID, opts, identifyIDs, &rep)
	}
	finishRunIfCommit(ctx, l, runID, rep.Summary())
	return rep, nil
}

func identifyByStashBox(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts Options) (Report, error) {
	rep := Report{Via: "stash-box", Endpoint: opts.Endpoint, DryRun: !opts.Commit}
	if opts.Endpoint == "" {
		return rep, fmt.Errorf("identify: --via stash-box needs --endpoint")
	}

	ids, err := l.ScenesWithoutStashID(ctx, opts.Limit)
	if err != nil {
		return rep, err
	}
	rep.Considered = len(ids)
	if len(ids) == 0 {
		return rep, nil
	}

	refs, err := l.SceneRefs(ctx, ids)
	if err != nil {
		return rep, err
	}
	duration := make(map[string]float64, len(refs))
	for _, r := range refs {
		duration[r.ID] = r.Duration
	}

	runID, err := beginRunIfCommit(ctx, l, opts.Commit)
	if err != nil {
		return rep, err
	}

	var selected []string
	for i := 0; i < len(ids); i += stashBoxChunkSize {
		end := min(i+stashBoxChunkSize, len(ids))
		chunk := ids[i:end]

		results, err := client.ScrapeMultiScenes(ctx, opts.Endpoint, chunk)
		if err != nil {
			return rep, fmt.Errorf("identify: scraping stash-box %s: %w", opts.Endpoint, err)
		}
		for j, sceneID := range chunk {
			var candidates []stash.ScrapedScene
			if j < len(results) {
				candidates = results[j]
			}
			sr := screenCandidate(sceneID, duration[sceneID], candidates)
			if sr.Selected {
				selected = append(selected, sceneID)
			}
			rep.Screened = append(rep.Screened, sr)
		}

		if end >= len(ids) {
			break
		}
		if err := stashBoxWait(ctx, opts.Delay); err != nil {
			return rep, err
		}
	}

	rep.SelectedCount = len(selected)
	if len(selected) > 0 {
		runIdentifyJob(ctx, client, l, runID, opts, selected, &rep)
	}
	finishRunIfCommit(ctx, l, runID, rep.Summary())
	return rep, nil
}

// screenCandidate applies REBUILD.md's stash-box pre-screen: exactly one
// candidate, within the trimmed duration tolerance of the scene's own.
// Anything else is held, with a reason.
func screenCandidate(sceneID string, sceneDuration float64, candidates []stash.ScrapedScene) ScreenResult {
	sr := ScreenResult{SceneID: sceneID, Duration: sceneDuration, Candidates: candidates}
	switch {
	case len(candidates) == 0:
		sr.Reason = "no candidate"
	case len(candidates) > 1:
		sr.Reason = fmt.Sprintf("ambiguous: %d candidates", len(candidates))
	case sceneDuration <= 0 || candidates[0].Duration <= 0:
		sr.Reason = "no duration to compare"
	case !withinTrimmedTolerance(sceneDuration, float64(candidates[0].Duration)):
		sr.Reason = fmt.Sprintf("duration mismatch: scene %.0fs, candidate %ds", sceneDuration, candidates[0].Duration)
	default:
		sr.Selected = true
	}
	return sr
}

// withinTrimmedTolerance reports whether a and b agree within REBUILD.md's
// trimmed threshold — ≤ 5% of the longer AND ≤ 90s. Comparing the
// difference to the minimum of the two bounds enforces both at once.
func withinTrimmedTolerance(a, b float64) bool {
	diff := math.Abs(a - b)
	longer := max(a, b)
	limit := min(longer*toleranceFraction, toleranceSeconds)
	return diff <= limit
}

// scrapeURLsForScene tries sc's URLs in order and stops at the first one
// the scraper answers. Errors on individual URLs are recorded but do not
// stop the walk; a scene with no successful scrape gets scraped == nil.
func scrapeURLsForScene(ctx context.Context, client *stash.Client, sc *stash.Scene) (URLResult, *stash.ScrapedScene) {
	res := URLResult{SceneID: sc.ID, Current: scalarsOfScene(sc)}
	for _, u := range sc.URLs {
		if u == "" {
			continue
		}
		scraped, err := scrapeSceneURL(ctx, client, u)
		if err != nil {
			res.Err = err.Error()
			continue
		}
		if scraped == nil {
			continue
		}
		res.URL = u
		res.Found = true
		res.Scraped = scalarsOfScraped(scraped)
		res.Studio = studioNameOf(scraped)
		res.Performers = performerNamesOf(scraped)
		res.RemoteSiteID = scraped.RemoteSiteID
		return res, scraped
	}
	return res, nil
}

// fillEmptyFields is stash-go's FillEmpty policy applied to the five plain
// text fields a scrape can safely fill: current wins unless it is empty,
// and only then does scraped's value get written. Pure, so the union logic
// is testable without a fake Stash server.
func fillEmptyFields(current, scraped Scalars) (stash.SceneUpdate, []string) {
	var u stash.SceneUpdate
	var fields []string
	fill := func(name, cur, val string, set func(string)) {
		if cur == "" && val != "" {
			set(val)
			fields = append(fields, name)
		}
	}
	fill("title", current.Title, scraped.Title, func(v string) { u.Title = &v })
	fill("code", current.Code, scraped.Code, func(v string) { u.Code = &v })
	fill("date", current.Date, scraped.Date, func(v string) { u.Date = &v })
	fill("details", current.Details, scraped.Details, func(v string) { u.Details = &v })
	fill("director", current.Director, scraped.Director, func(v string) { u.Director = &v })
	return u, fields
}

func scalarsOfScene(sc *stash.Scene) Scalars {
	return Scalars{Title: sc.Title, Code: sc.Code, Date: sc.Date, Details: sc.Details, Director: sc.Director}
}

func scalarsOfScraped(s *stash.ScrapedScene) Scalars {
	return Scalars{Title: s.Title, Code: s.Code, Date: s.Date, Details: s.Details, Director: s.Director}
}

func studioNameOf(s *stash.ScrapedScene) string {
	if s.Studio == nil {
		return ""
	}
	return s.Studio.Name
}

func performerNamesOf(s *stash.ScrapedScene) []string {
	names := make([]string, 0, len(s.Performers))
	for _, p := range s.Performers {
		names = append(names, p.Name)
	}
	return names
}

// scrapedSceneURLFields mirrors stash-go's unexported scrapedSceneFields
// selection (scrape.go) field for field, so the two decode into the same
// [stash.ScrapedScene] shape. Duplicated here because scrapeSceneURL is not
// wrapped yet — logged in REBUILD.md §6, "reached through Execute".
const scrapedSceneURLFields = `
	title code date details director urls image duration remote_site_id
	studio { name stored_id remote_site_id url }
	performers { name gender remote_site_id stored_id images country birthdate }
	tags { name stored_id }`

// scrapeSceneURL asks Stash's URL scraper about one URL, read-only. A nil
// result with no error means the scraper had nothing for this URL, which is
// the ordinary answer for a page it doesn't recognise, not a failure.
func scrapeSceneURL(ctx context.Context, client *stash.Client, url string) (*stash.ScrapedScene, error) {
	data, err := client.Execute(ctx,
		`query($url: String!) { scrapeSceneURL(url: $url) {`+scrapedSceneURLFields+`} }`,
		map[string]any{"url": url})
	if err != nil {
		return nil, fmt.Errorf("identify: scraping url %q: %w", url, err)
	}
	var result struct {
		ScrapeSceneURL *stash.ScrapedScene `json:"scrapeSceneURL"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("identify: decoding scraped url %q: %w", url, err)
	}
	return result.ScrapeSceneURL, nil
}

// runIdentifyJob hands sceneIDs to Stash's MetadataIdentify, restricted to
// exactly those scenes, and waits for the job — the identity decision stays
// Stash's own. In dry-run mode doMutation sends nothing, so the job is
// never started and rep's job fields stay empty.
func runIdentifyJob(ctx context.Context, client *stash.Client, l *ledger.Ledger, runID int64, opts Options, sceneIDs []string, rep *Report) {
	var sources []string
	if opts.Endpoint != "" {
		sources = []string{opts.Endpoint}
	}
	err := doMutation(ctx, l, runID, opts.Commit, ledger.Mutation{
		Kind: "identify:job", Target: fmt.Sprintf("%d scene(s)", len(sceneIDs)),
		To: strings.Join(sceneIDs, ","), Note: "metadataIdentify",
	}, func() error {
		jobID, err := client.MetadataIdentify(ctx, stash.IdentifyOptions{Sources: sources, SceneIDs: sceneIDs})
		if err != nil {
			return err
		}
		rep.JobID = jobID
		job, err := waitForJob(ctx, client, jobID, opts.PollInterval)
		if err != nil {
			return err
		}
		rep.JobStatus = string(job.Status)
		return nil
	})
	if err != nil {
		rep.JobErr = err.Error()
	}
}

// waitForJob polls FindJob until the job reaches a terminal state.
func waitForJob(ctx context.Context, client *stash.Client, jobID string, pollInterval time.Duration) (*stash.Job, error) {
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	for {
		job, found, err := client.FindJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("identify: job %s aged out of Stash's queue before it finished", jobID)
		}
		if job.Status.Done() {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// stashBoxWait is internal/canonical/stashbox.go's chunk pause, duplicated
// rather than shared: two callers, one small function, not worth exporting
// from canonical for.
func stashBoxWait(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		delay = stashBoxDelay
	}
	if delay < 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func beginRunIfCommit(ctx context.Context, l *ledger.Ledger, commit bool) (int64, error) {
	if !commit {
		return 0, nil
	}
	id, err := l.BeginRun(ctx, "identify", "")
	if err != nil {
		return 0, fmt.Errorf("identify: beginning run: %w", err)
	}
	return id, nil
}

func finishRunIfCommit(ctx context.Context, l *ledger.Ledger, runID int64, notes string) {
	if runID == 0 {
		return
	}
	_ = l.FinishRun(ctx, runID, ledger.RunSummary{Notes: notes})
}

// doMutation sends one write, logged before it goes and stamped with the
// outcome after — reclaim.applier's pattern. A dry run (commit == false)
// sends nothing and logs nothing.
func doMutation(ctx context.Context, l *ledger.Ledger, runID int64, commit bool, m ledger.Mutation, send func() error) error {
	if !commit {
		return nil
	}
	m.RunID = runID
	id, err := l.AppendMutation(ctx, m)
	if err != nil {
		return err
	}
	if err := send(); err != nil {
		_ = l.MarkMutation(ctx, id, false, err.Error())
		return err
	}
	return l.MarkMutation(ctx, id, true, "")
}

// Summary is the one-line form recorded as the identify run's notes.
func (r Report) Summary() string {
	switch r.Via {
	case "url":
		return fmt.Sprintf("url: considered %d, identified %d", r.Considered, r.SelectedCount)
	case "stash-box":
		return fmt.Sprintf("stash-box %s: considered %d, selected %d", r.Endpoint, r.Considered, r.SelectedCount)
	default:
		return "identify: nothing to do"
	}
}

// caveat is the first line of every report — REBUILD.md §0's decision that
// identification "must never surprise": stated up front, every run.
const caveat = "identify: matches are not proof of identity — stash-box fingerprint matching in particular has false positives. Nothing is written without --commit.\n"

// String renders the report as the plain text `custodian identify` prints.
func (r Report) String() string {
	var b strings.Builder
	b.WriteString(caveat)
	if r.Via == "" {
		return b.String()
	}

	mode := "commit"
	if r.DryRun {
		mode = "DRY RUN"
	}
	fmt.Fprintf(&b, "\ncustodian identify --via %s (%s)\n", r.Via, mode)
	if r.Endpoint != "" {
		fmt.Fprintf(&b, "endpoint: %s\n", r.Endpoint)
	}
	fmt.Fprintf(&b, "considered: %d\n", r.Considered)

	if r.Via == "url" {
		writeURLResults(&b, r.URLResults)
	} else {
		writeScreenResults(&b, r.Screened)
	}

	action := "would identify"
	if !r.DryRun {
		action = "identified"
	}
	fmt.Fprintf(&b, "\n%s: %d scene(s)\n", action, r.SelectedCount)
	if r.JobID != "" {
		fmt.Fprintf(&b, "job: %s (%s)\n", r.JobID, r.JobStatus)
	}
	if r.JobErr != "" {
		fmt.Fprintf(&b, "job error: %s\n", r.JobErr)
	}
	if r.DryRun {
		b.WriteString("\nDRY RUN — nothing was sent to Stash. Re-run with --commit to act on this.\n")
	}
	return b.String()
}

func writeURLResults(b *strings.Builder, results []URLResult) {
	for _, res := range results {
		fmt.Fprintf(b, "\nscene %s\n", res.SceneID)
		if res.Err != "" && !res.Found {
			fmt.Fprintf(b, "  error: %s\n", res.Err)
			continue
		}
		if !res.Found {
			b.WriteString("  no scraper had this url\n")
			continue
		}
		fmt.Fprintf(b, "  url: %s\n", res.URL)
		fmt.Fprintf(b, "  now:     title=%q date=%q\n", res.Current.Title, res.Current.Date)
		fmt.Fprintf(b, "  scraped: title=%q date=%q studio=%q performers=%v\n",
			res.Scraped.Title, res.Scraped.Date, res.Studio, res.Performers)
		if len(res.Wrote) > 0 {
			fmt.Fprintf(b, "  fills empty: %s\n", strings.Join(res.Wrote, ", "))
		} else {
			b.WriteString("  fills empty: nothing (every scraped field is already set)\n")
		}
		if res.RemoteSiteID != "" {
			fmt.Fprintf(b, "  stash-box id: %s → hands this scene to MetadataIdentify\n", res.RemoteSiteID)
		}
	}
}

func writeScreenResults(b *strings.Builder, results []ScreenResult) {
	for _, sr := range results {
		if sr.Selected {
			fmt.Fprintf(b, "\nscene %s (%.0fs) — selected: %s (%ds)\n",
				sr.SceneID, sr.Duration, sr.Candidates[0].Title, sr.Candidates[0].Duration)
			continue
		}
		fmt.Fprintf(b, "\nscene %s (%.0fs) — held: %s\n", sr.SceneID, sr.Duration, sr.Reason)
	}
}
