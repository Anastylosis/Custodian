package vfs

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Wasylq/StashJanitor/internal/pathmap"
)

// Mem is an in-memory FS used by tests. It keeps the sidecar move logic
// testable without a real filesystem, and lets tests assert exactly which
// renames were attempted and in what order.
type Mem struct {
	// Files is the set of existing paths.
	Files map[string]bool
	// Renames records every successful rename as "src -> dst", in order.
	Renames []string
	// FailRename, when set, makes Rename return this error for a matching
	// source path — used to exercise the failure-reporting path.
	FailRename map[string]error
	// Contents optionally holds file bodies, for tests that read files.
	Contents map[string]string
}

// NewMem builds a Mem pre-populated with the given paths.
func NewMem(paths ...string) *Mem {
	m := &Mem{Files: make(map[string]bool, len(paths)), FailRename: map[string]error{}, Contents: map[string]string{}}
	for _, p := range paths {
		m.Files[p] = true
	}
	return m
}

func (m *Mem) Describe() string { return "in-memory (test)" }
func (m *Mem) Close() error     { return nil }

func (m *Mem) ReadDir(_ context.Context, dir string) ([]Entry, error) {
	seen := map[string]Entry{}
	for p, ok := range m.Files {
		if !ok {
			continue
		}
		if pathmap.Dir(p) == dir {
			seen[pathmap.Base(p)] = Entry{Name: pathmap.Base(p), Size: int64(len(m.Contents[p]))}
			continue
		}
		// Surface intermediate directories so a recursive walk works.
		if strings.HasPrefix(p, dir) && len(p) > len(dir) {
			rest := strings.TrimLeft(p[len(dir):], `/\`)
			if i := strings.IndexAny(rest, `/\`); i > 0 {
				name := rest[:i]
				seen[name] = Entry{Name: name, IsDir: true}
			}
		}
	}
	out := make([]Entry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Open returns the recorded contents of a file, or empty content when the file
// exists but no contents were supplied.
func (m *Mem) Open(_ context.Context, p string) (io.ReadCloser, error) {
	if !m.Files[p] {
		return nil, fmt.Errorf("%w: %s", ErrNotExist, p)
	}
	return io.NopCloser(strings.NewReader(m.Contents[p])), nil
}

func (m *Mem) Exists(_ context.Context, path string) (bool, error) {
	return m.Files[path], nil
}

func (m *Mem) MkdirAll(_ context.Context, _ string) error { return nil }

func (m *Mem) Rename(_ context.Context, src, dst string) error {
	if err, ok := m.FailRename[src]; ok {
		return err
	}
	if !m.Files[src] {
		return fmt.Errorf("%w: %s", ErrNotExist, src)
	}
	if m.Files[dst] {
		return fmt.Errorf("%w: %s", ErrExist, dst)
	}
	delete(m.Files, src)
	m.Files[dst] = true
	// Contents must follow the file, or the double silently loses data in a
	// way no real filesystem would.
	if body, ok := m.Contents[src]; ok {
		m.Contents[dst] = body
		delete(m.Contents, src)
	}
	m.Renames = append(m.Renames, src+" -> "+dst)
	return nil
}

// Sorted returns the current file set, sorted — convenient for assertions.
func (m *Mem) Sorted() []string {
	out := make([]string, 0, len(m.Files))
	for p := range m.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// String renders the file set for test failure messages.
func (m *Mem) String() string { return strings.Join(m.Sorted(), "\n") }

type memWriter struct {
	m   *Mem
	p   string
	buf strings.Builder
}

func (w *memWriter) Write(b []byte) (int, error) { return w.buf.Write(b) }
func (w *memWriter) Close() error {
	w.m.Files[w.p] = true
	w.m.Contents[w.p] = w.buf.String()
	return nil
}

func (m *Mem) Create(_ context.Context, p string) (io.WriteCloser, error) {
	return &memWriter{m: m, p: p}, nil
}

func (m *Mem) Remove(_ context.Context, p string) error {
	delete(m.Files, p)
	delete(m.Contents, p)
	return nil
}
