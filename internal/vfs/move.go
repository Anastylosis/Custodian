package vfs

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Move relocates src on srcFS to dst on dstFS.
//
// When both sides are the same filesystem this is a plain rename. When they
// differ — the common case being subtitles sitting on a workstation while the
// library lives on a NAS — the file is streamed across and the original then
// removed, so "move a subtitle into the library" is a single operation
// regardless of where the two ends happen to be.
//
// The destination is never overwritten, and the transfer lands on a temporary
// name first: a half-written subtitle left at the real path would be indexed by
// Stash as a valid caption.
func Move(ctx context.Context, srcFS, dstFS FS, src, dst string) error {
	if srcFS == nil || dstFS == nil {
		return fmt.Errorf("vfs: nil filesystem")
	}
	if srcFS == dstFS {
		return dstFS.Rename(ctx, src, dst)
	}

	if ok, err := srcFS.Exists(ctx, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotExist, src)
	}
	if ok, err := dstFS.Exists(ctx, dst); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExist, dst)
	}
	if err := dstFS.MkdirAll(ctx, dirOf(dst)); err != nil {
		return err
	}

	tmp := dst + ".sj-partial"
	if err := streamFile(ctx, srcFS, dstFS, src, tmp); err != nil {
		_ = dstFS.Remove(ctx, tmp)
		return err
	}
	if err := dstFS.Rename(ctx, tmp, dst); err != nil {
		_ = dstFS.Remove(ctx, tmp)
		return fmt.Errorf("finalise %q: %w", dst, err)
	}
	if err := srcFS.Remove(ctx, src); err != nil {
		// The file is safely in place; report the leftover rather than
		// failing a move that actually succeeded.
		return fmt.Errorf("copied to %q but could not remove original %q: %w", dst, src, err)
	}
	return nil
}

func streamFile(ctx context.Context, srcFS, dstFS FS, src, tmp string) error {
	in, err := srcFS.Open(ctx, src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := dstFS.Create(ctx, tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %q: %w", src, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %q: %w", tmp, err)
	}
	return nil
}

// dirOf returns everything before the final separator, honouring both styles
// so a POSIX source can be written to a Windows destination.
func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return "/"
	}
	return p[:i]
}
