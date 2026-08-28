package reclaim

import (
	"fmt"
	"strings"

	"github.com/Anastylosis/Custodian/internal/confirm"
)

// Report is what one Apply run did — or, in a dry run, exactly what it would
// have done, down to the mutations that were never sent.
type Report struct {
	DryRun        bool
	RunID         int64
	QuarantineDir string
	Tag           string
	Backup        BackupStep
	Groups        []GroupReport
}

// BackupStep is §4.4 step 1: the verified database backup no --commit run is
// allowed to skip.
type BackupStep struct {
	Dir      string
	File     string
	Manifest string
	Bytes    int64
	Taken    bool
	Err      string
}

// GroupReport is one candidate group's outcome: the union, the losers moved
// to quarantine, and anything that stopped either.
type GroupReport struct {
	Signature string
	Keeper    string
	Skipped   bool
	Reason    string
	Union     *UnionStep
	Losers    []LoserReport
	Notes     []string
	Errs      []string
	Bytes     int64
	Done      bool
}

// UnionStep is the metadata carried onto the keeper before its losers are
// taken out of the way, plus the stash ids Union had to drop.
type UnionStep struct {
	Scene     string
	Fields    []string
	Conflicts []string
}

// LoserReport is one loser scene: the tag it took and the move its primary
// file made into quarantine.
type LoserReport struct {
	SceneID  string
	FileID   string
	From     string
	To       string
	Tagged   bool
	Bytes    int64
	Sidecars []SidecarMove
	Notes    []string
}

// SidecarMove is one companion file following its video into quarantine.
type SidecarMove struct {
	From string
	To   string
	Done bool
	Note string
}

// Counts summarises a Report: groups quarantined, skipped, failed, and the
// bytes those quarantines make reclaimable once `prune` runs.
func (r Report) Counts() (done, skipped, failed int, bytes int64) {
	for _, g := range r.Groups {
		switch {
		case g.Skipped:
			skipped++
		case len(g.Errs) > 0:
			failed++
		default:
			done++
			bytes += g.Bytes
		}
	}
	return done, skipped, failed, bytes
}

// Summary is the one-line form recorded as the apply run's notes.
func (r Report) Summary() string {
	done, skipped, failed, bytes := r.Counts()
	return fmt.Sprintf("quarantined %d, skipped %d, failed %d, %s",
		done, skipped, failed, confirm.HumanBytes(bytes))
}

// String renders the report as the plain text `custodian apply` prints.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== custodian apply (%s) ===\n", mode(r.DryRun))
	fmt.Fprintf(&b, "quarantine: %s\n", r.QuarantineDir)
	fmt.Fprintf(&b, "tag:        %s\n", r.Tag)
	writeBackup(&b, r.Backup, r.DryRun)
	b.WriteString("\n")

	for _, g := range r.Groups {
		writeGroup(&b, g, r.DryRun)
	}

	done, skipped, failed, bytes := r.Counts()
	verb := "quarantined"
	if r.DryRun {
		verb = "would quarantine"
	}
	fmt.Fprintf(&b, "%s: %d groups, %s reclaimable once pruned\n", verb, done, confirm.HumanBytes(bytes))
	fmt.Fprintf(&b, "skipped:    %d groups\n", skipped)
	fmt.Fprintf(&b, "failed:     %d groups\n", failed)
	if r.DryRun {
		b.WriteString("\nDRY RUN — nothing was sent to Stash, nothing moved on disk, and the\n")
		b.WriteString("mutation log is untouched. Re-run with --commit to act on this plan.\n")
	}
	return b.String()
}

func mode(dryRun bool) string {
	if dryRun {
		return "DRY RUN"
	}
	return "COMMIT"
}

func writeBackup(b *strings.Builder, bk BackupStep, dryRun bool) {
	switch {
	case bk.Err != "":
		fmt.Fprintf(b, "backup:     FAILED — %s\n", bk.Err)
	case bk.Taken:
		fmt.Fprintf(b, "backup:     %s (%s)\n", bk.File, confirm.HumanBytes(bk.Bytes))
		fmt.Fprintf(b, "manifest:   %s\n", bk.Manifest)
	case dryRun:
		fmt.Fprintf(b, "backup:     would download a verified backup to %s first\n", bk.Dir)
	default:
		fmt.Fprintf(b, "backup:     not taken\n")
	}
}

func writeGroup(b *strings.Builder, g GroupReport, dryRun bool) {
	fmt.Fprintf(b, "group %s keeper %s\n", shortSignature(g.Signature), g.Keeper)
	if g.Skipped {
		fmt.Fprintf(b, "  SKIPPED: %s\n\n", g.Reason)
		return
	}
	writeUnion(b, g.Union, dryRun)
	for _, l := range g.Losers {
		writeLoser(b, l, dryRun)
	}
	for _, n := range g.Notes {
		fmt.Fprintf(b, "  note: %s\n", n)
	}
	for _, e := range g.Errs {
		fmt.Fprintf(b, "  ERROR: %s\n", e)
	}
	fmt.Fprintf(b, "  reclaimable: %s\n\n", confirm.HumanBytes(g.Bytes))
}

func writeUnion(b *strings.Builder, u *UnionStep, dryRun bool) {
	if u == nil {
		return
	}
	if len(u.Fields) == 0 {
		b.WriteString("  union:   nothing to carry over\n")
	} else {
		fmt.Fprintf(b, "  union:   %s scene %s: %s\n",
			verb(dryRun, "sceneUpdate", "would sceneUpdate"), u.Scene, strings.Join(u.Fields, ", "))
	}
	for _, c := range u.Conflicts {
		fmt.Fprintf(b, "  conflict: %s\n", c)
	}
}

func writeLoser(b *strings.Builder, l LoserReport, dryRun bool) {
	fmt.Fprintf(b, "  loser %s (%s)\n", l.SceneID, confirm.HumanBytes(l.Bytes))
	fmt.Fprintf(b, "    tag:     %s\n", verb(dryRun, "added", "would add"))
	fmt.Fprintf(b, "    move:    %s → %s\n", l.From, l.To)
	for _, s := range l.Sidecars {
		note := s.Note
		if note == "" && !s.Done {
			note = "planned"
		}
		fmt.Fprintf(b, "    sidecar: %s → %s", s.From, s.To)
		if note != "" {
			fmt.Fprintf(b, " (%s)", note)
		}
		b.WriteString("\n")
	}
	for _, n := range l.Notes {
		fmt.Fprintf(b, "    note:    %s\n", n)
	}
}

func verb(dryRun bool, did, would string) string {
	if dryRun {
		return would
	}
	return did
}

// shortSignature truncates a group's signature to a readable prefix.
func shortSignature(sig string) string {
	const n = 12
	r := []rune(sig)
	if len(r) <= n {
		return sig
	}
	return string(r[:n]) + "…"
}
