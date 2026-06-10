package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshTransport is the real SSH implementation of Transport. File moves use the
// shell (cat > path / cat path) to avoid a hard SFTP dependency — lean by
// default. Credentials come from a private key referenced in the target spec;
// secrets are never stored locally by Rollops (the SecretProvider supplies the
// key material at execution time).
type sshTransport struct {
	client *ssh.Client
}

func dialSSH(s spec) (Transport, error) {
	host := s.str("host")
	port := s.str("port")
	if port == "" {
		port = "22"
	}
	user := s.str("user")
	if user == "" {
		user = "deploy"
	}

	auth, err := authMethods(s)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(s),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(host, port)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh: dial %s: %w", addr, err)
	}
	return &sshTransport{client: client}, nil
}

func authMethods(s spec) ([]ssh.AuthMethod, error) {
	keyPath := s.str("privateKeyPath")
	if keyPath == "" {
		return nil, fmt.Errorf("ssh: spec.privateKeyPath is required (key supplied via SecretProvider)")
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh: read key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("ssh: parse key: %w", err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

func hostKeyCallback(s spec) ssh.HostKeyCallback {
	// A pinned host key in the spec is the secure path; an explicit
	// insecureSkipHostKeyCheck opt-in exists for throwaway/dev hosts only.
	if fp := s.str("hostKey"); fp != "" {
		if pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(fp)); err == nil {
			return ssh.FixedHostKey(pk)
		}
	}
	if b, _ := s["insecureSkipHostKeyCheck"].(bool); b {
		return ssh.InsecureIgnoreHostKey()
	}
	// Default: refuse unknown hosts rather than trust on first use.
	return func(string, net.Addr, ssh.PublicKey) error {
		return fmt.Errorf("ssh: no pinned host key (set spec.hostKey, or spec.insecureSkipHostKeyCheck for dev)")
	}
}

func (t *sshTransport) Run(ctx context.Context, cmd string) (int, string, error) {
	sess, err := t.client.NewSession()
	if err != nil {
		return -1, "", err
	}
	defer sess.Close()

	// Separate buffers: x/crypto/ssh copies stdout and stderr in concurrent
	// goroutines, so sharing one bytes.Buffer is a data race that corrupts or
	// empties the captured output.
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return -1, stdout.String(), ctx.Err()
	case err := <-done:
		if err == nil {
			return 0, stdout.String(), nil
		}
		if ee, ok := err.(*ssh.ExitError); ok {
			return ee.ExitStatus(), stdout.String(), nil
		}
		return -1, stdout.String() + stderr.String(), err
	}
}

func (t *sshTransport) WriteFile(ctx context.Context, path string, content []byte) error {
	sess, err := t.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin = bytes.NewReader(content)
	if err := sess.Run("cat > " + shellQuote(path)); err != nil {
		return fmt.Errorf("ssh: write %s: %w", path, err)
	}
	return nil
}

func (t *sshTransport) ReadFile(ctx context.Context, path string) ([]byte, error) {
	code, out, err := t.Run(ctx, "cat "+shellQuote(path))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, ErrNotFound
	}
	return []byte(out), nil
}

func shellQuote(s string) string {
	// Paths are operator-controlled config, not end-user input.
	return "'" + s + "'"
}
