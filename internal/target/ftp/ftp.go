// Package ftp is the first-party FTP target — a "dumb" target that, like SSH,
// verifies drift against a checksum marker stamped at deploy time. File I/O
// goes through the Conn interface so the target logic is testable with an
// in-memory fake; the real FTP connection (conn_ftp.go) is one implementation.
package ftp

import (
	"context"
	"errors"
	"fmt"

	"go.klarlabs.de/rolloffs/internal/config"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// Conn stores and retrieves files on an FTP server and reports reachability.
type Conn interface {
	Store(ctx context.Context, path string, content []byte) error
	Retrieve(ctx context.Context, path string) ([]byte, error)
	Ping(ctx context.Context) error
}

// ErrNotFound is returned by Conn.Retrieve when the path does not exist.
var ErrNotFound = errors.New("ftp: file not found")

// Target deploys to an FTP server and stamps a checksum marker.
type Target struct {
	conn       Conn
	deployPath string
	stampPath  string
}

// New constructs the real FTP target from config.
func New(cfg config.Target) (pt.Target, error) {
	s := spec(cfg.Spec)
	if s.str("host") == "" {
		return nil, fmt.Errorf("ftp: target %q: spec.host is required", cfg.Ref)
	}
	conn, err := dialFTP(s)
	if err != nil {
		return nil, err
	}
	return newWith(conn, s), nil
}

func newWith(conn Conn, s spec) *Target {
	deployPath := s.str("deployPath")
	if deployPath == "" {
		deployPath = "payload"
	}
	stampPath := s.str("stampPath")
	if stampPath == "" {
		stampPath = deployPath + ".rolloffs-stamp"
	}
	return &Target{conn: conn, deployPath: deployPath, stampPath: stampPath}
}

// Apply uploads the payload and stamps the checksum. Idempotent.
func (t *Target) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	if cur, _ := t.readStamp(ctx); cur == m.Checksum && m.Checksum != "" {
		return pt.Result{Changed: false, Detail: "already at desired checksum"}, nil
	}
	if err := t.conn.Store(ctx, t.deployPath, m.Spec); err != nil {
		return pt.Result{}, fmt.Errorf("ftp: store payload: %w", err)
	}
	if err := t.conn.Store(ctx, t.stampPath, []byte(m.Checksum)); err != nil {
		return pt.Result{}, fmt.Errorf("ftp: store stamp: %w", err)
	}
	return pt.Result{Changed: true, Detail: "uploaded " + t.deployPath}, nil
}

// Observe returns the stamped checksum (empty if never deployed).
func (t *Target) Observe(ctx context.Context) (pt.Fingerprint, error) {
	cur, err := t.readStamp(ctx)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return pt.Fingerprint{}, fmt.Errorf("ftp: observe: %w", err)
	}
	return pt.Fingerprint{Value: cur}, nil
}

// Health reports the server reachable.
func (t *Target) Health(ctx context.Context) (pt.HealthStatus, error) {
	if err := t.conn.Ping(ctx); err != nil {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: err.Error()}, nil
	}
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func (t *Target) readStamp(ctx context.Context) (string, error) {
	b, err := t.conn.Retrieve(ctx, t.stampPath)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type spec map[string]any

func (s spec) str(key string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return ""
}
