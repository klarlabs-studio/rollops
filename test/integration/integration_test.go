//go:build integration

// Package integration runs the dumb targets against real SSH and FTP servers
// (docker-compose.test.yml). It is gated behind the `integration` build tag so
// the default `go test ./...` stays hermetic; run it via run.sh.
//
// Connection details come from the environment (set by run.sh):
//
//	SSH_HOST, SSH_PORT, SSH_USER, SSH_KEY, SSH_DEPLOY_PATH
//	FTP_HOST, FTP_PORT, FTP_USER, FTP_PASSWORD, FTP_DEPLOY_PATH
package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/target/ftp"
	"go.klarlabs.de/rolloffs/internal/target/ssh"
	"go.klarlabs.de/rolloffs/pkg/conformance"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

func env(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set; run via test/integration/run.sh", key)
	}
	return v
}

func TestSSHTarget_Live(t *testing.T) {
	host := env(t, "SSH_HOST")
	cfg := config.Target{
		Kind: "ssh",
		Ref:  "integration/ssh",
		Spec: map[string]any{
			"host":                     host,
			"port":                     getenv("SSH_PORT", "2222"),
			"user":                     getenv("SSH_USER", "deploy"),
			"privateKeyPath":           env(t, "SSH_KEY"),
			"deployPath":               getenv("SSH_DEPLOY_PATH", "/config/deploy/app"),
			"insecureSkipHostKeyCheck": true,
		},
	}
	tgt, err := ssh.New(cfg)
	if err != nil {
		t.Fatalf("connect ssh: %v", err)
	}

	sample := pt.Manifest{Kind: "ssh", Spec: []byte(`{"app":"api","v":1}`), Checksum: "live-ssh-v1"}

	// Full conformance against the live server: idempotency, fingerprint
	// stability, health.
	conformance.Run(t, func() (pt.Target, error) { return ssh.New(cfg) }, sample)

	// End-to-end deploy + observe round-trip.
	ctx := context.Background()
	if _, err := tgt.Apply(ctx, sample); err != nil {
		t.Fatalf("apply: %v", err)
	}
	fp, err := tgt.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if fp.Value != sample.Checksum {
		t.Errorf("live observed %q, want %q", fp.Value, sample.Checksum)
	}
}

func TestFTPTarget_Live(t *testing.T) {
	host := env(t, "FTP_HOST")
	cfg := config.Target{
		Kind: "ftp",
		Ref:  "integration/ftp",
		Spec: map[string]any{
			"host":       host,
			"port":       getenv("FTP_PORT", "21"),
			"user":       getenv("FTP_USER", "deploy"),
			"password":   env(t, "FTP_PASSWORD"),
			"deployPath": getenv("FTP_DEPLOY_PATH", "index.html"),
		},
	}
	sample := pt.Manifest{Kind: "ftp", Spec: []byte("<html>live</html>"), Checksum: "live-ftp-v1"}

	// vsftpd can drop the first connections during cold start; retry briefly.
	var tgt pt.Target
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		tgt, err = ftp.New(cfg)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		t.Fatalf("connect ftp after retries: %v", err)
	}
	ctx := context.Background()
	if _, err := tgt.Apply(ctx, sample); err != nil {
		t.Fatalf("apply: %v", err)
	}
	fp, err := tgt.Observe(ctx)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if fp.Value != sample.Checksum {
		t.Errorf("live ftp observed %q, want %q", fp.Value, sample.Checksum)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
