package vfs

import (
	"context"
	"errors"
	"os"
	"path"
	"strconv"
	"testing"
	"time"
)

// Integration tests here talk to a real SSH/SFTP host and only run when
// SJ_INT_SSH_HOST is set, so `go test ./...` stays hermetic.
//
// They write only inside a freshly created temporary directory under
// SJ_INT_SSH_DIR (default /tmp) and remove it afterwards.
//
//	SJ_INT_SSH_HOST=nas.local \
//	SJ_INT_SSH_USER=me \
//	SJ_INT_SSH_PASSWORD_ENV=MY_PW \
//	  go test ./internal/vfs/... -run Integration -v
func integrationSSH(t *testing.T) (*SSH, string) {
	t.Helper()
	host := os.Getenv("SJ_INT_SSH_HOST")
	if host == "" {
		t.Skip("SJ_INT_SSH_HOST not set; skipping SSH integration test")
	}
	pw := ""
	if env := os.Getenv("SJ_INT_SSH_PASSWORD_ENV"); env != "" {
		pw = os.Getenv(env)
	}
	port := 22
	if p := os.Getenv("SJ_INT_SSH_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	fs, err := NewSSH(context.Background(), SSHConfig{
		Host:             host,
		Port:             port,
		User:             os.Getenv("SJ_INT_SSH_USER"),
		Password:         pw,
		AcceptNewHostKey: true,
		Timeout:          20 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	base := os.Getenv("SJ_INT_SSH_DIR")
	if base == "" {
		base = "/tmp"
	}
	dir := path.Join(base, "sj-sidecar-it-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := fs.MkdirAll(context.Background(), dir); err != nil {
		fs.Close()
		t.Fatalf("MkdirAll %q: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = fs.client.RemoveDirectory(dir)
		fs.Close()
	})
	return fs, dir
}

// write creates a remote file with the given contents.
func write(t *testing.T, fs *SSH, p, contents string) {
	t.Helper()
	f, err := fs.client.Create(p)
	if err != nil {
		t.Fatalf("create %q: %v", p, err)
	}
	if _, err := f.Write([]byte(contents)); err != nil {
		f.Close()
		t.Fatalf("write %q: %v", p, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %q: %v", p, err)
	}
	t.Cleanup(func() { _ = fs.client.Remove(p) })
}

func TestIntegrationSSHRenameRoundTrip(t *testing.T) {
	fs, dir := integrationSSH(t)
	ctx := context.Background()

	// Deliberately awful name: the characters that make shelling out to `mv`
	// unsafe, and that this library's real filenames actually contain.
	src := path.Join(dir, "StudioHandleXXX+-+Free Use (2024)'s & more.en.srt")
	dst := path.Join(dir, "2024-01-01_Renamed.Clip_1080p.en.srt")
	write(t, fs, src, "1\n00:00:01,000 --> 00:00:02,000\nhello\n")

	if ok, err := fs.Exists(ctx, src); err != nil || !ok {
		t.Fatalf("Exists(src) = (%v, %v), want (true, nil)", ok, err)
	}
	if err := fs.Rename(ctx, src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	t.Cleanup(func() { _ = fs.client.Remove(dst) })

	if ok, _ := fs.Exists(ctx, dst); !ok {
		t.Error("destination missing after rename")
	}
	if ok, _ := fs.Exists(ctx, src); ok {
		t.Error("source still present after rename")
	}
}

func TestIntegrationSSHNeverOverwrites(t *testing.T) {
	fs, dir := integrationSSH(t)
	ctx := context.Background()

	src := path.Join(dir, "a.srt")
	dst := path.Join(dir, "b.srt")
	write(t, fs, src, "source")
	write(t, fs, dst, "PRECIOUS")

	err := fs.Rename(ctx, src, dst)
	if !errors.Is(err, ErrExist) {
		t.Fatalf("Rename over an existing file = %v, want ErrExist", err)
	}
	// The occupied destination must be untouched.
	f, err := fs.client.Open(dst)
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, _ := f.Read(buf)
	if got := string(buf[:n]); got != "PRECIOUS" {
		t.Errorf("destination was modified: %q", got)
	}
}

func TestIntegrationSSHListFindsSidecars(t *testing.T) {
	fs, dir := integrationSSH(t)
	ctx := context.Background()

	write(t, fs, path.Join(dir, "clip_1080p.mp4"), "video")
	write(t, fs, path.Join(dir, "clip_1080p.en.srt"), "en")
	write(t, fs, path.Join(dir, "clip_1080p.es.srt"), "es")

	entries, err := fs.ReadDir(ctx, dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	found := map[string]bool{}
	var names []string
	for _, e := range entries {
		found[e.Name] = true
		names = append(names, e.Name)
	}
	for _, want := range []string{"clip_1080p.mp4", "clip_1080p.en.srt", "clip_1080p.es.srt"} {
		if !found[want] {
			t.Errorf("List missing %q (got %v)", want, names)
		}
	}
}

// Listing a directory that does not exist is "no sidecars", not an error.
func TestIntegrationSSHListMissingDirIsEmpty(t *testing.T) {
	fs, dir := integrationSSH(t)
	entries, err := fs.ReadDir(context.Background(), path.Join(dir, "nope"))
	if err != nil {
		t.Fatalf("ReadDir(missing) error = %v, want nil", err)
	}
	if len(entries) != 0 {
		t.Errorf("ReadDir(missing) = %v, want empty", entries)
	}
}
