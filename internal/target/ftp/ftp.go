// Package ftp is the first-party FTP target — a "dumb" target that, like SSH,
// verifies drift against a checksum marker stamped at deploy time. File I/O
// goes through the Conn interface so the target logic is testable with an
// in-memory fake; the real FTP connection (conn_ftp.go) is one implementation.
package ftp

import (
	"context"
	"errors"
	"fmt"

	"go.klarlabs.de/rollops/internal/config"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
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
	keyPath    string // marker holding the idempotency key of the last upload
	meta       targetv2.Metadata
}

// New constructs the real FTP target from config.
func New(cfg config.Target) (*targetv2.Bound, error) {
	s := spec(cfg.Spec)
	if s.str("host") == "" {
		return nil, fmt.Errorf("ftp: target %q: spec.host is required", cfg.Ref)
	}
	conn, err := dialFTP(s)
	if err != nil {
		return nil, err
	}
	t := newWith(conn, s)
	t.meta = meta(cfg.Ref)
	return targetv2.NewBound(t, capabilities()), nil
}

func newWith(conn Conn, s spec) *Target {
	deployPath := s.str("deployPath")
	if deployPath == "" {
		deployPath = "payload"
	}
	stampPath := s.str("stampPath")
	if stampPath == "" {
		stampPath = deployPath + ".rollops-stamp"
	}
	return &Target{
		conn:       conn,
		deployPath: deployPath,
		stampPath:  stampPath,
		keyPath:    deployPath + ".rollops-key",
		meta:       meta("ftp/test"),
	}
}

func meta(name string) targetv2.Metadata {
	return targetv2.Metadata{Kind: "ftp", Name: name, Version: "v2"}
}

func (t *Target) Metadata() targetv2.Metadata { return t.meta }

// capabilities is the honest answer for a target that can store a file and ask
// whether the server answers. Everything else is false, and health is the one
// true entry only because reachability is a real verdict: a server that cannot
// be reached is one an apply would fail against.
func capabilities() targetv2.Capabilities {
	return targetv2.Capabilities{HealthObservation: true}
}

func (t *Target) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return capabilities(), nil
}

// Inspect reports the stamped checksum. There is no inventory: this target
// knows the file it uploaded, not what the server serves from it.
func (t *Target) Inspect(ctx context.Context, _ targetv2.InspectRequest) (targetv2.ObservedState, error) {
	if err := targetv2.Abandoned(ctx, "Inspect"); err != nil {
		return targetv2.ObservedState{}, err
	}
	cur, err := t.readMarker(ctx, "Inspect", t.stampPath)
	if err != nil {
		return targetv2.ObservedState{}, err
	}
	return targetv2.ObservedState{Fingerprint: cur}, nil
}

// Plan compares the stamp against the desired checksum and uploads nothing.
func (t *Target) Plan(ctx context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	if err := targetv2.Abandoned(ctx, "Plan"); err != nil {
		return targetv2.PlanResult{}, err
	}
	cur, err := t.readMarker(ctx, "Plan", t.stampPath)
	if err != nil {
		return targetv2.PlanResult{}, err
	}
	changes := cur != req.Desired.Checksum
	res := targetv2.PlanResult{Changes: changes, Rendered: req.Desired.Spec}
	if changes {
		// The payload is opaque bytes that may carry anything, so the diff names
		// the change rather than showing it (INV-012).
		res.Diff = fmt.Sprintf("upload %s and stamp %q (server carries %q)", t.deployPath, req.Desired.Checksum, cur)
	}
	return res, nil
}

// Apply uploads the payload and stamps the checksum. Idempotent.
//
// The idempotency key is stored on the server beside the stamp (§9.4), because
// the case the key exists for is a crash between the call and its response and
// the retry comes from a process that has forgotten everything. It is stored
// last: FTP has no atomic multi-file write, and the key is the claim that the
// payload and the stamp landed.
func (t *Target) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	if err := targetv2.Abandoned(ctx, "Apply"); err != nil {
		return targetv2.ApplyResult{}, err
	}
	if req.IdempotencyKey == "" {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindInvalid, "Apply", nil,
			"an idempotency key is required")
	}
	cur, err := t.readMarker(ctx, "Apply", t.stampPath)
	if err != nil {
		return targetv2.ApplyResult{}, err
	}
	spent, err := t.readMarker(ctx, "Apply", t.keyPath)
	if err != nil {
		return targetv2.ApplyResult{}, err
	}
	if spent != "" && spent == req.IdempotencyKey {
		if cur != req.Desired.Checksum {
			return targetv2.ApplyResult{}, targetv2.IdempotencyConflict("Apply", req.IdempotencyKey)
		}
		return targetv2.ApplyResult{Changed: true, Detail: "replayed: this key already uploaded to the server"}, nil
	}
	// A no-op leaves no key: there is nothing for a repeat to replay, and
	// repeating the check reaches the same answer by looking.
	if cur == req.Desired.Checksum && req.Desired.Checksum != "" {
		return targetv2.ApplyResult{Changed: false, Detail: "already at desired checksum"}, nil
	}

	if err := t.conn.Store(ctx, t.deployPath, req.Desired.Spec); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindUnavailable, "Apply", err, "store payload: %v", err)
	}
	if err := t.conn.Store(ctx, t.stampPath, []byte(req.Desired.Checksum)); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindUnavailable, "Apply", err, "store stamp: %v", err)
	}
	if err := t.conn.Store(ctx, t.keyPath, []byte(req.IdempotencyKey)); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindUnavailable, "Apply", err, "store idempotency key: %v", err)
	}
	return targetv2.ApplyResult{Changed: true, Detail: "uploaded " + t.deployPath}, nil
}

// Observe returns the stamped checksum (empty if never deployed) and whether
// the server answers.
func (t *Target) Observe(ctx context.Context, _ targetv2.ObserveRequest) (targetv2.Observation, error) {
	if err := targetv2.Abandoned(ctx, "Observe"); err != nil {
		return targetv2.Observation{}, err
	}
	cur, err := t.readMarker(ctx, "Observe", t.stampPath)
	if err != nil {
		return targetv2.Observation{}, err
	}
	return targetv2.Observation{Fingerprint: cur, Health: t.health(ctx)}, nil
}

// Rollback is not supported: see capabilities.
func (t *Target) Rollback(ctx context.Context, _ targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	if err := targetv2.Abandoned(ctx, "Rollback"); err != nil {
		return targetv2.RollbackResult{}, err
	}
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

// health reports the server reachable. An unreachable server is unhealthy with
// the reason rather than an error: it is a verdict about the target, not a
// failure of the call that asked.
func (t *Target) health(ctx context.Context) targetv2.HealthStatus {
	if err := t.conn.Ping(ctx); err != nil {
		return targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: err.Error()}
	}
	return targetv2.HealthStatus{State: targetv2.HealthHealthy}
}

// readMarker reads one of the server's marker files. An absent marker is not a
// failure — a server never deployed to has none — so it reads as empty, and
// only a connection that could not answer is an error.
func (t *Target) readMarker(ctx context.Context, op, path string) (string, error) {
	b, err := t.conn.Retrieve(ctx, path)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", nil
	case err != nil:
		return "", targetv2.Failf(targetv2.KindUnavailable, op, err, "retrieve %s: %v", path, err)
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
