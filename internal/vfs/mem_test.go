package vfs

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestMemDescribeAndClose(t *testing.T) {
	m := NewMem()
	if m.Describe() == "" {
		t.Error("Describe is empty")
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestMemReadDir(t *testing.T) {
	m := NewMem("/lib/Show/a.srt", "/lib/Show/Sub/b.srt", "/lib/other.srt")
	m.Contents["/lib/Show/a.srt"] = "hello"

	got, err := m.ReadDir(context.Background(), "/lib/Show")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	byName := map[string]Entry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if f := byName["a.srt"]; f.IsDir || f.Size != 5 {
		t.Errorf("a.srt = %+v, want a 5-byte file", f)
	}
	if d := byName["Sub"]; !d.IsDir {
		t.Errorf("Sub = %+v, want a surfaced intermediate directory", d)
	}
	if _, ok := byName["other.srt"]; ok {
		t.Error("ReadDir returned an entry outside the requested directory")
	}
}

func TestMemReadDir_SkipsTombstonedEntries(t *testing.T) {
	m := NewMem("/lib/a.srt")
	m.Files["/lib/removed.srt"] = false

	got, err := m.ReadDir(context.Background(), "/lib")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range got {
		if e.Name == "removed.srt" {
			t.Error("ReadDir surfaced a tombstoned (false) entry")
		}
	}
}

func TestMemReadDir_Empty(t *testing.T) {
	m := NewMem()
	got, err := m.ReadDir(context.Background(), "/nope")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("entries = %+v, want none", got)
	}
}

func TestMemOpen(t *testing.T) {
	m := NewMem("/a.srt")
	m.Contents["/a.srt"] = "body"

	r, err := m.Open(context.Background(), "/a.srt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, _ := io.ReadAll(r)
	if string(got) != "body" {
		t.Errorf("read %q, want body", got)
	}
}

func TestMemOpen_MissingFile(t *testing.T) {
	m := NewMem()
	if _, err := m.Open(context.Background(), "/nope.srt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Open(missing) error = %v, want ErrNotExist", err)
	}
}

func TestMemRename_FailRenameInjectsAnError(t *testing.T) {
	m := NewMem("/a.srt")
	injected := errors.New("injected failure")
	m.FailRename["/a.srt"] = injected

	if err := m.Rename(context.Background(), "/a.srt", "/b.srt"); !errors.Is(err, injected) {
		t.Fatalf("Rename error = %v, want %v", err, injected)
	}
	if !m.Files["/a.srt"] {
		t.Error("source should be untouched when Rename is forced to fail")
	}
}

func TestMemRename_MissingSourceErrors(t *testing.T) {
	m := NewMem()
	if err := m.Rename(context.Background(), "/nope.srt", "/b.srt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("Rename of a missing source = %v, want ErrNotExist", err)
	}
}

func TestMemRename_NeverOverwrites(t *testing.T) {
	m := NewMem("/a.srt", "/b.srt")
	if err := m.Rename(context.Background(), "/a.srt", "/b.srt"); !errors.Is(err, ErrExist) {
		t.Fatalf("Rename onto an existing file = %v, want ErrExist", err)
	}
}

func TestMemSortedAndString(t *testing.T) {
	m := NewMem("/b.srt", "/a.srt")
	got := m.Sorted()
	want := []string{"/a.srt", "/b.srt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Sorted() = %v, want %v", got, want)
	}
	if m.String() != "/a.srt\n/b.srt" {
		t.Errorf("String() = %q", m.String())
	}
}
