package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHConfig describes how to reach the machine holding the library.
//
// This backend exists for the common case where Stash and its storage live on
// a NAS while stash-janitor runs on a workstation that has no mount. It speaks
// SFTP rather than shelling out to ssh, which matters for a media library:
// filenames routinely contain spaces, '+', '(', "'" and '&', all of which are
// hazards when interpolated into a remote shell command.
type SSHConfig struct {
	Host string
	Port int
	User string

	// KeyFile is an optional private key path. When empty, the agent is tried
	// first, then the usual default key locations.
	KeyFile string
	// Passphrase decrypts KeyFile when it is encrypted.
	Passphrase string
	// Password enables password auth. Prefer keys or the agent.
	Password string

	// KnownHostsFile defaults to ~/.ssh/known_hosts.
	KnownHostsFile string
	// AcceptNewHostKey trusts (and records) an unknown host on first contact,
	// mirroring ssh's StrictHostKeyChecking=accept-new. An already-known host
	// whose key CHANGED is always rejected, regardless of this setting.
	AcceptNewHostKey bool

	// Timeout for the initial connection. Defaults to 15s.
	Timeout time.Duration
}

// SSH is an FS backed by an SFTP session.
type SSH struct {
	client *sftp.Client
	conn   *ssh.Client
	label  string
}

func expandHome(p string) string {
	if p == "" || !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return path.Join(home, strings.TrimPrefix(p, "~"))
}

// authMethods assembles the auth attempts in the order a user would expect:
// explicit key, then agent, then default key locations, then password.
func authMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	addKey := func(p string) {
		buf, err := os.ReadFile(p)
		if err != nil {
			return
		}
		var signer ssh.Signer
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(buf, []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(buf)
		}
		if err != nil {
			return
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.KeyFile != "" {
		p := expandHome(cfg.KeyFile)
		buf, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read key %q: %w", p, err)
		}
		var signer ssh.Signer
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(buf, []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(buf)
		}
		if err != nil {
			return nil, fmt.Errorf("parse key %q (encrypted keys need sidecars.ssh.passphrase): %w", p, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if conn, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
		}
	}

	if cfg.KeyFile == "" {
		if home, err := os.UserHomeDir(); err == nil {
			for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
				addKey(path.Join(home, ".ssh", name))
			}
		}
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range qs {
					ans[i] = cfg.Password
				}
				return ans, nil
			}))
	}

	if len(methods) == 0 {
		return nil, errors.New("no usable SSH credentials: set sidecars.ssh.key_file, run ssh-agent, or set a password")
	}
	return methods, nil
}

// hostKeyCallback verifies the server against known_hosts.
func hostKeyCallback(cfg SSHConfig) (ssh.HostKeyCallback, error) {
	khPath := expandHome(cfg.KnownHostsFile)
	if khPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("locate known_hosts: %w", err)
		}
		khPath = path.Join(home, ".ssh", "known_hosts")
	}

	if _, err := os.Stat(khPath); errors.Is(err, os.ErrNotExist) {
		if !cfg.AcceptNewHostKey {
			return nil, fmt.Errorf("%s does not exist; connect once with ssh, or set sidecars.ssh.accept_new_host_key: true", khPath)
		}
		if f, err := os.OpenFile(khPath, os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.Close()
		}
	}

	verify, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", khPath, err)
	}
	if !cfg.AcceptNewHostKey {
		return verify, nil
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		// A changed key on a KNOWN host is never acceptable — that is what a
		// man-in-the-middle looks like. Only a genuinely unknown host is
		// trusted on first contact.
		var ke *knownhosts.KeyError
		if errors.As(err, &ke) && len(ke.Want) == 0 {
			return appendKnownHost(khPath, hostname, key)
		}
		return err
	}, nil
}

func appendKnownHost(khPath, hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(khPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("record new host key: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(knownhosts.Line([]string{hostname}, key) + "\n")
	return err
}

// NewSSH dials the host and opens an SFTP session.
func NewSSH(ctx context.Context, cfg SSHConfig) (*SSH, error) {
	if cfg.Host == "" {
		return nil, errors.New("sidecars.ssh.host is required")
	}
	if cfg.User == "" {
		return nil, errors.New("sidecars.ssh.user is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}

	auths, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hk, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	d := net.Dialer{Timeout: cfg.Timeout}
	rawConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(rawConn, addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		HostKeyCallback: hk,
		Timeout:         cfg.Timeout,
	})
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	conn := ssh.NewClient(c, chans, reqs)

	sc, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("start sftp on %s (is the sftp subsystem enabled?): %w", addr, err)
	}
	return &SSH{client: sc, conn: conn, label: fmt.Sprintf("ssh://%s@%s", cfg.User, addr)}, nil
}

func (s *SSH) Describe() string { return s.label }

func (s *SSH) Close() error {
	var first error
	if s.client != nil {
		first = s.client.Close()
	}
	if s.conn != nil {
		if err := s.conn.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *SSH) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := s.client.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %q: %w", dir, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{Name: e.Name(), IsDir: e.IsDir(), Size: e.Size()})
	}
	return out, nil
}

func (s *SSH) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.client.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", p, err)
	}
	return f, nil
}

func (s *SSH) Exists(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, err := s.client.Lstat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %q: %w", p, err)
}

func (s *SSH) MkdirAll(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.client.MkdirAll(dir); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}
	return nil
}

func (s *SSH) Rename(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ok, err := s.Exists(ctx, src); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotExist, src)
	}
	if ok, err := s.Exists(ctx, dst); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExist, dst)
	}
	if err := s.client.MkdirAll(path.Dir(dst)); err != nil {
		return fmt.Errorf("mkdir for %q: %w", dst, err)
	}
	if err := s.client.Rename(src, dst); err == nil {
		return nil
	}
	// A NAS library often spans several mounts, where the server refuses a
	// rename across them. Fall back to streaming the file over.
	return s.copyThenRemove(src, dst)
}

func (s *SSH) copyThenRemove(src, dst string) error {
	in, err := s.client.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer in.Close()

	tmp := dst + ".sj-partial"
	out, err := s.client.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("create %q: %w", tmp, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		s.client.Remove(tmp)
		return fmt.Errorf("copy %q → %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		s.client.Remove(tmp)
		return fmt.Errorf("close %q: %w", tmp, err)
	}
	if err := s.client.Rename(tmp, dst); err != nil {
		s.client.Remove(tmp)
		return fmt.Errorf("finalise %q: %w", dst, err)
	}
	if err := s.client.Remove(src); err != nil {
		return fmt.Errorf("copied to %q but could not remove original %q: %w", dst, src, err)
	}
	return nil
}

func (s *SSH) Create(ctx context.Context, p string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.client.MkdirAll(path.Dir(p)); err != nil {
		return nil, fmt.Errorf("mkdir for %q: %w", p, err)
	}
	f, err := s.client.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return nil, fmt.Errorf("create %q: %w", p, err)
	}
	return f, nil
}

func (s *SSH) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.client.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %q: %w", p, err)
	}
	return nil
}
