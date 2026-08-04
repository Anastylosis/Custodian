package sidecar

import (
	"context"
	"errors"
	"testing"

	"github.com/Anastylosis/Custodian/internal/pathmap"
	"github.com/Anastylosis/Custodian/internal/vfs"
)

func newTestMover(t *testing.T, fs vfs.FS, rules []pathmap.Rule) *Mover {
	t.Helper()
	mp, err := pathmap.New(rules)
	if err != nil {
		t.Fatalf("pathmap.New: %v", err)
	}
	mv, err := NewMover(fs, mp, nil)
	if err != nil {
		t.Fatalf("NewMover: %v", err)
	}
	return mv
}

// The end-to-end shape of the bug this feature fixes: organize moves a video
// out of tmp2/ into a performer folder, and the subtitle must follow.
func TestMover_MovesSidecarWithVideo(t *testing.T) {
	fs := vfs.NewMem(
		"/mnt/lib/tmp2/clip_1080p.mp4",
		"/mnt/lib/tmp2/clip_1080p.en.srt",
		"/mnt/lib/tmp2/clip_1080p.es.srt",
		"/mnt/lib/tmp2/unrelated.mp4",
	)
	mv := newTestMover(t, fs, []pathmap.Rule{{StashPrefix: "/data", LocalPrefix: "/mnt/lib"}})
	ctx := context.Background()

	moves, err := mv.Plan(ctx, "/data/tmp2/clip_1080p.mp4", "/data/River Stone/2024_Clip_4k.mp4")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(moves) != 2 {
		t.Fatalf("Plan returned %d moves, want 2: %+v", len(moves), moves)
	}
	if moves[0].DstStash != "/data/River Stone/2024_Clip_4k.en.srt" {
		t.Errorf("unexpected destination: %q", moves[0].DstStash)
	}

	res := mv.Apply(ctx, moves)
	if len(res.Moved) != 2 || len(res.Failed) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("Apply = moved:%d skipped:%d failed:%d",
			len(res.Moved), len(res.Skipped), len(res.Failed))
	}
	for _, want := range []string{
		"/mnt/lib/River Stone/2024_Clip_4k.en.srt",
		"/mnt/lib/River Stone/2024_Clip_4k.es.srt",
	} {
		if !fs.Files[want] {
			t.Errorf("expected %q to exist after apply. Have:\n%s", want, fs)
		}
	}
	if fs.Files["/mnt/lib/tmp2/clip_1080p.en.srt"] {
		t.Error("source sidecar still present after move")
	}
}

// A rename in place keeps the language suffix, which is what allows a scene to
// carry en + es tracks simultaneously.
func TestMover_RenameInPlacePreservesLanguages(t *testing.T) {
	fs := vfs.NewMem(
		"/lib/a/old.mp4",
		"/lib/a/old.en.srt",
		"/lib/a/old.zh.srt",
	)
	mv := newTestMover(t, fs, []pathmap.Rule{{StashPrefix: "/data", LocalPrefix: "/lib"}})
	ctx := context.Background()

	moves, err := mv.Plan(ctx, "/data/a/old.mp4", "/data/a/new.mp4")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res := mv.Apply(ctx, moves)
	if len(res.Moved) != 2 {
		t.Fatalf("moved %d, want 2 (%+v)", len(res.Moved), res)
	}
	for _, want := range []string{"/lib/a/new.en.srt", "/lib/a/new.zh.srt"} {
		if !fs.Files[want] {
			t.Errorf("missing %q after rename. Have:\n%s", want, fs)
		}
	}
}

// Never clobber: an occupied destination is skipped, not overwritten.
func TestMover_SkipsRatherThanOverwrites(t *testing.T) {
	fs := vfs.NewMem(
		"/lib/a/old.mp4",
		"/lib/a/old.srt",
		"/lib/b/new.srt", // already occupied
	)
	mv := newTestMover(t, fs, []pathmap.Rule{{StashPrefix: "/data", LocalPrefix: "/lib"}})
	ctx := context.Background()

	moves, err := mv.Plan(ctx, "/data/a/old.mp4", "/data/b/new.mp4")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res := mv.Apply(ctx, moves)
	if len(res.Skipped) != 1 || len(res.Moved) != 0 {
		t.Fatalf("want 1 skip and 0 moves, got %+v", res)
	}
	if !fs.Files["/lib/a/old.srt"] {
		t.Error("source should be left untouched when the destination is occupied")
	}
}

// A failing sidecar is reported, and does not prevent the others from moving.
func TestMover_FailureIsIsolated(t *testing.T) {
	fs := vfs.NewMem(
		"/lib/a/old.mp4",
		"/lib/a/old.en.srt",
		"/lib/a/old.es.srt",
	)
	boom := errors.New("permission denied")
	fs.FailRename["/lib/a/old.en.srt"] = boom

	mv := newTestMover(t, fs, []pathmap.Rule{{StashPrefix: "/data", LocalPrefix: "/lib"}})
	ctx := context.Background()

	moves, err := mv.Plan(ctx, "/data/a/old.mp4", "/data/a/new.mp4")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	res := mv.Apply(ctx, moves)
	if len(res.Failed) != 1 || len(res.Moved) != 1 {
		t.Fatalf("want 1 failure and 1 move, got %+v", res)
	}
	if !errors.Is(res.Failed[0].Err, boom) {
		t.Errorf("failure lost its cause: %v", res.Failed[0].Err)
	}
	if !fs.Files["/lib/a/new.es.srt"] {
		t.Error("the healthy sidecar should still have moved")
	}
}

// A nil Mover is the "sidecars disabled" configuration and must be inert.
func TestMover_NilIsInert(t *testing.T) {
	var mv *Mover
	moves, err := mv.Plan(context.Background(), "/data/a.mp4", "/data/b.mp4")
	if err != nil || moves != nil {
		t.Fatalf("nil Mover Plan = (%v, %v), want (nil, nil)", moves, err)
	}
	if res := mv.Apply(context.Background(), nil); res.Total() != 0 {
		t.Errorf("nil Mover Apply did something: %+v", res)
	}
	if mv.Describe() != "disabled" {
		t.Errorf("Describe = %q", mv.Describe())
	}
}

// Windows destinations must be built with backslashes even though Stash
// reports POSIX paths.
func TestMover_WindowsPaths(t *testing.T) {
	fs := vfs.NewMem(
		`Z:\Videos\a\old.mp4`,
		`Z:\Videos\a\old.en.srt`,
	)
	mv := newTestMover(t, fs, []pathmap.Rule{{StashPrefix: "/data", LocalPrefix: `Z:\Videos`}})
	ctx := context.Background()

	moves, err := mv.Plan(ctx, "/data/a/old.mp4", "/data/b/new.mp4")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(moves) != 1 {
		t.Fatalf("want 1 move, got %+v", moves)
	}
	if got, want := moves[0].DstLocal, `Z:\Videos\b\new.en.srt`; got != want {
		t.Errorf("DstLocal = %q, want %q", got, want)
	}
	if got, want := moves[0].DstStash, "/data/b/new.en.srt"; got != want {
		t.Errorf("DstStash = %q, want %q", got, want)
	}
}

// An unmapped path must fail loudly rather than silently skipping sidecars.
func TestMover_UnmappedPathErrors(t *testing.T) {
	fs := vfs.NewMem()
	mv := newTestMover(t, fs, []pathmap.Rule{{StashPrefix: "/data", LocalPrefix: "/lib"}})
	if _, err := mv.Plan(context.Background(), "/other/a.mp4", "/other/b.mp4"); err == nil {
		t.Fatal("expected an error for an unmapped path")
	}
}
