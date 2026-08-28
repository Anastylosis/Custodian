package vfs

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// genKey writes an ed25519 keypair (optionally passphrase-protected) to
// dir/name and dir/name.pub, and returns the private key path.
func genKey(t *testing.T, dir, name, passphrase string) string {
	t.Helper()
	priv := filepath.Join(dir, name)
	args := []string{"-t", "ed25519", "-N", passphrase, "-f", priv, "-q", "-C", ""}
	cmd := exec.Command("ssh-keygen", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	return priv
}

func pubKey(t *testing.T, privPath string) ssh.PublicKey {
	t.Helper()
	buf, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	k, _, _, _, err := ssh.ParseAuthorizedKey(buf)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := []struct{ in, want string }{
		{"", ""},
		{"/abs/path", "/abs/path"},
		{"relative", "relative"},
		{"~", home},
		{"~/keys/id_ed25519", filepath.Join(home, "keys", "id_ed25519")},
	}
	for _, tc := range tests {
		if got := expandHome(tc.in); got != tc.want {
			t.Errorf("expandHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandHome_NoHomeDirReturnsInputUnchanged(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	const in = "~/id_ed25519"
	if got := expandHome(in); got != in {
		t.Errorf("expandHome(%q) = %q, want unchanged when home cannot be located", in, got)
	}
}

func TestAuthMethods_NoCredentialsErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	if _, err := authMethods(SSHConfig{}); err == nil {
		t.Fatal("expected an error when no credentials are configured")
	}
}

func TestAuthMethods_ExplicitKeyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, home, "id_ed25519", "")

	methods, err := authMethods(SSHConfig{KeyFile: key})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("methods = %d, want 1", len(methods))
	}
}

func TestAuthMethods_ExplicitKeyFileWithTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	genKey(t, home, "id_ed25519", "")

	methods, err := authMethods(SSHConfig{KeyFile: "~/id_ed25519"})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("methods = %d, want 1", len(methods))
	}
}

func TestAuthMethods_ExplicitKeyFileMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	if _, err := authMethods(SSHConfig{KeyFile: filepath.Join(home, "absent")}); err == nil {
		t.Fatal("expected an error for a missing key file")
	}
}

func TestAuthMethods_ExplicitKeyFileWrongPassphrase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, home, "id_ed25519", "correct-horse")

	if _, err := authMethods(SSHConfig{KeyFile: key, Passphrase: "wrong"}); err == nil {
		t.Fatal("expected an error for a wrong passphrase")
	}
}

func TestAuthMethods_ExplicitKeyFileCorrectPassphrase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, home, "id_ed25519", "correct-horse")

	methods, err := authMethods(SSHConfig{KeyFile: key, Passphrase: "correct-horse"})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("methods = %d, want 1", len(methods))
	}
}

func TestAuthMethods_ExplicitKeyFileNotAKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	bogus := filepath.Join(home, "bogus")
	if err := os.WriteFile(bogus, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := authMethods(SSHConfig{KeyFile: bogus}); err == nil {
		t.Fatal("expected an error parsing a non-key file")
	}
}

func TestAuthMethods_DefaultKeyLocations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	genKey(t, filepath.Join(home, ".ssh"), "id_rsa", "")

	methods, err := authMethods(SSHConfig{})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("methods = %d, want 1 from the default ~/.ssh location", len(methods))
	}
}

func TestAuthMethods_Password(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	methods, err := authMethods(SSHConfig{Password: "hunter2"})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 2 {
		t.Errorf("methods = %d, want 2 (password + keyboard-interactive)", len(methods))
	}
}

func TestAuthMethods_SSHAgentSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	t.Setenv("SSH_AUTH_SOCK", sock)

	methods, err := authMethods(SSHConfig{})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("methods = %d, want 1 (the agent)", len(methods))
	}
}

func TestAuthMethods_DefaultKeyLocationsSkipsUnusableKeysButUsesPassphrase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A default-location key encrypted with the configured passphrase must
	// still be picked up ...
	genKey(t, sshDir, "id_ed25519", "hunter2")
	// ... while one that fails to parse at all is silently skipped rather
	// than aborting the whole scan.
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	methods, err := authMethods(SSHConfig{Passphrase: "hunter2"})
	if err != nil {
		t.Fatalf("authMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Errorf("methods = %d, want 1 (only id_ed25519 decrypts)", len(methods))
	}
}

