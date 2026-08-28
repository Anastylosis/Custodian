package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// defaultPollInterval is how often GeneratePhashes checks a commit run's
// job status.
const defaultPollInterval = 2 * time.Second

// sampleSize is how many paths a dry run prints.
const sampleSize = 5

// GenerateOptions configures GeneratePhashes.
type GenerateOptions struct {
	// Commit sends the job to Stash. Without it nothing is written.
	Commit bool
	// Limit caps how many files are acted on (0 = no limit).
	Limit int
	// Progress, if set, is called after every poll while a commit run
	// waits for its job to finish.
	Progress func(stash.Job)
	// PollInterval overrides the default poll cadence. Tests set this low.
	PollInterval time.Duration
}

// GenerateReport is what GeneratePhashes did, or would do.
type GenerateReport struct {
	Count       int      `json:"count"`
	SamplePaths []string `json:"sample_paths,omitempty"`
	Committed   bool     `json:"committed"`
	JobID       string   `json:"job_id,omitempty"`
	JobStatus   string   `json:"job_status,omitempty"`
	// Resynced is how many scenes were re-fetched and upserted into the
	// ledger after the job finished.
	Resynced int `json:"resynced,omitempty"`
}

// GeneratePhashes collects the scene ids of every live file with no phash
// and, in commit mode, hands them to Stash's generate task, waits for it,
// then re-fetches those scenes and upserts them into the ledger.
//
// MetadataGenerate does NOT bump scene.updated_at (verified against a
// throwaway Stash container: forcing an overwrite regenerate left
// updated_at unchanged), so mirror.Sync's incremental high-water mark would
// never pick these scenes up — they are re-fetched by id instead.
//
// A dry run only reports the count and a few sample paths; nothing is sent.
func GeneratePhashes(ctx context.Context, client *stash.Client, l *ledger.Ledger, opts GenerateOptions) (GenerateReport, error) {
	files, err := l.PhashGapScenes(ctx)
	if err != nil {
		return GenerateReport{}, err
	}
	if opts.Limit > 0 && len(files) > opts.Limit {
		files = files[:opts.Limit]
	}

	var rep GenerateReport
	rep.Count = len(files)

	if !opts.Commit {
		n := sampleSize
		if n > len(files) {
			n = len(files)
		}
		for _, f := range files[:n] {
			rep.SamplePaths = append(rep.SamplePaths, f.Path)
		}
		return rep, nil
	}

	if len(files) == 0 {
		return rep, nil
	}

	sceneIDs := dedupSceneIDs(files)
	jobID, mutID, err := startGenerateJob(ctx, client, l, sceneIDs)
	if err != nil {
		return rep, err
	}
	rep.Committed = true
	rep.JobID = jobID

	job, waitErr := waitForJob(ctx, client, jobID, pollInterval(opts.PollInterval), opts.Progress)
	finishGenerateMutation(ctx, l, mutID, jobID, job, waitErr)
	if waitErr != nil {
		return rep, waitErr
	}
	if job != nil {
		rep.JobStatus = string(job.Status)
	}

	n, err := resyncScenes(ctx, client, l, sceneIDs)
	rep.Resynced = n
	return rep, err
}

func dedupSceneIDs(files []ledger.PhashGapFile) []string {
	seen := make(map[string]bool, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		if seen[f.SceneID] {
			continue
		}
		seen[f.SceneID] = true
		out = append(out, f.SceneID)
	}
	return out
}

// startGenerateJob logs the mutation row before sending, per the mutation
// log's own convention, then starts the job. mutID is 0 (never a valid row
// id) when logging itself failed, so finishGenerateMutation knows not to
// try to stamp it.
func startGenerateJob(ctx context.Context, client *stash.Client, l *ledger.Ledger, sceneIDs []string) (jobID string, mutID int64, err error) {
	mutID, logErr := l.AppendMutation(ctx, ledger.Mutation{
		Kind: "generate", Target: fmt.Sprintf("%d scenes", len(sceneIDs)),
	})
	if logErr != nil {
		mutID = 0
	}

	jobID, err = client.MetadataGenerate(ctx, stash.GenerateOptions{Phashes: true, SceneIDs: sceneIDs})
	if err != nil {
		if mutID != 0 {
			_ = l.MarkMutation(ctx, mutID, false, err.Error())
		}
		return "", mutID, fmt.Errorf("doctor: starting generate job: %w", err)
	}
	return jobID, mutID, nil
}

// finishGenerateMutation stamps the mutation row AppendMutation opened,
// with the job id as its note (REBUILD.md §7 phase 0: "note: job id") and
// success tied to the job actually finishing rather than just starting.
func finishGenerateMutation(ctx context.Context, l *ledger.Ledger, mutID int64, jobID string, job *stash.Job, waitErr error) {
	if mutID == 0 {
		return
	}
	note := jobID
	ok := waitErr == nil && job != nil && job.Status == stash.JobFinished
	if waitErr != nil {
		note = jobID + ": " + waitErr.Error()
	}
	_ = l.MarkMutation(ctx, mutID, ok, note)
}

// waitForJob polls client.FindJob until it reports a terminal status,
// calling progress after every poll if set. It polls once immediately
// before the first wait, so a job that finishes fast is not held up by a
// full interval.
func waitForJob(ctx context.Context, client *stash.Client, jobID string, interval time.Duration, progress func(stash.Job)) (*stash.Job, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		job, found, err := client.FindJob(ctx, jobID)
		if err != nil {
			return nil, fmt.Errorf("doctor: polling job %s: %w", jobID, err)
		}
		if !found {
			return nil, fmt.Errorf("doctor: job %s aged out of the queue before finishing", jobID)
		}
		if progress != nil {
			progress(*job)
		}
		if job.Status.Done() {
			if job.Status != stash.JobFinished {
				msg := ""
				if job.Error != nil {
					msg = *job.Error
				}
				return job, fmt.Errorf("doctor: generate job %s ended %s: %s", jobID, job.Status, msg)
			}
			return job, nil
		}

		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-ticker.C:
		}
	}
}

func pollInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultPollInterval
	}
	return d
}

// resyncScenes re-fetches each scene by id and upserts it into the ledger
// under its own "generate" run — see GeneratePhashes' doc comment for why
// this can't just be an incremental mirror.Sync. A scene gone from Stash
// since the job started is simply skipped, not an error.
func resyncScenes(ctx context.Context, client *stash.Client, l *ledger.Ledger, sceneIDs []string) (int, error) {
	runID, err := l.BeginRun(ctx, "generate", "")
	if err != nil {
		return 0, err
	}

	scenes := make([]stash.Scene, 0, len(sceneIDs))
	for _, id := range sceneIDs {
		sc, found, err := client.FindScene(ctx, id)
		if err != nil {
			_ = l.FinishRun(ctx, runID, ledger.RunSummary{Notes: err.Error()})
			return 0, fmt.Errorf("doctor: refetching scene %s after generate: %w", id, err)
		}
		if !found {
			continue
		}
		scenes = append(scenes, *sc)
	}

	if err := l.UpsertScenes(ctx, runID, scenes); err != nil {
		_ = l.FinishRun(ctx, runID, ledger.RunSummary{Notes: err.Error()})
		return 0, err
	}
	if err := l.FinishRun(ctx, runID, ledger.RunSummary{ScenesSeen: len(scenes)}); err != nil {
		return 0, err
	}
	return len(scenes), nil
}
