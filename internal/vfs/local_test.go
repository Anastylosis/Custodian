package vfs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Local is the backend that actually moves files on disk. Every guarantee it
// makes is one the user is relying on without being able to see it: that a
// destination is never clobbered, that a cross-filesystem move still works,
// and that an interrupted copy never leaves a truncated subtitle where Stash
// would index it as valid.

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func TestLocalReadDirReportsNamesDirsAndSizes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.srt"), "hello")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := NewLocal().ReadDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(got), got)
	}
	byName := map[string]Entry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if f := byName["a.srt"]; f.IsDir || f.Size != 5 {
		t.Errorf("a.srt = %+v, want a 5-byte file", f)
	}
	if d := byName["sub"]; !d.IsDir {
		t.Errorf("sub = %+v, want a directory", d)
	}
}

// A missing directory is emptiness, not an error: the scan walks candidate
// locations that legitimately may not exist, and failing on the first absent
// one would abort the whole run.
func TestLocalReadDirTreatsAMissingDirectoryAsEmpty(t *testing.T) {
	got, err := NewLocal().ReadDir(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("ReadDir of a missing directory errored: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("entries = %+v, want none", got)
	}
}

func TestLocalExists(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "there"), "x")
	l := NewLocal()

	if ok, err := l.Exists(context.Background(), filepath.Join(dir, "there")); err != nil || !ok {
		t.Errorf("Exists(there) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := l.Exists(context.Background(), filepath.Join(dir, "absent")); err != nil || ok {
		t.Errorf("Exists(absent) = %v, %v; want false, nil", ok, err)
	}
}

// Lstat rather than Stat: a broken symlink at the destination still occupies
// the name, so a move onto it must be refused rather than silently succeed.
func TestLocalExistsSeesABrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "nothing-here"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ok, err := NewLocal().Exists(context.Background(), link)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("a broken symlink reported as absent; a move would clobber the name")
	}
}

func TestLocalRenameMovesAndCreatesParents(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "loose", "x.srt")
	dst := filepath.Join(dir, "library", "Show", "Ep01.en.srt")
	writeFile(t, src, "subtitle body")

	if err := NewLocal().Rename(context.Background(), src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if readFile(t, dst) != "subtitle body" {
		t.Error("destination content is wrong")
	}
	if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
		t.Error("source still exists after a move")
	}
}

// The guarantee that matters most. os.Rename silently overwrites on POSIX, so
// this is checked explicitly — without it, moving a subtitle onto an existing
// one destroys the original with no error and no way to tell.
func TestLocalRenameNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new.srt")
	dst := filepath.Join(dir, "existing.srt")
	writeFile(t, src, "new")
	writeFile(t, dst, "PRECIOUS")

	err := NewLocal().Rename(context.Background(), src, dst)
	if !errors.Is(err, ErrExist) {
		t.Fatalf("error = %v, want ErrExist", err)
	}
	if got := readFile(t, dst); got != "PRECIOUS" {
		t.Errorf("destination was overwritten: %q", got)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Error("source was removed despite the move being refused")
	}
}

func TestLocalRenameMissingSource(t *testing.T) {
	dir := t.TempDir()
	err := NewLocal().Rename(context.Background(),
		filepath.Join(dir, "absent"), filepath.Join(dir, "dst"))
	if !errors.Is(err, ErrNotExist) {
		t.Fatalf("error = %v, want ErrNotExist", err)
	}
}

func TestLocalRenamePropagatesDestinationExistsCheckError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFile(t, src, "x")

	err := NewLocal().Rename(context.Background(), src, "bad\x00path")
	if err == nil {
		t.Fatal("Rename with an invalid destination returned no error")
	}
	if errors.Is(err, ErrExist) {
		t.Error("an invalid-path stat error was reported as ErrExist")
	}
}

func TestLocalRenamePropagatesMkdirFailure(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFile(t, src, "x")
	blocker := filepath.Join(dir, "blocker")
	writeFile(t, blocker, "x")

	err := NewLocal().Rename(context.Background(), src, filepath.Join(blocker, "child"))
	if err == nil {
		t.Fatal("Rename onto a destination whose parent is a file returned no error")
	}
}

