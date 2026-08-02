package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Local is an FS backed by the machine's own filesystem. It covers running
// stash-janitor directly on the NAS, on a Linux box with the share mounted,
// and on Windows with a mapped drive or UNC path.
type Local struct{}

// NewLocal returns a filesystem-backed FS.
func NewLocal() *Local { return &Local{} }

func (l *Local) Describe() string { return "local filesystem" }
func (l *Local) Close() error     { return nil }

func (l *Local) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %q: %w", dir, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		var size int64
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		out = append(out, Entry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	return out, nil
}

func (l *Local) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", p, err)
	}
	return f, nil
}

func (l *Local) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %q: %w", path, err)
}

func (l *Local) MkdirAll(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}
	return nil
}

func (l *Local) Rename(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ok, err := l.Exists(ctx, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotExist, src)
	}
	// Never clobber. This is checked explicitly rather than relying on
	// os.Rename, which silently overwrites on POSIX.
	if ok, err := l.Exists(ctx, dst); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExist, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir for %q: %w", dst, err)
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !isCrossDevice(err) {
		return fmt.Errorf("rename %q → %q: %w", src, dst, err)
	}
	// Different filesystems (a library spanning mounts, or a bind mount on a
	// NAS): fall back to copy-then-delete.
	return copyThenRemove(src, dst)
}

// isCrossDevice reports whether err is the "invalid cross-device link" that
// os.Rename returns when src and dst live on different filesystems.
func isCrossDevice(err error) bool {
	var le *os.LinkError
	if !errors.As(err, &le) {
		return false
	}
	// EXDEV is 18 on Linux and 17 on Darwin; on Windows a cross-volume move
	// surfaces with its own wording. Matching on the message keeps this
	// portable without per-GOOS syscall constants.
	msg := strings.ToLower(le.Err.Error())
	return strings.Contains(msg, "cross-device") ||
		strings.Contains(msg, "cross device") ||
		strings.Contains(msg, "different disk drive")
}

// copyThenRemove performs a cross-filesystem move. The destination is written
// to a temporary name first so an interrupted copy cannot leave a truncated
// subtitle sitting where Stash would pick it up.
func copyThenRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer in.Close()

	tmp := dst + ".sj-partial"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %q: %w", tmp, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("copy %q → %q: %w", src, dst, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("sync %q: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalise %q: %w", dst, err)
	}
	if err := os.Remove(src); err != nil {
		// The copy succeeded, so the sidecar is in place; report the leftover
		// rather than failing the whole move.
		return fmt.Errorf("copied to %q but could not remove original %q: %w", dst, src, err)
	}
	return nil
}

func (l *Local) Create(ctx context.Context, p string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir for %q: %w", p, err)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create %q: %w", p, err)
	}
	return f, nil
}

func (l *Local) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %q: %w", p, err)
	}
	return nil
}
