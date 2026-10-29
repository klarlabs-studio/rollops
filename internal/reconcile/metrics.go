package reconcile

import (
	"context"
	"errors"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
)

// MetricsSink receives what each watcher tick did. *metrics.Metrics
// implements it; the interface keeps this package free of Prometheus.
type MetricsSink interface {
	ObserveTick(d time.Duration)
	CountReconcile(result string)
	IncDrift()
	IncOutcome(outcome string)
	SetOldestInFlight(seconds float64)
}

// WithMetrics records every tick to sink. Before this option nothing ever
// recorded a metric, so /metrics returned zeros however rollops was doing.
func WithMetrics(sink MetricsSink) WatcherOption {
	return func(w *Watcher) { w.metrics = sink }
}

// recordTick counts one tick's outcomes by result: insync, drift (detected,
// not corrected), reconciled, or error. A follower's ErrNotLeader is not an
// outcome and counts as nothing.
func recordTick(sink MetricsSink, d time.Duration, outcomes []RepoOutcome) {
	if sink == nil {
		return
	}
	sink.ObserveTick(d)
	for _, o := range outcomes {
		switch {
		case errors.Is(o.Err, ErrNotLeader):
			continue
		case o.Err != nil:
			sink.CountReconcile("error")
		case o.Outcome.Reconciled:
			sink.CountReconcile("reconciled")
		case o.Outcome.Drift:
			sink.CountReconcile("drift")
		default:
			sink.CountReconcile("insync")
		}
		if o.Outcome.Drift {
			sink.IncDrift()
		}
		if o.Err == nil && o.Outcome.Rollout != nil && o.Outcome.Rollout.Phase.Settled() {
			sink.IncOutcome(string(o.Outcome.Rollout.Phase))
		}
	}
}

// oldestInFlight returns how long the oldest target has been held in
// deploying or verifying, judged by each target's latest rollout. Paused and
// awaiting-approval are waits a human chose and are not counted. This is the
// 2026-09-19 wedge as a number: a target stuck mid-rollout for 31 days.
func oldestInFlight(now time.Time, rollouts []rollout.Rollout) float64 {
	latest := map[string]rollout.Rollout{}
	for _, r := range rollouts {
		if cur, ok := latest[r.TargetRef]; !ok || r.UpdatedAt.After(cur.UpdatedAt) {
			latest[r.TargetRef] = r
		}
	}
	var oldest time.Duration
	for _, r := range latest {
		if r.Phase != rollout.PhaseDeploying && r.Phase != rollout.PhaseVerifying {
			continue
		}
		if age := now.Sub(r.UpdatedAt); age > oldest {
			oldest = age
		}
	}
	return oldest.Seconds()
}

// recordInFlight updates the in-flight gauge from the store. A store error
// leaves the gauge unchanged; /readyz reports an unreadable store.
func (w *Watcher) recordInFlight(ctx context.Context) {
	if w.metrics == nil {
		return
	}
	rs, err := w.rec.eng.List(ctx, 0)
	if err != nil {
		return
	}
	w.metrics.SetOldestInFlight(oldestInFlight(w.now(), rs))
}
