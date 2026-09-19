// Package kubernetes is the first-party Kubernetes target — a "rich" target
// that queries live cluster state rather than a stamped file. It records the
// deployed checksum as an annotation on the managed resource and reads it back
// from the live cluster on Observe, so drift reflects the actual cluster, not a
// local marker.
//
// To honour the core constraint "no Kubernetes dependency", this target drives
// the cluster through the external kubectl binary via the Cluster interface —
// client-go is never compiled into the Rollops core. The logic is testable
// with an in-memory fake cluster.
package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/security"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// ChecksumAnnotation is where the deployed checksum is recorded on the resource.
const ChecksumAnnotation = "rollops.klarlabs.de/checksum"

// KeyAnnotation is where the idempotency key of the apply that last converged
// the resource is recorded. It lives on the cluster rather than in this process
// because the case §9.4 exists for is a crash between the call and its
// response: the retry comes from a process that has forgotten everything, so a
// memo held in memory is gone exactly when it is needed.
const KeyAnnotation = "rollops.klarlabs.de/idempotency-key"

// Cluster abstracts the live cluster operations the target needs.
type Cluster interface {
	// Apply applies the manifest and records checksum and key as annotations on
	// the resource. Both are written together: a key that outlived its checksum
	// would replay a result for a state that is no longer there.
	Apply(ctx context.Context, manifest []byte, checksum, key string) error
	// Preflight reports whether Apply would be accepted, changing nothing.
	Preflight(ctx context.Context, manifest []byte) error
	// LiveChecksum reads the recorded checksum from the live cluster (empty if
	// the resource is absent or unmanaged).
	LiveChecksum(ctx context.Context) (string, error)
	// LiveKey reads the recorded idempotency key (empty if the resource is
	// absent, unmanaged, or was last left alone rather than applied).
	LiveKey(ctx context.Context) (string, error)
	// LiveYAML is the live object as YAML (empty if absent). Used with
	// ignoreDifferences so ignored field drift is not reported.
	LiveYAML(ctx context.Context) ([]byte, error)
	// Healthy reports rollout readiness.
	Healthy(ctx context.Context) (bool, string, error)
	// Diff returns the difference between the manifest and live state.
	Diff(ctx context.Context, manifest []byte) (string, error)
	// Resources lists the live managed resources.
	Resources(ctx context.Context) ([]targetv2.Resource, error)
	// ReapTarget removes resources carrying this target's marker. Invoked only
	// when a RolloutConfig was deleted and the instance set reapOnDelete.
	ReapTarget(ctx context.Context) (removed int, err error)
}

// Target deploys to a Kubernetes cluster through a Cluster. It renders the
// desired manifest from raw YAML, a Helm chart, or a Kustomize overlay.
type Target struct {
	cl     Cluster
	run    cmdRunner // helm/kubectl renderer; injectable for tests
	ignore []string  // json-pointers / field paths ignored in Observe/Diff
	meta   targetv2.Metadata
}

// New constructs the real kubectl-backed target from config, applying the
// multi-tenant confinement policy resolved from the daemon environment. It is
// a target.Factory: the binding carries the capabilities, and this target
// knows them without asking anything, so there is nothing to fail.
func New(cfg config.Target) (*targetv2.Bound, error) {
	t, err := newTarget(cfg, security.ConfinementFromEnv(os.Getenv))
	if err != nil {
		return nil, err
	}
	return targetv2.NewBound(t, capabilities()), nil
}

// newTarget is the test seam: it takes an explicit confinement policy so the
// namespace allowlist and cluster confinement can be exercised without mutating
// the process environment.
func newTarget(cfg config.Target, conf security.Confinement) (*Target, error) {
	s := spec(cfg.Spec)
	if s.str("resource") == "" {
		return nil, fmt.Errorf("kubernetes: target %q: spec.resource is required (e.g. deployment/api)", cfg.Ref)
	}
	cl, err := newKubectl(s, cfg.Ref, conf)
	if err != nil {
		return nil, err
	}
	return &Target{cl: cl, run: execRunner, ignore: parseIgnore(s), meta: meta(cfg.Ref)}, nil
}

func newWith(cl Cluster) *Target {
	return &Target{cl: cl, run: execRunner, meta: meta("kubernetes/test")}
}

func meta(name string) targetv2.Metadata {
	return targetv2.Metadata{Kind: "kubernetes", Name: name, Version: "v2"}
}

// Metadata identifies this target.
func (t *Target) Metadata() targetv2.Metadata { return t.meta }

// capabilities is what this target can do, and it is the same answer for every
// instance: the substrate is kubectl, not configuration.
//
// NativeRollback is false because kubectl has no rollback this target could
// drive — `kubectl rollout undo` returns a Deployment to its previous
// ReplicaSet, which is not the same thing as returning a target to the
// previous desired state, and the engine already does the latter correctly.
// Saying false is what keeps it doing so.
func capabilities() targetv2.Capabilities {
	return targetv2.Capabilities{
		DriftDetection:    true,
		Prune:             true,
		HealthObservation: true,
	}
}

