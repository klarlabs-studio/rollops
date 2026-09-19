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

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/target/ftp"
	"go.klarlabs.de/rollops/internal/target/ssh"
	conformancev2 "go.klarlabs.de/rollops/pkg/conformance/v2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
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

	sample := targetv2.DesiredState{Kind: "ssh", Spec: []byte(`{"app":"api","v":1}`), Checksum: "live-ssh-v1"}

	// §9.5's ten axes against the live server. The fakes answer instantly and
	// never fail, so this is where cancellation, timeout and the typed mapping
	// of a real refusal are measured for the first time.
	conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return ssh.New(cfg) },
		Desired: sample,
	}.Run(t)

	// End-to-end deploy + observe round-trip.
	ctx := context.Background()
	if _, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        sample,
		IdempotencyKey: targetv2.IdempotencyKeyFor("integration", "ssh-live"),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Fingerprint != sample.Checksum {
		t.Errorf("live observed %q, want %q", obs.Fingerprint, sample.Checksum)
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
	sample := targetv2.DesiredState{Kind: "ftp", Spec: []byte("<html>live</html>"), Checksum: "live-ftp-v1"}

	// vsftpd can drop the first connections during cold start; retry briefly.
	var tgt *targetv2.Bound
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
	conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return ftp.New(cfg) },
		Desired: sample,
	}.Run(t)

	ctx := context.Background()
	if _, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        sample,
		IdempotencyKey: targetv2.IdempotencyKeyFor("integration", "ftp-live"),
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.Fingerprint != sample.Checksum {
		t.Errorf("live ftp observed %q, want %q", obs.Fingerprint, sample.Checksum)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
