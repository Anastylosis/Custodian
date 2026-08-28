package ledger

import (
	"context"
	"testing"
)

func TestMutationsByRunIsOldestFirstAndScopedToTheRun(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	run1, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	id1, err := l.AppendMutation(ctx, Mutation{RunID: run1, Kind: "union", Target: "scene 1"})
	if err != nil {
		t.Fatalf("AppendMutation: %v", err)
	}
	if err := l.MarkMutation(ctx, id1, true, ""); err != nil {
		t.Fatalf("MarkMutation: %v", err)
	}
	if _, err := l.AppendMutation(ctx, Mutation{RunID: run1, Kind: "tag", Target: "scene 2"}); err != nil {
		t.Fatalf("AppendMutation: %v", err)
	}

	run2, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if _, err := l.AppendMutation(ctx, Mutation{RunID: run2, Kind: "union", Target: "scene 9"}); err != nil {
		t.Fatalf("AppendMutation: %v", err)
	}

	got, err := l.MutationsByRun(ctx, run1)
	if err != nil {
		t.Fatalf("MutationsByRun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("MutationsByRun(run1) = %d rows, want 2", len(got))
	}
	if got[0].Kind != "union" || got[1].Kind != "tag" {
		t.Errorf("order = %s, %s; want union then tag (oldest first)", got[0].Kind, got[1].Kind)
	}
	if got[0].OK == nil || !*got[0].OK {
		t.Errorf("first row OK = %v, want true", got[0].OK)
	}
}

func TestGroupBySignatureFindsAndFillsAGroup(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	runID, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, runID, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}
	if err := l.SetChoice(ctx, "hash1+hash2", "1", []string{"2"}, []Step{{Rule: "tier", Outcome: "1 wins"}}); err != nil {
		t.Fatalf("SetChoice: %v", err)
	}

	g, found, err := l.GroupBySignature(ctx, "hash1+hash2")
	if err != nil {
		t.Fatalf("GroupBySignature: %v", err)
	}
	if !found {
		t.Fatal("GroupBySignature did not find the group")
	}
	if g.Keeper != "1" || len(g.SceneIDs) != 2 || len(g.Evidence) != 2 || len(g.Steps) != 1 {
		t.Errorf("group = %+v, want keeper 1, 2 scenes, 2 evidence, 1 step", g)
	}

	if _, found, err := l.GroupBySignature(ctx, "no-such-signature"); err != nil || found {
		t.Errorf("GroupBySignature(missing) = found %v, err %v", found, err)
	}
}

func TestGroupsByAppliedRunOnlyReturnsThatRunsGroups(t *testing.T) {
	ctx := context.Background()
	l := newTestLedger(t)

	scanRun, err := l.BeginRun(ctx, "scan", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.ReplaceGroups(ctx, scanRun, sampleGroups()); err != nil {
		t.Fatalf("ReplaceGroups: %v", err)
	}

	applyRun, err := l.BeginRun(ctx, "apply", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.SetOutcome(ctx, "hash1+hash2", VerdictQuarantined, applyRun); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}

	got, err := l.GroupsByAppliedRun(ctx, applyRun)
	if err != nil {
		t.Fatalf("GroupsByAppliedRun: %v", err)
	}
	if len(got) != 1 || got[0].Signature != "hash1+hash2" {
		t.Fatalf("GroupsByAppliedRun = %+v, want just hash1+hash2", got)
	}
	if got[0].AppliedRun != applyRun {
		t.Errorf("AppliedRun = %d, want %d", got[0].AppliedRun, applyRun)
	}

	// A later prune under a different run reassigns applied_run, so the
	// group stops showing up under the apply run that quarantined it.
	pruneRun, err := l.BeginRun(ctx, "prune", "")
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := l.SetOutcome(ctx, "hash1+hash2", VerdictReclaimed, pruneRun); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
	got, err = l.GroupsByAppliedRun(ctx, applyRun)
	if err != nil {
		t.Fatalf("GroupsByAppliedRun: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GroupsByAppliedRun(applyRun) after prune = %+v, want none", got)
	}
}