func writeKnownHosts(t *testing.T, path, hostname string, key ssh.PublicKey) {
	t.Helper()
	line := knownhosts.Line([]string{hostname}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostKeyCallback_MissingFileWithoutAcceptNewErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := hostKeyCallback(SSHConfig{KnownHostsFile: filepath.Join(dir, "known_hosts")})
	if err == nil {
		t.Fatal("expected an error when known_hosts is missing and AcceptNewHostKey is false")
	}
}

func TestHostKeyCallback_CannotLocateHomeDir(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := hostKeyCallback(SSHConfig{}); err == nil {
		t.Fatal("expected an error when KnownHostsFile is empty and the home dir cannot be located")
	}
}

func TestHostKeyCallback_InvalidKnownHostsContents(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(kh, []byte("example.com ssh-rsa not-valid-base64!!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hostKeyCallback(SSHConfig{KnownHostsFile: kh}); err == nil {
		t.Fatal("expected an error parsing a malformed known_hosts file")
	}
}

func TestHostKeyCallback_AcceptNewOnAnAlreadyKnownMatchingHostSkipsRecording(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	key := genKey(t, dir, "hostkey", "")
	pk := pubKey(t, key)
	writeKnownHosts(t, kh, "example.com:22", pk)
	before, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}

	cb, err := hostKeyCallback(SSHConfig{KnownHostsFile: kh, AcceptNewHostKey: true})
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := cb("example.com:22", dummyAddr{}, pk); err != nil {
		t.Errorf("callback for an already-known matching host = %v, want nil", err)
	}
	after, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("known_hosts was rewritten even though the host was already known and matched")
	}
}

func TestHostKeyCallback_MissingFileWithAcceptNewCreatesIt(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")

	cb, err := hostKeyCallback(SSHConfig{KnownHostsFile: kh, AcceptNewHostKey: true})
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if cb == nil {
		t.Fatal("callback is nil")
	}
	if _, err := os.Stat(kh); err != nil {
		t.Errorf("known_hosts was not created: %v", err)
	}
}

func TestHostKeyCallback_DefaultsToHomeSSHDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := genKey(t, home, "hostkey", "")
	pk := pubKey(t, key)
	writeKnownHosts(t, filepath.Join(home, ".ssh", "known_hosts"), "example.com:22", pk)

	cb, err := hostKeyCallback(SSHConfig{})
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := cb("example.com:22", dummyAddr{}, pk); err != nil {
		t.Errorf("callback for a known, matching host key = %v, want nil", err)
	}
}

func TestHostKeyCallback_KnownHostRejectsWithoutTrustOnFirstUse(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	key := genKey(t, dir, "hostkey", "")
	pk := pubKey(t, key)
	writeKnownHosts(t, kh, "example.com:22", pk)

	cb, err := hostKeyCallback(SSHConfig{KnownHostsFile: kh})
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := cb("example.com:22", dummyAddr{}, pk); err != nil {
		t.Errorf("callback for a known, matching host key = %v, want nil", err)
	}

	other := genKey(t, dir, "otherhost", "")
	if err := cb("unknown.example.com:22", dummyAddr{}, pubKey(t, other)); err == nil {
		t.Error("an unknown host must be rejected when AcceptNewHostKey is false")
	}
}

func TestHostKeyCallback_AcceptNewTrustsAnUnknownHostAndRecordsIt(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	known := genKey(t, dir, "known", "")
	writeKnownHosts(t, kh, "known.example.com:22", pubKey(t, known))

	cb, err := hostKeyCallback(SSHConfig{KnownHostsFile: kh, AcceptNewHostKey: true})
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}

	newKey := genKey(t, dir, "new", "")
	if err := cb("new.example.com:22", dummyAddr{}, pubKey(t, newKey)); err != nil {
		t.Fatalf("callback for a new host with AcceptNewHostKey = %v, want nil", err)
	}
	got, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "new.example.com") {
		t.Error("the new host key was not recorded in known_hosts")
	}
}

func TestHostKeyCallback_AcceptNewStillRejectsAChangedKey(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	original := genKey(t, dir, "original", "")
	writeKnownHosts(t, kh, "example.com:22", pubKey(t, original))

	cb, err := hostKeyCallback(SSHConfig{KnownHostsFile: kh, AcceptNewHostKey: true})
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}

	changed := genKey(t, dir, "changed", "")
	if err := cb("example.com:22", dummyAddr{}, pubKey(t, changed)); err == nil {
		t.Error("a changed key on a known host must be rejected even with AcceptNewHostKey")
	}
}

func TestAppendKnownHost(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	key := genKey(t, dir, "id", "")

	if err := appendKnownHost(kh, "example.com:22", pubKey(t, key)); err != nil {
		t.Fatalf("appendKnownHost: %v", err)
	}
	got, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "example.com") {
		t.Errorf("known_hosts content = %q, missing the host", got)
	}
}

