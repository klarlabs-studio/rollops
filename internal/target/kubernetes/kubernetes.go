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
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// ChecksumAnnotation is where the deployed checksum is recorded on the resource.
const ChecksumAnnotation = "rollops.klarlabs.de/checksum"

// Cluster abstracts the live cluster operations the target needs.
type Cluster interface {
	// Apply applies the manifest and records checksum as the resource annotation.
	Apply(ctx context.Context, manifest []byte, checksum string) error
	// LiveChecksum reads the recorded checksum from the live cluster (empty if
	// the resource is absent or unmanaged).
	LiveChecksum(ctx context.Context) (string, error)
	// LiveYAML is the live object as YAML (empty if absent). Used with
	// ignoreDifferences so ignored field drift is not reported.
	LiveYAML(ctx context.Context) ([]byte, error)
	// Healthy reports rollout readiness.
	Healthy(ctx context.Context) (bool, string, error)
	// Diff returns the difference between the manifest and live state.
	Diff(ctx context.Context, manifest []byte) (string, error)
	// Resources lists the live managed resources.
	Resources(ctx context.Context) ([]pt.Resource, error)
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
}

// New constructs the real kubectl-backed target from config, applying the
// multi-tenant confinement policy resolved from the daemon environment.
func New(cfg config.Target) (pt.Target, error) {
	return newTarget(cfg, security.ConfinementFromEnv(os.Getenv))
}

// newTarget is the test seam: it takes an explicit confinement policy so the
// namespace allowlist and cluster confinement can be exercised without mutating
// the process environment.
func newTarget(cfg config.Target, conf security.Confinement) (pt.Target, error) {
	s := spec(cfg.Spec)
	if s.str("resource") == "" {
		return nil, fmt.Errorf("kubernetes: target %q: spec.resource is required (e.g. deployment/api)", cfg.Ref)
	}
	cl, err := newKubectl(s, cfg.Ref, conf)
	if err != nil {
		return nil, err
	}
	return &Target{cl: cl, run: execRunner, ignore: parseIgnore(s)}, nil
}

func newWith(cl Cluster) *Target { return &Target{cl: cl, run: execRunner} }

// Apply reconciles the cluster to the desired manifest. It is idempotent: when
// the stamped checksum matches AND a live diff confirms the cluster genuinely
// matches desired, it is a no-op. A matching stamp alone is not trusted — an
// out-of-band field edit (e.g. `kubectl set image`) leaves the stamp intact, so
// Apply re-applies when the diff shows drift, correcting it.
func (t *Target) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	manifest, err := desiredManifest(ctx, m, t.run)
	if err != nil {
		return pt.Result{}, err
	}
	if cur, _ := t.cl.LiveChecksum(ctx); cur == m.Checksum && m.Checksum != "" {
		// Stamp matches; confirm live really equals desired before skipping.
		if diff, derr := t.cl.Diff(ctx, manifest); derr == nil && strings.TrimSpace(diff) == "" {
			return pt.Result{Changed: false, Detail: "cluster already at desired checksum"}, nil
		}
		// Non-empty diff (or diff unavailable): live drifted — fall through and
		// re-apply to correct it.
	}
	if err := t.cl.Apply(ctx, manifest, m.Checksum); err != nil {
		return pt.Result{}, fmt.Errorf("kubernetes: apply: %w", err)
	}
	return pt.Result{Changed: true, Detail: "applied to cluster"}, nil
}

// Observe reads the live checksum annotation — the rich drift signal.
// ignoreDifferences does not change the stamp; it filters the live Diff used
// for detect/full verification and Apply's no-op check.
func (t *Target) Observe(ctx context.Context) (pt.Fingerprint, error) {
	cur, err := t.cl.LiveChecksum(ctx)
	if err != nil {
		return pt.Fingerprint{}, fmt.Errorf("kubernetes: observe: %w", err)
	}
	return pt.Fingerprint{Value: cur}, nil
}

// Diff implements target.Differ: the diff between desired and live cluster state.
// Fields listed in spec.ignoreDifferences are stripped before the emptiness
// check so HPA replicas and similar controller writes are not drift. Apply
// stays kubectl apply; this only changes whether a diff counts.
func (t *Target) Diff(ctx context.Context, desired pt.Manifest) (string, error) {
	manifest, err := desiredManifest(ctx, desired, t.run)
	if err != nil {
		return "", err
	}
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

// desiredManifest resolves the concrete manifest bytes for m. A referenced
// source (manifestFrom) captures its rendered output on the manifest at apply
// time (Manifest.Rendered); reusing those bytes means a rollback restores
// exactly what was deployed and needs no checkout Root to re-render — the source
// files may have changed since, and the manual CLI/UI/API rollback path has no
// Root at all. Inline manifests and the legacy flat keys carry no Rendered and
// resolve deterministically from Spec.
func desiredManifest(ctx context.Context, m pt.Manifest, run cmdRunner) ([]byte, error) {
	if len(m.Rendered) > 0 {
		return m.Rendered, nil
	}
	return manifestFromSpec(ctx, m.Spec, m.Root, run)
}

// Render implements target.Renderer: it resolves the desired manifest to
// concrete bytes (Helm/Kustomize/path rendering included). The engine surfaces
// the result in the plan and, when Referenced, stamps the drift checksum over
// it.
func (t *Target) Render(ctx context.Context, desired pt.Manifest) ([]byte, error) {
	return manifestFromSpec(ctx, desired.Spec, desired.Root, t.run)
}

// Referenced implements target.Renderer: it reports whether the desired
// manifest is resolved from an external source (manifestFrom) rather than an
// inline manifest or the legacy flat keys. Cheap — it inspects the spec only.
func (t *Target) Referenced(desired pt.Manifest) bool {
	return specReferencesSource(desired.Spec)
}

// Resources implements target.Inspector: the live managed resources.
func (t *Target) Resources(ctx context.Context) ([]pt.Resource, error) {
	return t.cl.Resources(ctx)
}

// ReapTarget implements target.Reaper: remove resources this target marked as
// its own after the RolloutConfig itself was deleted (#154). Forwards to the
// cluster backend; kubectlCluster refuses unless reapOnDelete was set.
func (t *Target) ReapTarget(ctx context.Context) (int, error) {
	return t.cl.ReapTarget(ctx)
}

// Health reports rollout readiness.
func (t *Target) Health(ctx context.Context) (pt.HealthStatus, error) {
	ok, reason, err := t.cl.Healthy(ctx)
	if err != nil {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: err.Error()}, nil
	}
	if !ok {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: reason}, nil
	}
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
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
