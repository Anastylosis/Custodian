package reclaim

import (
	"slices"
	"testing"

	"github.com/Anastylosis/Custodian/internal/ledger"
	stash "github.com/Anastylosis/stash-go"
)

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }
func boolPtr(b bool) *bool    { return &b }

func TestDiffRestoreScalarFields(t *testing.T) {
	cur := &stash.Scene{Title: "The Real Title", Code: "unchanged"}
	before := fieldSnapshot{Title: strPtr(""), Code: strPtr("was")}
	after := fieldSnapshot{Title: strPtr("The Real Title")} // code untouched by the union

	u, cleared, restored, notes := diffRestore(cur, before, after)
	if len(cleared) != 0 {
		t.Errorf("clearFields = %v, want none for scalar fields", cleared)
	}
	if !slices.Contains(restored, "title") {
		t.Errorf("restored = %v, want title", restored)
	}
	if u.Title == nil || *u.Title != "" {
		t.Errorf("Title = %v, want a pointer to \"\"", u.Title)
	}
	if u.Code != nil {
		t.Errorf("Code = %v, want untouched (the union never set it)", u.Code)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
}

func TestDiffRestoreSkipsAnEditedScalarField(t *testing.T) {
	cur := &stash.Scene{Title: "User Edited"}
	before := fieldSnapshot{Title: strPtr("")}
	after := fieldSnapshot{Title: strPtr("The Real Title")}

	u, _, restored, notes := diffRestore(cur, before, after)
	if len(restored) != 0 {
		t.Errorf("restored = %v, want none", restored)
	}
	if u.Title != nil {
		t.Errorf("Title = %v, want left unset", u.Title)
	}
	if !containsSubstring(notes, "title") {
		t.Errorf("notes = %v, want one about title", notes)
	}
}

func TestDiffRestoreRating100(t *testing.T) {
	t.Run("restored when it matches and had a prior value", func(t *testing.T) {
		cur := &stash.Scene{Rating100: intPtr(80)}
		before := fieldSnapshot{Rating100: intPtr(60)}
		after := fieldSnapshot{Rating100: intPtr(80)}
		u, _, restored, notes := diffRestore(cur, before, after)
		if !slices.Contains(restored, "rating100") || u.Rating100 == nil || *u.Rating100 != 60 {
			t.Errorf("restored=%v u.Rating100=%v", restored, u.Rating100)
		}
		if len(notes) != 0 {
			t.Errorf("notes = %v, want none", notes)
		}
	})
	t.Run("noted, not restored, when it was unset before", func(t *testing.T) {
		cur := &stash.Scene{Rating100: intPtr(80)}
		before := fieldSnapshot{Rating100: nil}
		after := fieldSnapshot{Rating100: intPtr(80)}
		u, _, restored, notes := diffRestore(cur, before, after)
		if len(restored) != 0 || u.Rating100 != nil {
			t.Errorf("restored=%v u.Rating100=%v, want neither", restored, u.Rating100)
		}
		if !containsSubstring(notes, "rating100") {
			t.Errorf("notes = %v, want one about rating100", notes)
		}
	})
	t.Run("skipped when edited since apply", func(t *testing.T) {
		cur := &stash.Scene{Rating100: intPtr(99)}
		before := fieldSnapshot{Rating100: intPtr(60)}
		after := fieldSnapshot{Rating100: intPtr(80)}
		_, _, restored, notes := diffRestore(cur, before, after)
		if len(restored) != 0 || !containsSubstring(notes, "edited since apply") {
			t.Errorf("restored=%v notes=%v", restored, notes)
		}
	})
}

func TestDiffRestoreOrganized(t *testing.T) {
	cur := &stash.Scene{Organized: true}
	before := fieldSnapshot{Organized: boolPtr(false)}
	after := fieldSnapshot{Organized: boolPtr(true)}
	u, _, restored, _ := diffRestore(cur, before, after)
	if !slices.Contains(restored, "organized") || u.Organized == nil || *u.Organized {
		t.Errorf("restored=%v u.Organized=%v", restored, u.Organized)
	}

	cur2 := &stash.Scene{Organized: false}
	_, _, restored2, notes2 := diffRestore(cur2, before, after)
	if len(restored2) != 0 || !containsSubstring(notes2, "organized") {
		t.Errorf("restored2=%v notes2=%v, want a skip", restored2, notes2)
	}
}

func TestDiffRestoreListFieldRestoresDirectlyWhenBeforeIsNonEmpty(t *testing.T) {
	cur := &stash.Scene{Tags: []stash.Tag{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}
	before := fieldSnapshot{TagIDs: []string{"t1", "t2"}}
	after := fieldSnapshot{TagIDs: []string{"t2", "t1", "t3"}} // order need not match

	u, cleared, restored, _ := diffRestore(cur, before, after)
	if slices.Contains(cleared, "tag_ids") {
		t.Errorf("clearFields = %v, want tag_ids restored directly (before is non-empty)", cleared)
	}
	if !slices.Contains(restored, "tag_ids") || !sameSet(u.TagIDs, []string{"t1", "t2"}) {
		t.Errorf("restored=%v u.TagIDs=%v", restored, u.TagIDs)
	}
}

func TestDiffRestoreListFieldClearsWhenBeforeWasEmpty(t *testing.T) {
	cur := &stash.Scene{Performers: []stash.Performer{{ID: "p1"}}}
	before := fieldSnapshot{PerformerIDs: nil}
	after := fieldSnapshot{PerformerIDs: []string{"p1"}}

	u, cleared, restored, _ := diffRestore(cur, before, after)
	if !slices.Contains(cleared, "performer_ids") {
		t.Errorf("clearFields = %v, want performer_ids", cleared)
	}
	if !slices.Contains(restored, "performer_ids") {
		t.Errorf("restored = %v, want performer_ids", restored)
	}
	if u.PerformerIDs != nil {
		t.Errorf("PerformerIDs = %v, want unset (cleared separately)", u.PerformerIDs)
	}
}

func TestDiffRestoreListFieldSkippedWhenEdited(t *testing.T) {
	cur := &stash.Scene{URLs: []string{"https://edited"}}
	before := fieldSnapshot{URLs: nil}
	after := fieldSnapshot{URLs: []string{"https://from-loser"}}

	_, cleared, restored, notes := diffRestore(cur, before, after)
	if len(cleared) != 0 || len(restored) != 0 {
		t.Errorf("cleared=%v restored=%v, want neither", cleared, restored)
	}
	if !containsSubstring(notes, "urls") {
		t.Errorf("notes = %v, want one about urls", notes)
	}
}

func TestDiffRestoreStashIDs(t *testing.T) {
	a := stash.StashID{Endpoint: "https://sb", ID: "uuid-a"}
	b := stash.StashID{Endpoint: "https://other", ID: "uuid-b"}

	t.Run("restored directly when before is non-empty", func(t *testing.T) {
		cur := &stash.Scene{StashIDs: []stash.StashID{a, b}}
		before := fieldSnapshot{StashIDs: []stash.StashID{a}}
		after := fieldSnapshot{StashIDs: []stash.StashID{a, b}}
		u, cleared, restored, _ := diffRestore(cur, before, after)
		if slices.Contains(cleared, "stash_ids") || !slices.Contains(restored, "stash_ids") {
			t.Errorf("cleared=%v restored=%v", cleared, restored)
		}
		if !sameStashIDs(u.StashIDs, []stash.StashID{a}) {
			t.Errorf("StashIDs = %v", u.StashIDs)
		}
	})
	t.Run("cleared when before was empty", func(t *testing.T) {
		cur := &stash.Scene{StashIDs: []stash.StashID{b}}
		before := fieldSnapshot{StashIDs: nil}
		after := fieldSnapshot{StashIDs: []stash.StashID{b}}
		_, cleared, restored, _ := diffRestore(cur, before, after)
		if !slices.Contains(cleared, "stash_ids") || !slices.Contains(restored, "stash_ids") {
			t.Errorf("cleared=%v restored=%v", cleared, restored)
		}
	})
	t.Run("skipped when edited", func(t *testing.T) {
		cur := &stash.Scene{StashIDs: []stash.StashID{{Endpoint: "https://sb", ID: "uuid-different"}}}
		before := fieldSnapshot{StashIDs: nil}
		after := fieldSnapshot{StashIDs: []stash.StashID{b}}
		_, _, restored, notes := diffRestore(cur, before, after)
		if len(restored) != 0 || !containsSubstring(notes, "stash_ids") {
			t.Errorf("restored=%v notes=%v", restored, notes)
		}
	})
}

func TestSameSetAndSameStashIDsIgnoreOrder(t *testing.T) {
	if !sameSet([]string{"a", "b"}, []string{"b", "a"}) {
		t.Error("sameSet should ignore order")
	}
	if sameSet([]string{"a"}, []string{"a", "b"}) {
		t.Error("sameSet should notice a length difference")
	}
	x := stash.StashID{Endpoint: "e1", ID: "1"}
	y := stash.StashID{Endpoint: "e2", ID: "2"}
	if !sameStashIDs([]stash.StashID{x, y}, []stash.StashID{y, x}) {
		t.Error("sameStashIDs should ignore order")
	}
	if sameStashIDs([]stash.StashID{x}, []stash.StashID{x, y}) {
		t.Error("sameStashIDs should notice a length difference")
	}
}

func TestEqualIntPtr(t *testing.T) {
	if !equalIntPtr(nil, nil) {
		t.Error("nil, nil should be equal")
	}
	if equalIntPtr(nil, intPtr(1)) || equalIntPtr(intPtr(1), nil) {
		t.Error("nil vs a value should not be equal")
	}
	if !equalIntPtr(intPtr(5), intPtr(5)) {
		t.Error("equal values should be equal")
	}
	if equalIntPtr(intPtr(5), intPtr(6)) {
		t.Error("different values should not be equal")
	}
}

func TestFieldValueCoversEveryUnionField(t *testing.T) {
	s := fieldSnapshot{
		Title: strPtr("t"), Code: strPtr("c"), Date: strPtr("d"), Details: strPtr("de"),
		Director: strPtr("di"), StudioID: strPtr("s1"), Rating100: intPtr(70), Organized: boolPtr(true),
		URLs: []string{"u"}, TagIDs: []string{"tag"}, PerformerIDs: []string{"p"}, GalleryIDs: []string{"g"},
		StashIDs: []stash.StashID{{Endpoint: "e", ID: "i"}},
	}
	for _, name := range []string{
		"title", "code", "date", "details", "director", "studio_id",
		"rating100", "organized", "urls", "tag_ids", "performer_ids", "gallery_ids", "stash_ids",
	} {
		if fieldValue(s, name) == nil {
			t.Errorf("fieldValue(%q) = nil, want a value", name)
		}
	}
	if fieldValue(s, "nonsense") != nil {
		t.Error("an unknown field name should return nil")
	}
	if fieldValue(fieldSnapshot{}, "rating100") != nil || fieldValue(fieldSnapshot{}, "organized") != nil {
		t.Error("an unset pointer field should return nil")
	}
}

func TestRestoredVerdictInfersPartialFromTheCompletenessStep(t *testing.T) {
	if got := restoredVerdict([]ledger.Step{{Rule: "completeness"}, {Rule: "tier"}}); got != ledger.VerdictPartial {
		t.Errorf("restoredVerdict = %q, want partial", got)
	}
	if got := restoredVerdict([]ledger.Step{{Rule: "tier"}}); got != ledger.VerdictCertain {
		t.Errorf("restoredVerdict = %q, want certain", got)
	}
	if got := restoredVerdict(nil); got != ledger.VerdictCertain {
		t.Errorf("restoredVerdict(nil) = %q, want certain", got)
	}
}

func TestUndoTargetFormatsSignatureOrRun(t *testing.T) {
	if got := undoTarget(UndoOptions{Signature: "sig-1"}); got != "signature sig-1" {
		t.Errorf("undoTarget = %q", got)
	}
	if got := undoTarget(UndoOptions{RunID: 5}); got != "run 5" {
		t.Errorf("undoTarget = %q", got)
	}
}

func TestUndoerLoserReportsAMissingTagOrMoveRow(t *testing.T) {
	scenes := []stash.Scene{scene("11", "/data/_quarantine/worse.mp4", 1<<20)}
	_, c := newFakeStash(t, scenes...)
	l := newLedger(t)
	u := &undoer{client: c, l: l, opts: UndoOptions{}}

	if lr := u.loser(t.Context(), nil, "11"); !containsSubstring(lr.Errs, "no tag mutation") {
		t.Errorf("Errs = %v, want a missing-tag-row error", lr.Errs)
	}

	rows := []ledger.Mutation{{Kind: "tag", Target: "scene 11", To: DefaultTag}}
	if lr := u.loser(t.Context(), rows, "11"); !containsSubstring(lr.Errs, "no move mutation") {
		t.Errorf("Errs = %v, want a missing-move-row error", lr.Errs)
	}
}

func TestUndoerLoserReportsAGoneOrFilelessScene(t *testing.T) {
	empty := scene("11", "/data/_quarantine/worse.mp4", 1<<20)
	empty.Files = nil
	_, c := newFakeStash(t, empty)
	l := newLedger(t)
	u := &undoer{client: c, l: l, opts: UndoOptions{}}
	rows := []ledger.Mutation{
		{Kind: "tag", Target: "scene 11", To: DefaultTag},
		{Kind: "tag", Target: "scene 99", To: DefaultTag},
	}

	if lr := u.loser(t.Context(), rows, "11"); !containsSubstring(lr.Errs, "has no file") {
		t.Errorf("Errs = %v, want a no-file error", lr.Errs)
	}
	if lr := u.loser(t.Context(), rows, "99"); !containsSubstring(lr.Errs, "gone from Stash") {
		t.Errorf("Errs = %v, want a gone-scene error", lr.Errs)
	}
}
