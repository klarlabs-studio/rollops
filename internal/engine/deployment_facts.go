package engine

import (
	"context"

	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// A consumer recording deployment evidence — an external governance tool, an audit
// log, a dashboard — needs to know what reached where. A notification carrying only
// a target ref and a rollout ID cannot say that: the ref identifies the destination
// and the ID is resolvable only by asking us back.
//
// These are the two facts, read off the rollout's own desired state so they are
// available at every notification site without threading config through.

const (
	// LabelEnvironment is stamped from the target's declared env, so an event always
	// carries where it went without the operator doing anything.
	LabelEnvironment = "rollops.env"

	// LabelVersion is set by the operator on the config's metadata labels. Version is
	// not first-class here — it lives inside a target-specific spec, and its shape
	// differs per kind — so this is a declared fact rather than one we extract. An
	// absent label yields an empty version, which reports "unreported" rather than a
	// value guessed from a spec we do not model.
	LabelVersion = "rollops.version"
)

// deploymentFacts returns the version and environment a rollout represents.
func deploymentFacts(r rollout.Rollout) (version, environment string) {
	return labelOf(r.Desired, LabelVersion), labelOf(r.Desired, LabelEnvironment)
}

func labelOf(m pt.Manifest, key string) string {
	if m.Labels == nil {
		return ""
	}
	return m.Labels[key]
}

// notifyDeployment emits an event describing what reached where.
//
// Separate from notifyEvent because only the deployment-outcome kinds carry these
// facts: an approval request is about a rollout that has not happened yet, and
// stamping a version on it would report a deployment that did not occur.
func (e *Engine) notifyDeployment(ctx context.Context, kind notify.Kind, r rollout.Rollout, detail string) {
	version, environment := deploymentFacts(r)
	e.notifyEvent(ctx, notify.Event{
		Kind:        kind,
		TargetRef:   r.TargetRef,
		RolloutID:   r.ID,
		Detail:      detail,
		Version:     version,
		Environment: environment,
	})
}
