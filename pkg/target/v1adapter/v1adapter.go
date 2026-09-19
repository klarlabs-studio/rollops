// Package v1adapter presents a pkg/target (v1) implementation as a
// pkg/target/v2 one, so that v2 can be introduced without every target
// migrating in the same release (§9.6, ADR-0006).
//
// It derives capabilities by asserting the v1 optional subinterfaces. That is
// the one place a type assertion on a target is still correct, and it is
// correct because both sides are in this process, on a value this code holds
// — which is exactly what stops being true once the target is a subprocess.
package v1adapter

import (
	"context"
	"sync"

	v1 "go.klarlabs.de/rollops/pkg/target"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Adapter wraps one bound v1 target.
type Adapter struct {
	inner v1.Target
	meta  targetv2.Metadata

	mu      sync.Mutex
	applied map[string]memo
}

// memo is what an idempotency key replays. v1 has no key of its own, so the
// adapter is the thing that remembers — the map is bounded by the operations
// one deployment runs against one target in one process.
type memo struct {
	checksum string
	result   targetv2.ApplyResult
}

// New adapts t. The metadata is supplied by the host because v1 targets carry
// none: a v1 target knows how to act on its substrate and nothing about how it
// was named in configuration.
func New(t v1.Target, meta targetv2.Metadata) *Adapter {
	return &Adapter{inner: t, meta: meta, applied: map[string]memo{}}
}

// Metadata identifies the target.
func (a *Adapter) Metadata() targetv2.Metadata { return a.meta }

// alive refuses a call whose context is already done. v2 requires a target to
// honour cancellation and v1 never promised it, so the adapter answers for the
// targets that never learned to — a caller that has stopped waiting gains
// nothing from the work starting. It cannot make a v1 target interruptible
// once it is running; a target that blocks past its deadline still blocks.
func (a *Adapter) alive(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		return targetv2.Failf(targetv2.KindOf(err), op, err, "%v", err)
	}
	return nil
}

// Capabilities reports what this v1 target can do. HealthObservation is always
// true because Health is mandatory in v1. NativeRollback is always false
// because v1 has no rollback at all — the engine rolls back by applying the
// previous desired state, and saying so is what lets it keep doing that.
func (a *Adapter) Capabilities(context.Context) (targetv2.Capabilities, error) {
	caps := targetv2.Capabilities{HealthObservation: true}
	if _, ok := a.inner.(v1.Differ); ok {
		caps.DriftDetection = true
	}
	if _, ok := a.inner.(v1.Reaper); ok {
		caps.Prune = true
	}
	return caps, nil
}

// Inspect reports the live inventory. A v1 target without an Inspector still
// has a fingerprint; an empty inventory is the truthful answer rather than a
// failure, because Inspect is mandatory in v2 and v1 targets cannot all list.
func (a *Adapter) Inspect(ctx context.Context, _ targetv2.InspectRequest) (targetv2.ObservedState, error) {
	if err := a.alive(ctx, "Inspect"); err != nil {
		return targetv2.ObservedState{}, err
	}
	fp, err := a.inner.Observe(ctx)
	if err != nil {
		return targetv2.ObservedState{}, targetv2.Failf(targetv2.KindInternal, "Inspect", err, "observe: %v", err)
	}
	state := targetv2.ObservedState{Fingerprint: fp.Value, Meta: fp.Meta}
	insp, ok := a.inner.(v1.Inspector)
	if !ok {
		return state, nil
	}
	rs, err := insp.Resources(ctx)
	if err != nil {
		return targetv2.ObservedState{}, targetv2.Failf(targetv2.KindInternal, "Inspect", err, "resources: %v", err)
	}
	for _, r := range rs {
		state.Resources = append(state.Resources, targetv2.Resource{
			Kind:      r.Kind,
			Name:      r.Name,
			Namespace: r.Namespace,
			Status:    r.Status,
			Parent:    r.Parent,
		})
	}
	return state, nil
}

// Plan assembles v2's single plan call from the three v1 interfaces that each
// answered part of it: Differ says what would change, Renderer says what would
// actually be sent, and Preflighter says whether it would be refused.
func (a *Adapter) Plan(ctx context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	if err := a.alive(ctx, "Plan"); err != nil {
		return targetv2.PlanResult{}, err
	}
	m := manifest(req.Desired)

	// A target that cannot diff has no way to know whether anything would
	// change. Claiming it would not skips an apply that was needed; claiming
	// it would costs one idempotent apply.
	res := targetv2.PlanResult{Changes: true}

	if d, ok := a.inner.(v1.Differ); ok {
		diff, err := d.Diff(ctx, m)
		if err != nil {
			return targetv2.PlanResult{}, targetv2.Failf(targetv2.KindInternal, "Plan", err, "diff: %v", err)
		}
		res.Diff = diff
		res.Changes = diff != ""
	}
	if r, ok := a.inner.(v1.Renderer); ok {
		rendered, err := r.Render(ctx, m)
		if err != nil {
			return targetv2.PlanResult{}, targetv2.Failf(targetv2.KindInternal, "Plan", err, "render: %v", err)
		}
		res.Rendered = rendered
	}
	// A refused preflight is a blocker, not an error: the plan succeeded in
	// establishing that the apply would not.
	if p, ok := a.inner.(v1.Preflighter); ok {
		if err := p.Preflight(ctx, m); err != nil {
			res.Blockers = append(res.Blockers, err.Error())
		}
	}
	return res, nil
}

