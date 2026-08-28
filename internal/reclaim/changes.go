package reclaim

import (
	"encoding/json"
	"fmt"
	"os"
)

// ChangeEntry is one group's outcome, appended to <backup file>.changes.json
// beside the verified backup a commit-mode Apply or Prune takes (REBUILD.md
// §4.4's Reversal paragraph). It mirrors FSS's stashbox changelog shape:
// every entry a self-contained record of what changed and why, so a reader
// (or `custodian undo`, for an apply entry) never has to re-derive it.
//
// An apply entry carries Union and Losers; a prune entry carries Merged and
// Deleted instead — the two never overlap on one entry.
type ChangeEntry struct {
	Signature string                 `json:"signature"`
	RunID     int64                  `json:"run_id"`
	At        string                 `json:"at"`
	Keeper    string                 `json:"keeper"`
	Union     map[string]FieldChange `json:"union,omitempty"`
	Losers    []LoserChange          `json:"losers,omitempty"`
	Merged    []string               `json:"merged,omitempty"`
	Deleted   []string               `json:"deleted,omitempty"`
}

// FieldChange is one field's value before and after a union.
type FieldChange struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// LoserChange is one loser's move into quarantine, as recorded for an apply
// entry.
type LoserChange struct {
	SceneID  string          `json:"scene_id"`
	FileID   string          `json:"file_id"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	Sidecars []SidecarChange `json:"sidecars,omitempty"`
	Tag      string          `json:"tag"`
}

// SidecarChange is one companion file's move alongside its video.
type SidecarChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// writeChanges appends entries to the changes JSON at path, creating it —
// as a fresh array — if it does not exist yet. Never truncates: a second
// commit run's entries land after the first's.
func writeChanges(path string, entries []ChangeEntry) error {
	if len(entries) == 0 {
		return nil
	}

	var all []ChangeEntry
	switch data, err := os.ReadFile(path); {
	case err == nil:
		if err := json.Unmarshal(data, &all); err != nil {
			return fmt.Errorf("reclaim: reading existing changes JSON %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// First commit run against this backup — start a fresh array.
	default:
		return fmt.Errorf("reclaim: reading changes JSON %s: %w", path, err)
	}

	all = append(all, entries...)
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("reclaim: encoding changes JSON: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("reclaim: writing changes JSON %s: %w", path, err)
	}
	return nil
}

// unionChanges renders a GroupReport's union step as the field map a changes
// entry carries — nil when the group had no union step, or nothing to carry.
func unionChanges(u *UnionStep) map[string]FieldChange {
	if u == nil || len(u.Fields) == 0 {
		return nil
	}
	out := make(map[string]FieldChange, len(u.Fields))
	for _, name := range u.Fields {
		out[name] = FieldChange{Before: fieldValue(u.Before, name), After: fieldValue(u.After, name)}
	}
	return out
}

// changeEntriesFromReport builds one ChangeEntry per fully-applied group in
// rep — skipped and failed groups changed nothing lasting and have nothing
// to reverse.
func changeEntriesFromReport(rep Report, at string) []ChangeEntry {
	var out []ChangeEntry
	for _, g := range rep.Groups {
		if !g.Done {
			continue
		}
		ce := ChangeEntry{
			Signature: g.Signature, RunID: rep.RunID, At: at, Keeper: g.Keeper,
			Union: unionChanges(g.Union),
		}
		for _, l := range g.Losers {
			lc := LoserChange{SceneID: l.SceneID, FileID: l.FileID, From: l.From, To: l.To, Tag: rep.Tag}
			for _, s := range l.Sidecars {
				lc.Sidecars = append(lc.Sidecars, SidecarChange{From: s.From, To: s.To})
			}
			ce.Losers = append(ce.Losers, lc)
		}
		out = append(out, ce)
	}
	return out
}

// pruneChangeEntries builds one ChangeEntry per fully-pruned group — the
// scene ids merged into the keeper and the file ids deleted from disk.
func pruneChangeEntries(rep PruneReport, at string) []ChangeEntry {
	var out []ChangeEntry
	for _, g := range rep.Groups {
		if !g.Done {
			continue
		}
		ce := ChangeEntry{Signature: g.Signature, RunID: rep.RunID, At: at, Keeper: g.Keeper}
		for _, l := range g.Losers {
			ce.Merged = append(ce.Merged, l.SceneID)
			ce.Deleted = append(ce.Deleted, l.FileID)
		}
		out = append(out, ce)
	}
	return out
}
