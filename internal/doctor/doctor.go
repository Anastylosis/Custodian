// Package doctor computes custodian's read-only gap report (REBUILD.md §7
// phase 0): what's missing from the ledger, what's sitting in quarantine
// unaccounted for, and how stale the mirror is against Stash. One line and
// a suggested action per finding.
package doctor

import (
	"context"
	"fmt"

	"github.com/Anastylosis/Custodian/internal/config"
	"github.com/Anastylosis/Custodian/internal/confirm"
	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

// maxTopDirs caps how many directories PhashGap's report shows, most
// concentrated first.
const maxTopDirs = 5

// maxQuarantineList caps how many foreign quarantine paths the report
// prints — Quarantine.Count and .Bytes above it are never capped.
const maxQuarantineList = 20

// Report is the gap report `custodian doctor` prints and `--json` emits.
type Report struct {
	Phash      ledger.PhashGap             `json:"phash"`
	Quarantine ledger.QuarantineGap        `json:"quarantine"`
	NoPrimary  []string                    `json:"scenes_without_primary_file"`
	Tombstoned []ledger.TombstonedGroupRef `json:"tombstoned_in_live_groups"`
	Age        AgeGap                      `json:"age"`
}

// AgeGap compares the ledger's last sync against what Stash reports right
// now — a hint to run `sync` when they disagree.
type AgeGap struct {
	LastSync     string `json:"last_sync"`
	LastSyncKind string `json:"last_sync_kind"`
	LedgerScenes int    `json:"ledger_scenes"`
	// StashScenes and StashErr are mutually exclusive: a failed
	// LibraryStats call leaves StashScenes at 0 and StashErr set, rather
	// than treating "unreachable" as "reports zero scenes".
	StashScenes int    `json:"stash_scenes"`
	StashErr    string `json:"stash_err,omitempty"`
}

// Check reads the ledger (and, for Age, asks Stash for its own scene count)
// and returns the gap report. Only a ledger query failure is returned as an
// error; Stash being unreachable is recorded in Age.StashErr so the rest of
// the report still comes back.
func Check(ctx context.Context, client *stash.Client, l *ledger.Ledger, cfg *config.Config) (Report, error) {
	var rep Report

	phash, err := l.PhashGap(ctx)
	if err != nil {
		return Report{}, err
	}
	if len(phash.Dirs) > maxTopDirs {
		phash.Dirs = phash.Dirs[:maxTopDirs]
	}
	rep.Phash = phash

	quarantineDir := ""
	if cfg != nil {
		quarantineDir = cfg.Quarantine.Dir
	}
	quarantine, err := l.QuarantineGap(ctx, quarantineDir)
	if err != nil {
		return Report{}, err
	}
	// Count and Bytes stay the true totals; only the printed/JSON list caps.
	if len(quarantine.Paths) > maxQuarantineList {
		quarantine.Paths = quarantine.Paths[:maxQuarantineList]
	}
	rep.Quarantine = quarantine

	noPrimary, err := l.ScenesWithoutPrimaryFile(ctx)
	if err != nil {
		return Report{}, err
	}
	rep.NoPrimary = noPrimary

	tombstoned, err := l.TombstonedInLiveGroups(ctx)
	if err != nil {
		return Report{}, err
	}
	rep.Tombstoned = tombstoned

	age, err := ageGap(ctx, client, l)
	if err != nil {
		return Report{}, err
	}
	rep.Age = age

	return rep, nil
}

// ageGap reads the ledger's last sync and scene count, and Stash's current
// scene count. A LibraryStats failure is recorded on the result rather than
// returned, so an unreachable Stash degrades this one finding, not the
// whole report.
func ageGap(ctx context.Context, client *stash.Client, l *ledger.Ledger) (AgeGap, error) {
	var ag AgeGap

	lastSync, err := l.LastRunOfKind(ctx, "full", "incremental")
	if err != nil {
		return AgeGap{}, err
	}
	if lastSync != nil {
		ag.LastSync = lastSync.StartedAt
		ag.LastSyncKind = lastSync.Kind
	}

	stats, err := l.Stats(ctx)
	if err != nil {
		return AgeGap{}, err
	}
	ag.LedgerScenes = stats.ScenesLive

	if client == nil {
		ag.StashErr = "no Stash client configured"
		return ag, nil
	}
	libStats, err := client.LibraryStats(ctx)
	if err != nil {
		ag.StashErr = err.Error()
		return ag, nil
	}
	ag.StashScenes = libStats.SceneCount
	return ag, nil
}

// Lines renders the report as the text `custodian doctor` prints: one line
// per finding, with a suggested action when there is one to take.
func (r Report) Lines() []string {
	var out []string
	out = append(out, phashLines(r.Phash)...)
	out = append(out, quarantineLines(r.Quarantine)...)
	out = append(out, noPrimaryLine(r.NoPrimary))
	out = append(out, tombstonedLine(r.Tombstoned))
	out = append(out, ageLine(r.Age))
	return out
}

func phashLines(g ledger.PhashGap) []string {
	if g.Count == 0 {
		return []string{"phash gap:      none"}
	}
	out := []string{fmt.Sprintf(
		"phash gap:      %d files, %s, %d with a stash_id already — `custodian doctor --generate-phashes`",
		g.Count, confirm.HumanBytes(g.Bytes), g.WithStashID)}
	for _, d := range g.Dirs {
		out = append(out, fmt.Sprintf("                  %s: %d of %d live", d.Dir, d.Gap, d.Live))
	}
	return out
}

func quarantineLines(g ledger.QuarantineGap) []string {
	if g.Count == 0 {
		return []string{"quarantine:     clean — nothing foreign in the quarantine directory"}
	}
	out := []string{fmt.Sprintf(
		"quarantine:     %d foreign file(s), %s — not put there by any logged move; inspect by hand",
		g.Count, confirm.HumanBytes(g.Bytes))}
	for _, p := range g.Paths {
		out = append(out, "                  "+p)
	}
	return out
}

func noPrimaryLine(scenes []string) string {
	if len(scenes) == 0 {
		return "no primary:     none"
	}
	return fmt.Sprintf("no primary:     %d scene(s) with no primary file — inspect by hand", len(scenes))
}

func tombstonedLine(refs []ledger.TombstonedGroupRef) string {
	if len(refs) == 0 {
		return "tombstoned:     none"
	}
	return fmt.Sprintf(
		"tombstoned:     %d scene(s) gone from Stash still referenced by a live group — `custodian scan`",
		len(refs))
}

func ageLine(a AgeGap) string {
	if a.LastSync == "" {
		return "sync age:       never synced — `custodian sync --full`"
	}
	if a.StashErr != "" {
		return fmt.Sprintf("sync age:       last %s (%s); could not reach Stash to compare: %s",
			a.LastSync, a.LastSyncKind, a.StashErr)
	}
	diff := a.StashScenes - a.LedgerScenes
	if diff == 0 {
		return fmt.Sprintf("sync age:       last %s (%s); ledger matches Stash at %d scenes",
			a.LastSync, a.LastSyncKind, a.LedgerScenes)
	}
	return fmt.Sprintf(
		"sync age:       last %s (%s); ledger has %d scenes, Stash reports %d (%+d) — `custodian sync --full`",
		a.LastSync, a.LastSyncKind, a.LedgerScenes, a.StashScenes, diff)
}