// copyThenRemove is the cross-filesystem path. It cannot be reached from a
// single temp dir — os.Rename succeeds there — so it is exercised directly.
// It is the branch that runs when the library is on a NAS, which is the
// configuration this feature exists for.
func TestCopyThenRemoveMovesContentAndClearsTheSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.srt")
	dst := filepath.Join(dir, "out", "dst.srt")
	writeFile(t, src, "streamed across")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	if readFile(t, dst) != "streamed across" {
		t.Error("content did not survive the copy")
	}
	if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
		t.Error("source survived a move")
	}
	// No partial file left behind. One sitting at the real path would be
	// indexed by Stash as a valid caption.
	entries, _ := os.ReadDir(filepath.Dir(dst))
	for _, e := range entries {
		if e.Name() != "dst.srt" {
			t.Errorf("leftover file after move: %s", e.Name())
		}
	}
}

// The message-matching in isCrossDevice is what decides between "fall back to
// copying" and "fail". Getting it wrong either loses the NAS case or masks a
// genuine rename failure as one.
func TestIsCrossDevice(t *testing.T) {
	linkErr := func(msg string) error {
		return &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errors.New(msg)}
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"linux EXDEV", linkErr("invalid cross-device link"), true},
		{"spaced wording", linkErr("cross device link"), true},
		{"windows wording", linkErr("the system cannot move the file to a different disk drive"), true},
		{"case insensitive", linkErr("Invalid Cross-Device Link"), true},
		// A permission failure must NOT be treated as cross-device, or a real
		// error becomes a silent copy attempt that fails differently.
		{"permission denied", linkErr("permission denied"), false},
		{"not a link error", errors.New("invalid cross-device link"), false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCrossDevice(tc.err); got != tc.want {
				t.Errorf("isCrossDevice(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestLocalCreateOpenRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "round.srt")
	l := NewLocal()
	ctx := context.Background()

	w, err := l.Create(ctx, path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := io.WriteString(w, "body"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := l.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if string(got) != "body" {
		t.Errorf("read %q, want body", got)
	}

	if err := l.Remove(ctx, path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ok, _ := l.Exists(ctx, path); ok {
		t.Error("file still exists after Remove")
	}
}

func TestLocalOpenMissingFileErrors(t *testing.T) {
	if _, err := NewLocal().Open(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("opening a missing file returned no error")
	}
}

// Every method checks the context first. A cancelled scan must stop touching
// the filesystem rather than finish walking a large library.
func TestLocalHonoursACancelledContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.srt"), "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := NewLocal()

	if _, err := l.ReadDir(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadDir = %v, want context.Canceled", err)
	}
	if _, err := l.Open(ctx, filepath.Join(dir, "a.srt")); !errors.Is(err, context.Canceled) {
		t.Errorf("Open = %v, want context.Canceled", err)
	}
	if _, err := l.Exists(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("Exists = %v, want context.Canceled", err)
	}
	if err := l.MkdirAll(ctx, filepath.Join(dir, "new")); !errors.Is(err, context.Canceled) {
		t.Errorf("MkdirAll = %v, want context.Canceled", err)
	}
	if err := l.Rename(ctx, filepath.Join(dir, "a.srt"), filepath.Join(dir, "b.srt")); !errors.Is(err, context.Canceled) {
		t.Errorf("Rename = %v, want context.Canceled", err)
	}
}

func TestLocalReadDirNonNotExistErrorIsWrapped(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	// os.ReadDir on a regular file fails with something other than
	// os.ErrNotExist, which must surface rather than be swallowed as "empty".
	if _, err := NewLocal().ReadDir(context.Background(), file); err == nil {
		t.Fatal("ReadDir on a file returned no error")
	}
}

func TestLocalExistsNonNotExistErrorIsWrapped(t *testing.T) {
	// A NUL byte makes the path invalid, which os.Lstat rejects with an error
	// other than ErrNotExist — that must be reported, not swallowed as "absent".
	_, err := NewLocal().Exists(context.Background(), "bad\x00path")
	if err == nil {
		t.Fatal("Exists on an invalid path returned no error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Error("an invalid-path error was reported as ErrNotExist")
	}
}

func TestLocalMkdirAllCreatesNestedDirs(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c")
	if err := NewLocal().MkdirAll(context.Background(), target); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		t.Errorf("target not created as a directory: %v, %v", fi, err)
	}
}

func TestLocalMkdirAllErrorsWhenAComponentIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	writeFile(t, blocker, "x")

	err := NewLocal().MkdirAll(context.Background(), filepath.Join(blocker, "child"))
	if err == nil {
		t.Fatal("MkdirAll through a file component returned no error")
	}
}

func TestLocalCreateErrorsWhenParentIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	writeFile(t, blocker, "x")

	if _, err := NewLocal().Create(context.Background(), filepath.Join(blocker, "child.srt")); err == nil {
		t.Fatal("Create under a file parent returned no error")
	}
}

func TestLocalCreateErrorsOnAnExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal().Create(context.Background(), sub); err == nil {
		t.Fatal("Create onto an existing directory returned no error")
	}
}

func TestLocalRemoveOfAMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := NewLocal().Remove(context.Background(), filepath.Join(dir, "absent")); err != nil {
		t.Errorf("Remove(absent) = %v, want nil", err)
	}
}

func TestLocalRemoveErrorsOnANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	writeFile(t, filepath.Join(sub, "child"), "x")

	if err := NewLocal().Remove(context.Background(), sub); err == nil {
		t.Fatal("Remove of a non-empty directory returned no error")
	}
}

func TestCopyThenRemoveErrorsWhenSourceIsMissing(t *testing.T) {
	dir := t.TempDir()
	err := copyThenRemove(filepath.Join(dir, "absent"), filepath.Join(dir, "dst"))
	if err == nil {
		t.Fatal("copyThenRemove with a missing source returned no error")
	}
}

func TestCopyThenRemoveErrorsWhenAPartialFileAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.srt")
	dst := filepath.Join(dir, "dst.srt")
	writeFile(t, src, "body")
	writeFile(t, dst+".sj-partial", "stale leftover")

	err := copyThenRemove(src, dst)
	if err == nil {
		t.Fatal("copyThenRemove with a pre-existing partial file returned no error")
	}
	if _, statErr := os.Lstat(src); statErr != nil {
		t.Error("source was removed despite the copy failing")
	}
}

func TestCopyThenRemoveErrorsWhenSourceIsADirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src-dir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.srt")

	if err := copyThenRemove(src, dst); err == nil {
		t.Fatal("copyThenRemove with a directory source returned no error")
	}
	// The partial file must not be left behind after a failed copy.
	if _, statErr := os.Lstat(dst + ".sj-partial"); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a partial file was left behind after a failed copy")
	}
}

func TestCopyThenRemoveErrorsWhenDestinationIsADirectory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.srt")
	writeFile(t, src, "body")
	dst := filepath.Join(dir, "dst-is-a-dir")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyThenRemove(src, dst); err == nil {
		t.Fatal("copyThenRemove onto an existing directory returned no error")
	}
	if _, statErr := os.Lstat(dst + ".sj-partial"); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a partial file was left behind after a failed finalise")
	}
}

// Regression guard for e9378a6: the source handle must be closed before
// os.Remove is attempted, or the final unlink fails on Windows even though
// the copy itself succeeded.
func TestCopyThenRemoveClosesSourceBeforeRemoving(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.srt")
	dst := filepath.Join(dir, "out", "dst.srt")
	writeFile(t, src, "streamed across")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := copyThenRemove(src, dst); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	// If the source handle were still open, a second explicit removal attempt
	// mirrors what an OS holding the file open would refuse; here it must
	// already be gone.
	if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
		t.Error("source still present after copyThenRemove")
	}
}

func TestLocalReadDir_NonDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	if _, err := NewLocal().ReadDir(context.Background(), file); err == nil {
		t.Fatal("ReadDir on a file should error")
	}
}

func TestLocalExists_NonDirectoryParentErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	if _, err := NewLocal().Exists(context.Background(), filepath.Join(file, "child")); err == nil {
		t.Fatal("Exists under a non-directory parent should error rather than report absence")
	}
}

func TestLocalMkdirAll(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b", "c")

	if err := NewLocal().MkdirAll(context.Background(), target); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		t.Fatalf("MkdirAll did not create %q", target)
	}
}

