// Package ssh is the first-party SSH/VM target — a "dumb" target that verifies
// drift against a manifest checksum stamped on the host at deploy time, rather
// than querying live state natively. Deploy writes the payload and a marker
// file holding the desired checksum; Observe reads that marker back.
//
// All host interaction goes through the Transport interface, so the target
// logic is fully testable with an in-memory fake; the real SSH transport
// (transport_ssh.go) is one implementation.
package ssh

import (
	"context"
	"errors"
	"fmt"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Transport executes commands and moves files on a host. Implementations:
// the real SSH transport, and the in-memory fake used in tests.
type Transport interface {
	Run(ctx context.Context, cmd string) (exitCode int, stdout string, err error)
	WriteFile(ctx context.Context, path string, content []byte) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
}

// ErrNotFound is returned by Transport.ReadFile when the path does not exist.
var ErrNotFound = errors.New("ssh: file not found")

// Target deploys to a host over a Transport and stamps a checksum marker.
type Target struct {
	tr         Transport
	deployPath string // where the manifest payload is written
	stampPath  string // marker file holding the deployed checksum
	healthCmd  string // optional command; exit 0 == healthy
}

// New constructs the real SSH target from config (host/user/port/paths/health).
func New(cfg config.Target) (pt.Target, error) {
	s := spec(cfg.Spec)
	host := s.str("host")
	if host == "" {
		return nil, fmt.Errorf("ssh: target %q: spec.host is required", cfg.Ref)
	}
	tr, err := dialSSH(s)
	if err != nil {
		return nil, err
	}
	return newWith(tr, s), nil
}

func newWith(tr Transport, s spec) *Target {
	deployPath := s.str("deployPath")
	if deployPath == "" {
		deployPath = "/srv/rollops/payload"
	}
	stampPath := s.str("stampPath")
	if stampPath == "" {
		stampPath = deployPath + ".rollops-stamp"
	}
	return &Target{
		tr:         tr,
		deployPath: deployPath,
		stampPath:  stampPath,
		healthCmd:  s.str("healthCmd"),
	}
}

// Apply writes the payload and stamps the checksum. Idempotent: if the host
// already carries the desired checksum, it is a no-op.
func (t *Target) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	if cur, _ := t.readStamp(ctx); cur == m.Checksum && m.Checksum != "" {
		return pt.Result{Changed: false, Detail: "already at desired checksum"}, nil
	}
	if err := t.tr.WriteFile(ctx, t.deployPath, m.Spec); err != nil {
		return pt.Result{}, fmt.Errorf("ssh: write payload: %w", err)
	}
	if err := t.tr.WriteFile(ctx, t.stampPath, []byte(m.Checksum)); err != nil {
		return pt.Result{}, fmt.Errorf("ssh: write stamp: %w", err)
	}
	return pt.Result{Changed: true, Detail: "deployed " + t.deployPath}, nil
}

// Observe returns the stamped checksum as the fingerprint (empty if never
// deployed), the dumb-target drift signal.
func (t *Target) Observe(ctx context.Context) (pt.Fingerprint, error) {
	cur, err := t.readStamp(ctx)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return pt.Fingerprint{}, fmt.Errorf("ssh: observe: %w", err)
	}
	return pt.Fingerprint{Value: cur}, nil
}

// Health runs the configured command (exit 0 == healthy). With no command, a
// reachable transport is reported healthy.
func (t *Target) Health(ctx context.Context) (pt.HealthStatus, error) {
	if t.healthCmd == "" {
		if _, _, err := t.tr.Run(ctx, "true"); err != nil {
			return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "host unreachable"}, nil
		}
		return pt.HealthStatus{State: pt.HealthHealthy}, nil
	}
	code, out, err := t.tr.Run(ctx, t.healthCmd)
	if err != nil {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: err.Error()}, nil
	}
	if code != 0 {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: fmt.Sprintf("health command exit %d: %s", code, out)}, nil
	}
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func (t *Target) readStamp(ctx context.Context) (string, error) {
	b, err := t.tr.ReadFile(ctx, t.stampPath)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// spec is a typed view over the config target's free-form spec map.
type spec map[string]any

func (s spec) str(key string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return ""
}
