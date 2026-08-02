package vfs

import (
	"context"
	"errors"
	"testing"
)

// The case this exists for: subtitles on the machine running the tool, library
// on another. A move has to cross the two in one operation.
func TestMove_AcrossBackends(t *testing.T) {
	src := NewMem("/home/me/Subs/Performer/clip.en.srt")
	src.Contents["/home/me/Subs/Performer/clip.en.srt"] = "1\n00:00:01,000 --> 00:00:02,000\nhi\n"
	dst := NewMem("/share/lib/Performer/clip.mp4")

	err := Move(context.Background(), src, dst,
		"/home/me/Subs/Performer/clip.en.srt",
		"/share/lib/Performer/clip.en.srt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}

	if !dst.Files["/share/lib/Performer/clip.en.srt"] {
		t.Errorf("destination missing. dst has:\n%s", dst)
	}
	if got := dst.Contents["/share/lib/Performer/clip.en.srt"]; got == "" {
		t.Error("destination is empty; contents were not streamed")
	}
	if src.Files["/home/me/Subs/Performer/clip.en.srt"] {
		t.Error("source still present after a move")
	}
	// The temporary must not survive: Stash would index it as a caption.
	if dst.Files["/share/lib/Performer/clip.en.srt.sj-partial"] {
		t.Error("temporary file left behind")
	}
}

func TestMove_AcrossBackendsNeverOverwrites(t *testing.T) {
	src := NewMem("/a/x.srt")
	src.Contents["/a/x.srt"] = "new"
	dst := NewMem("/b/y.srt")
	dst.Contents["/b/y.srt"] = "PRECIOUS"

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if !errors.Is(err, ErrExist) {
		t.Fatalf("Move over an existing file = %v, want ErrExist", err)
	}
	if dst.Contents["/b/y.srt"] != "PRECIOUS" {
		t.Error("destination was modified")
	}
	if !src.Files["/a/x.srt"] {
		t.Error("source should be untouched when the move is refused")
	}
}

func TestMove_AcrossBackendsMissingSource(t *testing.T) {
	src := NewMem()
	dst := NewMem()
	err := Move(context.Background(), src, dst, "/a/nope.srt", "/b/x.srt")
	if !errors.Is(err, ErrNotExist) {
		t.Fatalf("Move of a missing source = %v, want ErrNotExist", err)
	}
}

// Same filesystem on both sides must still be a plain rename, not a
// copy-and-delete round trip.
func TestMove_SameBackendUsesRename(t *testing.T) {
	fs := NewMem("/lib/a.srt")
	fs.Contents["/lib/a.srt"] = "body"

	if err := Move(context.Background(), fs, fs, "/lib/a.srt", "/lib/b.srt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if len(fs.Renames) != 1 {
		t.Errorf("expected exactly one rename, got %v", fs.Renames)
	}
	if !fs.Files["/lib/b.srt"] || fs.Files["/lib/a.srt"] {
		t.Errorf("unexpected file set:\n%s", fs)
	}
}