func TestLocalMkdirAll_NonDirectoryParentErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	if err := NewLocal().MkdirAll(context.Background(), filepath.Join(file, "child")); err == nil {
		t.Fatal("MkdirAll under a non-directory parent should error")
	}
}

func TestLocalRename_SrcExistsCheckErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	err := NewLocal().Rename(context.Background(), filepath.Join(file, "child"), filepath.Join(dir, "dst"))
	if err == nil {
		t.Fatal("Rename should surface an error checking a source under a non-directory parent")
	}
}

func TestLocalRename_DstExistsCheckErrors(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeFile(t, src, "x")
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	err := NewLocal().Rename(context.Background(), src, filepath.Join(file, "child"))
	if err == nil {
		t.Fatal("Rename should surface an error checking a destination under a non-directory parent")
	}
}

// os.Rename refuses to move a directory into its own subtree with EINVAL —
// a real failure that is not the cross-device signature, so it must be
// reported rather than mistaken for one and retried as a copy.
func TestLocalRename_NonCrossDeviceFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "srcdir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(src, "nested", "target")

	err := NewLocal().Rename(context.Background(), src, dst)
	if err == nil {
		t.Fatal("expected an error renaming a directory into its own subtree")
	}
	if errors.Is(err, ErrExist) || errors.Is(err, ErrNotExist) {
		t.Errorf("unexpected sentinel error: %v", err)
	}
}

func TestCopyThenRemove_OpenSourceFails(t *testing.T) {
	dir := t.TempDir()
	err := copyThenRemove(filepath.Join(dir, "absent"), filepath.Join(dir, "dst"))
	if err == nil {
		t.Fatal("expected an error opening a missing source")
	}
}

func TestCopyThenRemove_TempAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.srt")
	dst := filepath.Join(dir, "dst.srt")
	writeFile(t, src, "body")
	writeFile(t, dst+".sj-partial", "leftover from a crashed run")

	err := copyThenRemove(src, dst)
	if err == nil {
		t.Fatal("expected an error when the temporary name is already occupied")
	}
}

func TestCopyThenRemove_ReadingSourceFails(t *testing.T) {
	dir := t.TempDir()
	// A directory opens successfully but cannot be read as a file, so the
	// failure surfaces from io.Copy rather than os.Open.
	src := filepath.Join(dir, "srcdir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.srt")

	if err := copyThenRemove(src, dst); err == nil {
		t.Fatal("expected an error copying from a directory")
	}
}

func TestCopyThenRemove_FinaliseRenameFails(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.srt")
	writeFile(t, src, "body")
	// dst's parent does not exist, so the final rename fails.
	dst := filepath.Join(dir, "missing-parent", "dst.srt")

	if err := copyThenRemove(src, dst); err == nil {
		t.Fatal("expected an error finalising into a missing directory")
	}
	if _, err := os.Lstat(dst + ".sj-partial"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the temporary should have been cleaned up after a failed finalise")
	}
}

func TestLocalCreate_HonoursACancelledContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLocal().Create(ctx, filepath.Join(dir, "x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Create = %v, want context.Canceled", err)
	}
}

func TestLocalCreate_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	writeFile(t, file, "x")

	if _, err := NewLocal().Create(context.Background(), filepath.Join(file, "child")); err == nil {
		t.Fatal("Create under a non-directory parent should error")
	}
}

func TestLocalCreate_OpenFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal().Create(context.Background(), filepath.Join(dir, "adir")); err == nil {
		t.Fatal("Create on an existing directory should error")
	}
}

func TestLocalRemove_HonoursACancelledContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewLocal().Remove(ctx, filepath.Join(dir, "x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Remove = %v, want context.Canceled", err)
	}
}

func TestLocalRemove_MissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	if err := NewLocal().Remove(context.Background(), filepath.Join(dir, "absent")); err != nil {
		t.Errorf("Remove of a missing file = %v, want nil", err)
	}
}

func TestLocalRemove_NonEmptyDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "child"), "x")

	if err := NewLocal().Remove(context.Background(), sub); err == nil {
		t.Fatal("Remove of a non-empty directory should error")
	}
}

func TestLocalDescribe(t *testing.T) {
	if NewLocal().Describe() == "" {
		t.Error("Describe is empty; it names the backend in reports")
	}
	if err := NewLocal().Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