// Capabilities reports what this target can do.
func (t *Target) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return capabilities(), nil
}

// Inspect lists what is live, with the stamped checksum as the fingerprint.
func (t *Target) Inspect(ctx context.Context, _ targetv2.InspectRequest) (targetv2.ObservedState, error) {
	if err := targetv2.Abandoned(ctx, "Inspect"); err != nil {
		return targetv2.ObservedState{}, err
	}
	cur, err := t.cl.LiveChecksum(ctx)
	if err != nil {
		return targetv2.ObservedState{}, targetv2.Failf(targetv2.KindOf(err), "Inspect", err, "live checksum: %v", err)
	}
	rs, err := t.cl.Resources(ctx)
	if err != nil {
		return targetv2.ObservedState{}, targetv2.Failf(targetv2.KindOf(err), "Inspect", err, "resources: %v", err)
	}
	return targetv2.ObservedState{Fingerprint: cur, Resources: rs}, nil
}

// Plan answers in one call what v1 asked in three, and renders once to do it.
// Under v1 the engine called Diff, Render and Preflight in turn and each
// re-rendered the desired manifest from scratch — three helm templates for one
// question, and three chances for them to disagree.
//
// A refused preflight is a blocker rather than an error: the plan succeeded in
// establishing that the apply would not.
func (t *Target) Plan(ctx context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	if err := targetv2.Abandoned(ctx, "Plan"); err != nil {
		return targetv2.PlanResult{}, err
	}
	manifest, err := t.desired(ctx, "Plan", req.Desired)
	if err != nil {
		return targetv2.PlanResult{}, err
	}
	diff, err := t.diff(ctx, manifest)
	if err != nil {
		return targetv2.PlanResult{}, targetv2.Failf(targetv2.KindOf(err), "Plan", err, "diff: %v", err)
	}
	res := targetv2.PlanResult{Changes: diff != "", Diff: diff, Rendered: manifest}
	// A spec that points at an external source is checksummed over the pointer,
	// so the host records this one over the bytes instead.
	if specReferencesSource(req.Desired.Spec) {
		sum := sha256.Sum256(manifest)
		res.RenderedChecksum = hex.EncodeToString(sum[:])
	}
	if err := t.cl.Preflight(ctx, manifest); err != nil {
		res.Blockers = append(res.Blockers, err.Error())
	}
	return res, nil
}

// Apply reconciles the cluster to the desired manifest. It is idempotent: when
// the stamped checksum matches AND a live diff confirms the cluster genuinely
// matches desired, it is a no-op. A matching stamp alone is not trusted — an
// out-of-band field edit (e.g. `kubectl set image`) leaves the stamp intact, so
// Apply re-applies when the diff shows drift, correcting it.
//
// The idempotency key is answered from the cluster (§9.4). A key that is
// already stamped there was stamped by the apply that converged it, so the
// same key replays that apply's result; the same key over a different desired
// state is a conflict, because guessing which of the two the caller meant is
// worse than refusing. A no-op leaves no stamp, which is right: there is
// nothing for a repeat to replay, and repeating the check reaches the same
// answer by looking rather than by remembering.
func (t *Target) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	if err := targetv2.Abandoned(ctx, "Apply"); err != nil {
		return targetv2.ApplyResult{}, err
	}
	if req.IdempotencyKey == "" {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindInvalid, "Apply", nil,
			"an idempotency key is required")
	}
	manifest, err := t.desired(ctx, "Apply", req.Desired)
	if err != nil {
		return targetv2.ApplyResult{}, err
	}

	cur, err := t.cl.LiveChecksum(ctx)
	if err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindOf(err), "Apply", err, "live checksum: %v", err)
	}
	key, err := t.cl.LiveKey(ctx)
	if err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindOf(err), "Apply", err, "live idempotency key: %v", err)
	}
	if key != "" && key == req.IdempotencyKey {
		if cur != req.Desired.Checksum {
			return targetv2.ApplyResult{}, targetv2.IdempotencyConflict("Apply", req.IdempotencyKey)
		}
		return targetv2.ApplyResult{Changed: true, Detail: "replayed: this key already converged the cluster"}, nil
	}

	if cur == req.Desired.Checksum && req.Desired.Checksum != "" {
		// Stamp matches; confirm live really equals desired before skipping.
		if diff, derr := t.cl.Diff(ctx, manifest); derr == nil && strings.TrimSpace(diff) == "" {
			return targetv2.ApplyResult{Changed: false, Detail: "cluster already at desired checksum"}, nil
		}
		// Non-empty diff (or diff unavailable): live drifted — fall through and
		// re-apply to correct it.
	}
	if err := t.cl.Apply(ctx, manifest, req.Desired.Checksum, req.IdempotencyKey); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindOf(err), "Apply", err, "apply: %v", err)
	}
	return targetv2.ApplyResult{Changed: true, Detail: "applied to cluster"}, nil
}