func TestAppendKnownHost_UnwritablePathErrors(t *testing.T) {
	dir := t.TempDir()
	key := genKey(t, dir, "id", "")
	// A directory cannot be opened for writing as a file.
	if err := appendKnownHost(dir, "example.com:22", pubKey(t, key)); err == nil {
		t.Fatal("expected an error appending to a path that is a directory")
	}
}

func TestNewSSH_RequiresHostAndUser(t *testing.T) {
	if _, err := NewSSH(context.Background(), SSHConfig{}); err == nil {
		t.Error("NewSSH with no host should error")
	}
	if _, err := NewSSH(context.Background(), SSHConfig{Host: "example.com"}); err == nil {
		t.Error("NewSSH with no user should error")
	}
}

func TestNewSSH_PropagatesAuthMethodsFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := NewSSH(context.Background(), SSHConfig{Host: "example.com", User: "me"})
	if err == nil {
		t.Fatal("expected an error when no SSH credentials are usable")
	}
}

func TestNewSSH_PropagatesHostKeyCallbackFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	key := genKey(t, home, "id_ed25519", "")

	_, err := NewSSH(context.Background(), SSHConfig{
		Host:           "example.com",
		User:           "me",
		KeyFile:        key,
		KnownHostsFile: filepath.Join(home, "missing_known_hosts"),
	})
	if err == nil {
		t.Fatal("expected an error when known_hosts is missing and AcceptNewHostKey is false")
	}
}

// dummyAddr satisfies net.Addr for callback tests that never touch the
// network.
type dummyAddr struct{}

func (dummyAddr) Network() string { return "tcp" }
func (dummyAddr) String() string  { return "127.0.0.1:22" }

// newSFTPTestClient wires a real *sftp.Client to a real in-process SFTP
// server (backed by sftp.InMemHandler, an in-memory filesystem) over a
// net.Pipe — the SFTP wire protocol end to end, but with no network socket
// and no external sshd. This is what lets SSH's wire-level methods
// (ReadDir/Open/Exists/MkdirAll/Rename/Create/Remove/copyThenRemove) be
// exercised without the live SSH server ssh_integration_test.go requires.
//
// net.Pipe rather than a pair of io.Pipes: closing either end of a net.Pipe
// unblocks a pending Read/Write on both ends immediately, so client and
// server can be closed in either order — including a manual s.Close() mid-test
// followed by cleanup's second close. Two io.Pipes only unblock the reader
// when THAT side's writer closes, so closing the client before the server
// (which is still blocked reading) deadlocks both Close calls.
func newSFTPTestClient(t *testing.T) *sftp.Client {
	t.Helper()
	serverConn, clientConn := net.Pipe()

	server := sftp.NewRequestServer(serverConn, sftp.InMemHandler())
	go func() { _ = server.Serve() }()

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = server.Close() })
	return client
}

func newTestSSH(t *testing.T) *SSH {
	return &SSH{client: newSFTPTestClient(t), label: "ssh://test@example.com"}
}

func TestSSH_Describe(t *testing.T) {
	s := &SSH{label: "ssh://me@nas:22"}
	if got, want := s.Describe(), "ssh://me@nas:22"; got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
}

func TestSSH_CloseWithNothingToClose(t *testing.T) {
	s := &SSH{}
	if err := s.Close(); err != nil {
		t.Errorf("Close() on a zero-value SSH = %v, want nil", err)
	}
}

func TestSSH_Close(t *testing.T) {
	s := newTestSSH(t)
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Cleanup will close the client a second time; sftp.Client.Close
	// tolerates that.
}

func TestSSH_HonoursACancelledContext(t *testing.T) {
	s := newTestSSH(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.ReadDir(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadDir = %v, want context.Canceled", err)
	}
	if _, err := s.Open(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Open = %v, want context.Canceled", err)
	}
	if _, err := s.Exists(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Exists = %v, want context.Canceled", err)
	}
	if err := s.MkdirAll(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("MkdirAll = %v, want context.Canceled", err)
	}
	if err := s.Rename(ctx, "/a", "/b"); !errors.Is(err, context.Canceled) {
		t.Errorf("Rename = %v, want context.Canceled", err)
	}
	if _, err := s.Create(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Create = %v, want context.Canceled", err)
	}
	if err := s.Remove(ctx, "/a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Remove = %v, want context.Canceled", err)
	}
}

func TestSSH_MkdirAllAndReadDir(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()

	if err := s.MkdirAll(ctx, "/lib/Show"); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	w, err := s.Create(ctx, "/lib/Show/ep01.srt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := io.WriteString(w, "subtitle body"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := s.ReadDir(ctx, "/lib/Show")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "ep01.srt" || entries[0].Size != int64(len("subtitle body")) {
		t.Errorf("ReadDir = %+v, want one 13-byte ep01.srt", entries)
	}
}

