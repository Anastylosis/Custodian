// Package vfs is the narrow filesystem interface stash-janitor needs in order
// to move sidecar files.
//
// Everything else in the tool mutates through Stash's GraphQL API and touches
// no files. Sidecars are the one exception, and they are forced: Stash does not
// index subtitles as files, so they have no file ID and moveFiles cannot move
// them. See package sidecar for the full explanation.
//
// The interface is deliberately tiny — list, exists, rename, mkdir — so that a
// local mount, a Windows share and an SSH connection can all satisfy it, and so
// the planning logic can be tested against an in-memory implementation.
//
// Paths passed to an FS are always backend-native: already translated through
// pathmap. Implementations must not apply path/filepath, whose separator
// follows the machine running the tool rather than the one holding the files.
package vfs

import (
	"context"
	"errors"
	"io"
)

// ErrNotExist is returned by Rename when the source is missing.
var ErrNotExist = errors.New("vfs: file does not exist")

// ErrExist is returned by Rename when the destination is already occupied.
// Sidecar moves never overwrite: clobbering a subtitle is unrecoverable,
// whereas leaving one behind is not.
var ErrExist = errors.New("vfs: destination already exists")

// Entry is one directory entry.
type Entry struct {
	Name  string
	IsDir bool
	Size  int64
}

// FS is the minimum filesystem surface needed to relocate sidecar files and
// to scan a folder of loose subtitles.
type FS interface {
	// ReadDir returns the entries in dir. A missing directory returns an empty
	// slice and no error — callers treat "no directory" and "nothing here"
	// identically.
	ReadDir(ctx context.Context, dir string) ([]Entry, error)

	// Open returns the contents of a file for reading. The caller closes it.
	Open(ctx context.Context, path string) (io.ReadCloser, error)

	// Exists reports whether path exists.
	Exists(ctx context.Context, path string) (bool, error)

	// MkdirAll creates dir and any missing parents.
	MkdirAll(ctx context.Context, dir string) error

	// Rename moves src to dst. It must fail with ErrExist rather than
	// overwrite, and should fall back to copy+delete when src and dst live on
	// different filesystems (common when a library spans mounts).
	Rename(ctx context.Context, src, dst string) error

	// Describe returns a short human-readable description of the backend,
	// used in diagnostics: "local filesystem", "ssh://user@host".
	Describe() string

	// Close releases any resources (SSH connections). Safe to call on a
	// backend that holds none.
	Close() error
}