// Observe answers in one call what v1 answered in two: the stamped checksum —
// the rich drift signal, read back from the live cluster rather than from a
// local marker — and rollout readiness.
//
// ignoreDifferences does not change the stamp; it filters the live diff used
// for verification and Apply's no-op check.
func (t *Target) Observe(ctx context.Context, _ targetv2.ObserveRequest) (targetv2.Observation, error) {
	if err := targetv2.Abandoned(ctx, "Observe"); err != nil {
		return targetv2.Observation{}, err
	}
	cur, err := t.cl.LiveChecksum(ctx)
	if err != nil {
		return targetv2.Observation{}, targetv2.Failf(targetv2.KindOf(err), "Observe", err, "live checksum: %v", err)
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

// DetectDrift reports whether the live cluster has moved away from desired.
func (t *Target) DetectDrift(ctx context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	if err := targetv2.Abandoned(ctx, "DetectDrift"); err != nil {
		return targetv2.DriftResult{}, err
	}
	manifest, err := t.desired(ctx, "DetectDrift", req.Desired)
	if err != nil {
		return targetv2.DriftResult{}, err
	}
	diff, err := t.diff(ctx, manifest)
	if err != nil {
		return targetv2.DriftResult{}, targetv2.Failf(targetv2.KindOf(err), "DetectDrift", err, "diff: %v", err)
	}
	return targetv2.DriftResult{Drifted: diff != "", Detail: diff}, nil
}

// Prune removes resources this target marked as its own after the
// RolloutConfig itself was deleted (#154). Forwards to the cluster backend;
// kubectlCluster refuses unless reapOnDelete was set.
func (t *Target) Prune(ctx context.Context, _ targetv2.PruneRequest) (targetv2.PruneResult, error) {
	if err := targetv2.Abandoned(ctx, "Prune"); err != nil {
		return targetv2.PruneResult{}, err
	}
	removed, err := t.cl.ReapTarget(ctx)
	if err != nil {
		return targetv2.PruneResult{}, targetv2.Failf(targetv2.KindOf(err), "Prune", err, "reap: %v", err)
	}
	return targetv2.PruneResult{Removed: removed}, nil
}

// health reports rollout readiness. A cluster that cannot be asked is reported
// unhealthy with the reason rather than as an error: "I could not tell" and
// "it is not ready" lead to the same decision here, and the reason says which.
func (t *Target) health(ctx context.Context) targetv2.HealthStatus {
	ok, reason, err := t.cl.Healthy(ctx)
	switch {
	case err != nil:
		return targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: err.Error()}
	case !ok:
		return targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: reason}
	}
	return targetv2.HealthStatus{State: targetv2.HealthHealthy}
}

// diff is the live difference, with spec.ignoreDifferences applied. Fields
// listed there are stripped before the emptiness check so HPA replicas and
// similar controller writes are not drift. Apply stays kubectl apply; this only
// changes whether a difference counts.
func (t *Target) diff(ctx context.Context, manifest []byte) (string, error) {
	if len(t.ignore) > 0 {
		if live, lerr := t.cl.LiveYAML(ctx); lerr == nil && strings.TrimSpace(string(live)) != "" {
			same, serr := equivalentIgnoring(live, manifest, t.ignore)
			if serr == nil && same {
				return "", nil
			}
		}
	}
	return t.cl.Diff(ctx, manifest)
}

// desired resolves the concrete manifest bytes for d. A referenced source
// (manifestFrom) carries its rendered output on the desired state; reusing
// those bytes means a rollback restores exactly what was deployed rather than
// re-resolving the pointer against files that may have changed since — which
// is the drift the checksum exists to catch. Inline manifests and the legacy
// flat keys carry no Rendered and resolve deterministically from Spec.
//
// A spec this target cannot render is malformed input, not an internal
// failure, and says so: the host retries what is unavailable and does not
// retry what is invalid.
func (t *Target) desired(ctx context.Context, op string, d targetv2.DesiredState) ([]byte, error) {
	if len(d.Rendered) > 0 {
		return d.Rendered, nil
	}
	out, err := manifestFromSpec(ctx, d.Spec, "", t.run)
	if err != nil {
		return nil, targetv2.Failf(targetv2.KindInvalid, op, err, "render desired manifest: %v", err)
	}
	return out, nil
}

type spec map[string]any

func (s spec) str(key string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return ""
}

func (s spec) boolVal(key string) bool {
	b, _ := s[key].(bool)
	return b
}

// strSlice reads a list-of-strings setting. YAML decodes an untyped list as
// []any, so both shapes are accepted; anything else yields nil, which callers
// read as "unset" and fall back to their default.
func (s spec) strSlice(key string) []string {
	switch v := s[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if str, ok := e.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
