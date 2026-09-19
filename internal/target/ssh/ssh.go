// Package ssh is the first-party SSH/VM target — a "dumb" target that verifies
// drift against a manifest checksum stamped on the host at deploy time, rather
// than querying live state natively. Apply writes the payload and a marker file
// holding the desired checksum; Observe reads that marker back.
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
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
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
	keyPath    string // marker file holding the idempotency key of that deploy
	healthCmd  string // optional command; exit 0 == healthy
	meta       targetv2.Metadata
}

// New constructs the real SSH target from config (host/user/port/paths/health).
func New(cfg config.Target) (*targetv2.Bound, error) {
	s := spec(cfg.Spec)
	host := s.str("host")
	if host == "" {
		return nil, fmt.Errorf("ssh: target %q: spec.host is required", cfg.Ref)
	}
	tr, err := dialSSH(s)
	if err != nil {
		return nil, err
	}
	t := newWith(tr, s)
	t.meta = meta(cfg.Ref)
	return targetv2.NewBound(t, capabilities()), nil
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
		keyPath:    deployPath + ".rollops-key",
		healthCmd:  s.str("healthCmd"),
		meta:       meta("ssh/test"),
	}
}

func meta(name string) targetv2.Metadata {
	return targetv2.Metadata{Kind: "ssh", Name: name, Version: "v2"}
}

func (t *Target) Metadata() targetv2.Metadata { return t.meta }

// capabilities is the honest answer for a target that writes a file and reads a
// marker back. Drift detection is false on purpose: comparing the stamp against
// the desired checksum is what the engine already does with a fingerprint, and
// declaring it here would claim this target can see the host change underneath
// it, which it cannot. Native rollback is false because one payload path holds
// one payload — there is no previous version to return to, and the engine's
// fallback of applying the previous desired state is the right answer.
func capabilities() targetv2.Capabilities {
	return targetv2.Capabilities{HealthObservation: true}
}

func (t *Target) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return capabilities(), nil
}

// Inspect reports the stamped checksum. A dumb target has no inventory to
// enumerate: it knows the payload it wrote, not what the host made of it, and
// inventing a Resource for the file would be inventing knowledge.
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

// Plan reads the stamp and says whether Apply would write. It never touches the
// host: a dumb target's plan is a comparison, and one that wrote anything would
// have deployed.
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
		res.Diff = fmt.Sprintf("write %s and stamp %q (host carries %q)", t.deployPath, req.Desired.Checksum, cur)
	}
	return res, nil
}

// Apply writes the payload and stamps the checksum. Idempotent: if the host
// already carries the desired checksum, it is a no-op.
//
// The idempotency key is recorded on the host beside the stamp (§9.4). The case
// the key exists for is a crash between the call and its response, so the retry
// comes from a process that has forgotten everything and the answer has to be
// somewhere it can still be read. The key is written last, after the payload
// and the stamp, because the host has no atomic multi-file write and the key is
// the claim that everything before it landed: a crash before it costs a retry
// that redoes idempotent writes, where a key written first would have a retry
// believing work that never happened.
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
		return targetv2.ApplyResult{Changed: true, Detail: "replayed: this key already deployed to the host"}, nil
	}
	// A no-op leaves no key, which is right: there is nothing for a repeat to
	// replay, and repeating the check reaches the same answer by looking rather
	// than by remembering.
	if cur == req.Desired.Checksum && req.Desired.Checksum != "" {
		return targetv2.ApplyResult{Changed: false, Detail: "already at desired checksum"}, nil
	}

	if err := t.tr.WriteFile(ctx, t.deployPath, req.Desired.Spec); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindUnavailable, "Apply", err, "write payload: %v", err)
	}
	if err := t.tr.WriteFile(ctx, t.stampPath, []byte(req.Desired.Checksum)); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindUnavailable, "Apply", err, "write stamp: %v", err)
	}
	if err := t.tr.WriteFile(ctx, t.keyPath, []byte(req.IdempotencyKey)); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindUnavailable, "Apply", err, "write idempotency key: %v", err)
	}
	return targetv2.ApplyResult{Changed: true, Detail: "deployed " + t.deployPath}, nil
}

// Observe returns the stamped checksum as the fingerprint (empty if never
// deployed), the dumb-target drift signal, alongside host health.
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

// health runs the configured command (exit 0 == healthy). With no command, a
// reachable transport is reported healthy. A host that cannot be asked is
// unhealthy with the reason rather than an error: "I could not tell" and "it is
// not ready" lead to the same decision, and the reason says which.
func (t *Target) health(ctx context.Context) targetv2.HealthStatus {
	cmd := t.healthCmd
	if cmd == "" {
		if _, _, err := t.tr.Run(ctx, "true"); err != nil {
			return targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: "host unreachable"}
		}
		return targetv2.HealthStatus{State: targetv2.HealthHealthy}
	}
	code, out, err := t.tr.Run(ctx, cmd)
	switch {
	case err != nil:
		return targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: err.Error()}
	case code != 0:
		return targetv2.HealthStatus{
			State:  targetv2.HealthUnhealthy,
			Reason: fmt.Sprintf("health command exit %d: %s", code, out),
		}
	}
	return targetv2.HealthStatus{State: targetv2.HealthHealthy}
}

// readMarker reads one of the host's marker files. An absent marker is not a
// failure — a host that has never been deployed to has none — so it reads as
// empty, and only a transport that could not answer is an error.
func (t *Target) readMarker(ctx context.Context, op, path string) (string, error) {
	b, err := t.tr.ReadFile(ctx, path)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", nil
	case err != nil:
		return "", targetv2.Failf(targetv2.KindUnavailable, op, err, "read %s: %v", path, err)
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
