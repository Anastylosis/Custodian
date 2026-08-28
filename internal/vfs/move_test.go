package vfs

import (
	"context"
	"errors"
	"io"
	"testing"
)

// faultyFS wraps a Mem and lets tests inject failures at specific points in
// the cross-backend copy so streamFile's and Move's error-handling branches
// can be exercised without a real broken filesystem.
type faultyFS struct {
	*Mem
	openErr   error
	createErr error
	writeErr  error
	closeErr  error
	renameErr error
	removeErr error
	mkdirErr  error
	existsErr error
}

func (f *faultyFS) Exists(ctx context.Context, p string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	return f.Mem.Exists(ctx, p)
}

func (f *faultyFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.Mem.Open(ctx, p)
}

func (f *faultyFS) Create(ctx context.Context, p string) (io.WriteCloser, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	w, err := f.Mem.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	if f.writeErr == nil && f.closeErr == nil {
		return w, nil
	}
	return &faultyWriter{w: w, writeErr: f.writeErr, closeErr: f.closeErr}, nil
}

func (f *faultyFS) Rename(ctx context.Context, src, dst string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.Mem.Rename(ctx, src, dst)
}

func (f *faultyFS) Remove(ctx context.Context, p string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.Mem.Remove(ctx, p)
}

func (f *faultyFS) MkdirAll(ctx context.Context, dir string) error {
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	return f.Mem.MkdirAll(ctx, dir)
}

type faultyWriter struct {
	w        io.WriteCloser
	writeErr error
	closeErr error
}

func (w *faultyWriter) Write(b []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.w.Write(b)
}

func (w *faultyWriter) Close() error {
	if w.closeErr != nil {
		return w.closeErr
	}
	return w.w.Close()
}

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

func TestMove_NilFilesystem(t *testing.T) {
	dst := NewMem()
	if err := Move(context.Background(), nil, dst, "/a", "/b"); err == nil {
		t.Error("Move with a nil source FS should error")
	}
	src := NewMem("/a")
	if err := Move(context.Background(), src, nil, "/a", "/b"); err == nil {
		t.Error("Move with a nil destination FS should error")
	}
}

func TestMove_SourceExistsCheckFails(t *testing.T) {
	src := &faultyFS{Mem: NewMem(), existsErr: errors.New("stat failed")}
	dst := NewMem()

	if err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt"); err == nil {
		t.Fatal("expected an error when checking whether the source exists fails")
	}
}

func TestMove_DestExistsCheckFails(t *testing.T) {
	src := NewMem("/a/x.srt")
	dst := &faultyFS{Mem: NewMem(), existsErr: errors.New("stat failed")}

	if err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt"); err == nil {
		t.Fatal("expected an error when checking whether the destination exists fails")
	}
}

func TestMove_DestMkdirAllFails(t *testing.T) {
	src := NewMem("/a/x.srt")
	dst := &faultyFS{Mem: NewMem(), mkdirErr: errors.New("no space")}

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when the destination cannot make room")
	}
}

func TestMove_StreamFileOpenFails(t *testing.T) {
	src := &faultyFS{Mem: NewMem("/a/x.srt"), openErr: errors.New("permission denied")}
	dst := NewMem()

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when the source cannot be opened")
	}
	if dst.Files["/b/y.srt.sj-partial"] {
		t.Error("no temporary file should exist when the copy never started")
	}
}

func TestMove_StreamFileCreateFails(t *testing.T) {
	src := NewMem("/a/x.srt")
	dst := &faultyFS{Mem: NewMem(), createErr: errors.New("read-only filesystem")}

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when the destination cannot be created")
	}
}

func TestMove_StreamFileCopyFails(t *testing.T) {
	src := NewMem("/a/x.srt")
	src.Contents["/a/x.srt"] = "body"
	dst := &faultyFS{Mem: NewMem(), writeErr: errors.New("disk full")}

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when the copy fails mid-stream")
	}
	if dst.Files["/b/y.srt.sj-partial"] {
		t.Error("the failed temporary should have been removed")
	}
}

func TestMove_StreamFileCloseFails(t *testing.T) {
	src := NewMem("/a/x.srt")
	src.Contents["/a/x.srt"] = "body"
	dst := &faultyFS{Mem: NewMem(), closeErr: errors.New("close failed")}

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when closing the temporary fails")
	}
}

func TestMove_FinaliseRenameFails(t *testing.T) {
	src := NewMem("/a/x.srt")
	src.Contents["/a/x.srt"] = "body"
	dst := &faultyFS{Mem: NewMem(), renameErr: errors.New("finalise failed")}

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when the temporary cannot be renamed into place")
	}
}

func TestMove_SourceRemoveFailsAfterASuccessfulCopy(t *testing.T) {
	src := &faultyFS{Mem: NewMem("/a/x.srt"), removeErr: errors.New("permission denied")}
	src.Contents["/a/x.srt"] = "body"
	dst := NewMem()

	err := Move(context.Background(), src, dst, "/a/x.srt", "/b/y.srt")
	if err == nil {
		t.Fatal("expected an error when the source cannot be removed post-copy")
	}
	if !dst.Files["/b/y.srt"] {
		t.Error("the destination should still hold the copy even though cleanup failed")
	}
}

func TestDirOf(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/a/b/c.srt", "/a/b"},
		{"/c.srt", "/"},
		{"c.srt", "/"},
		{`Z:\a\b.srt`, `Z:\a`},
	}
	for _, tc := range tests {
		if got := dirOf(tc.in); got != tc.want {
			t.Errorf("dirOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