// A missing directory is emptiness, not an error — mirrors Local's contract.
func TestSSH_ReadDirTreatsAMissingDirectoryAsEmpty(t *testing.T) {
	s := newTestSSH(t)
	entries, err := s.ReadDir(context.Background(), "/nope")
	if err != nil {
		t.Fatalf("ReadDir of a missing directory errored: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want none", entries)
	}
}

func TestSSH_OpenReadsWhatWasWritten(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()
	w, err := s.Create(ctx, "/a.srt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := io.WriteString(w, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := s.Open(ctx, "/a.srt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("read %q, want hello", got)
	}
}

func TestSSH_OpenMissingFileErrors(t *testing.T) {
	s := newTestSSH(t)
	if _, err := s.Open(context.Background(), "/nope.srt"); err == nil {
		t.Fatal("opening a missing file returned no error")
	}
}

func TestSSH_Exists(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()
	w, err := s.Create(ctx, "/there")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	if ok, err := s.Exists(ctx, "/there"); err != nil || !ok {
		t.Errorf("Exists(there) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := s.Exists(ctx, "/absent"); err != nil || ok {
		t.Errorf("Exists(absent) = %v, %v; want false, nil", ok, err)
	}
}

func TestSSH_RenameMovesTheFile(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()
	w, err := s.Create(ctx, "/loose/x.srt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "body"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	// Rename into a directory that does not exist yet — Rename must create it.
	if err := s.Rename(ctx, "/loose/x.srt", "/library/Show/x.srt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if ok, _ := s.Exists(ctx, "/loose/x.srt"); ok {
		t.Error("source still exists after a move")
	}
	if ok, _ := s.Exists(ctx, "/library/Show/x.srt"); !ok {
		t.Error("destination missing after a move")
	}
}

func TestSSH_RenameMissingSourceErrors(t *testing.T) {
	s := newTestSSH(t)
	err := s.Rename(context.Background(), "/nope.srt", "/dst.srt")
	if !errors.Is(err, ErrNotExist) {
		t.Fatalf("error = %v, want ErrNotExist", err)
	}
}

func TestSSH_RenameNeverOverwrites(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()
	for _, p := range []string{"/new.srt", "/existing.srt"} {
		w, err := s.Create(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Close()
	}

	err := s.Rename(ctx, "/new.srt", "/existing.srt")
	if !errors.Is(err, ErrExist) {
		t.Fatalf("error = %v, want ErrExist", err)
	}
}

func TestSSH_RemoveMissingFileIsNotAnError(t *testing.T) {
	s := newTestSSH(t)
	if err := s.Remove(context.Background(), "/nope"); err != nil {
		t.Errorf("Remove of a missing file = %v, want nil", err)
	}
}

func TestSSH_RemoveDeletesTheFile(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()
	w, err := s.Create(ctx, "/gone.srt")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	if err := s.Remove(ctx, "/gone.srt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if ok, _ := s.Exists(ctx, "/gone.srt"); ok {
		t.Error("file still exists after Remove")
	}
}

// copyThenRemove is SSH's cross-mount fallback, exercised directly the same
// way Local's is: it is unreachable through Rename against InMemHandler,
// which never refuses a same-server rename.
func TestSSH_CopyThenRemove(t *testing.T) {
	s := newTestSSH(t)
	ctx := context.Background()
	w, err := s.Create(ctx, "/src.srt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "streamed across"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if err := s.MkdirAll(ctx, "/dst"); err != nil {
		t.Fatal(err)
	}

	if err := s.copyThenRemove("/src.srt", "/dst/out.srt"); err != nil {
		t.Fatalf("copyThenRemove: %v", err)
	}
	if ok, _ := s.Exists(ctx, "/src.srt"); ok {
		t.Error("source survived a move")
	}
	r, err := s.Open(ctx, "/dst/out.srt")
	if err != nil {
		t.Fatalf("Open dst: %v", err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if string(got) != "streamed across" {
		t.Errorf("content did not survive the copy: %q", got)
	}
	// No leftover partial file at the real path.
	if ok, _ := s.Exists(ctx, "/dst/out.srt.sj-partial"); ok {
		t.Error("temporary file left behind")
	}
}

func TestSSH_CopyThenRemoveOpenSourceFails(t *testing.T) {
	s := newTestSSH(t)
	if err := s.copyThenRemove("/nope.srt", "/dst.srt"); err == nil {
		t.Fatal("expected an error when the source cannot be opened")
	}
}