// Apply converges on the desired state, giving v1 the idempotency key it has
// no notion of (§9.4). The same key with the same desired state replays the
// stored result rather than applying again; the same key with a different
// request is a conflict, because guessing which of two requests the caller
// meant is worse than refusing.
func (a *Adapter) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	if err := a.alive(ctx, "Apply"); err != nil {
		return targetv2.ApplyResult{}, err
	}
	if req.IdempotencyKey == "" {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindInvalid, "Apply", nil,
			"an idempotency key is required")
	}

	a.mu.Lock()
	prior, seen := a.applied[req.IdempotencyKey]
	a.mu.Unlock()
	if seen {
		if prior.checksum != req.Desired.Checksum {
			return targetv2.ApplyResult{}, targetv2.IdempotencyConflict("Apply", req.IdempotencyKey)
		}
		return prior.result, nil
	}

	out, err := a.inner.Apply(ctx, manifest(req.Desired))
	if err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindOf(err), "Apply", err, "%v", err)
	}
	result := targetv2.ApplyResult{Changed: out.Changed, Detail: out.Detail}

	a.mu.Lock()
	a.applied[req.IdempotencyKey] = memo{checksum: req.Desired.Checksum, result: result}
	a.mu.Unlock()
	return result, nil
}

// Observe answers in one call what v1 answers in two. That is why v2 has no
// Health method: a caller wanted both every time.
func (a *Adapter) Observe(ctx context.Context, _ targetv2.ObserveRequest) (targetv2.Observation, error) {
	if err := a.alive(ctx, "Observe"); err != nil {
		return targetv2.Observation{}, err
	}
	fp, err := a.inner.Observe(ctx)
	if err != nil {
		return targetv2.Observation{}, targetv2.Failf(targetv2.KindInternal, "Observe", err, "observe: %v", err)
	}
	hs, err := a.inner.Health(ctx)
	if err != nil {
		return targetv2.Observation{}, targetv2.Failf(targetv2.KindInternal, "Observe", err, "health: %v", err)
	}
	return targetv2.Observation{
		Fingerprint: fp.Value,
		Health:      targetv2.HealthStatus{State: targetv2.HealthState(hs.State), Reason: hs.Reason},
		Meta:        fp.Meta,
	}, nil
}

// Rollback is never supported: v1 has no rollback verb.
func (a *Adapter) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

// Promote is never supported: v1 has no progressive-delivery verb. It is
// implemented rather than omitted so that the adapter answers the same way a
// plugin does — by saying no, not by being absent.
func (a *Adapter) Promote(context.Context, targetv2.PromoteRequest) (targetv2.PromoteResult, error) {
	return targetv2.PromoteResult{}, targetv2.Unsupported("Promote", targetv2.CapabilityProgressiveDelivery)
}

// DetectDrift asks the v1 Differ. Drift is a diff that is not empty.
func (a *Adapter) DetectDrift(ctx context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	d, ok := a.inner.(v1.Differ)
	if !ok {
		return targetv2.DriftResult{}, targetv2.Unsupported("DetectDrift", targetv2.CapabilityDriftDetection)
	}
	if err := a.alive(ctx, "DetectDrift"); err != nil {
		return targetv2.DriftResult{}, err
	}
	diff, err := d.Diff(ctx, manifest(req.Desired))
	if err != nil {
		return targetv2.DriftResult{}, targetv2.Failf(targetv2.KindInternal, "DetectDrift", err, "diff: %v", err)
	}
	return targetv2.DriftResult{Drifted: diff != "", Detail: diff}, nil
}

// Prune asks the v1 Reaper.
func (a *Adapter) Prune(ctx context.Context, _ targetv2.PruneRequest) (targetv2.PruneResult, error) {
	r, ok := a.inner.(v1.Reaper)
	if !ok {
		return targetv2.PruneResult{}, targetv2.Unsupported("Prune", targetv2.CapabilityPrune)
	}
	if err := a.alive(ctx, "Prune"); err != nil {
		return targetv2.PruneResult{}, err
	}
	removed, err := r.ReapTarget(ctx)
	if err != nil {
		return targetv2.PruneResult{}, targetv2.Failf(targetv2.KindInternal, "Prune", err, "reap: %v", err)
	}
	return targetv2.PruneResult{Removed: removed}, nil
}

// manifest converts desired state to v1's shape. Root does not survive: it is
// ambient local context that v1 already excludes from the checksum and from
// the wire, so a target reached through this adapter resolves from Spec the
// same way a plugin-backed one always has.
func manifest(d targetv2.DesiredState) v1.Manifest {
	return v1.Manifest{
		Kind:     d.Kind,
		Spec:     d.Spec,
		Labels:   d.Labels,
		Checksum: d.Checksum,
	}
}
