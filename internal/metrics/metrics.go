// Package metrics is Rollops' self-observability: the daemon exposes its own
// health and Prometheus-style metrics (reconcile latency, drift count, rollout
// outcomes) so operators can monitor Rollops itself — independent of the
// deferred target-observability seam.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the daemon's own instruments on a private registry.
type Metrics struct {
	reg              *prometheus.Registry
	reconcileLatency prometheus.Histogram
	driftTotal       prometheus.Counter
	rolloutOutcomes  *prometheus.CounterVec
	reconcileTotal   *prometheus.CounterVec
	oldestInFlight   prometheus.Gauge
}

// New builds the metric set on a fresh registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		reconcileLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rollops_reconcile_latency_seconds",
			Help:    "Time spent in one reconcile tick across all watched repos.",
			Buckets: prometheus.DefBuckets,
		}),
		driftTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rollops_drift_detected_total",
			Help: "Number of drift events detected.",
		}),
		rolloutOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rollops_rollout_outcomes_total",
			Help: "Rollout outcomes by result.",
		}, []string{"outcome"}),
		reconcileTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rollops_reconcile_total",
			Help: "Per-repo reconcile results by tick (insync, drift, reconciled, error).",
		}, []string{"result"}),
		oldestInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rollops_rollout_inflight_oldest_seconds",
			Help: "Age of the oldest target held in deploying or verifying (0 when none).",
		}),
	}
	reg.MustRegister(m.reconcileLatency, m.driftTotal, m.rolloutOutcomes, m.reconcileTotal, m.oldestInFlight)
	return m
}

// Handler returns the Prometheus scrape handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveTick records how long one reconcile tick took.
func (m *Metrics) ObserveTick(d time.Duration) { m.reconcileLatency.Observe(d.Seconds()) }

// CountReconcile counts one repo's reconcile result in a tick.
func (m *Metrics) CountReconcile(result string) { m.reconcileTotal.WithLabelValues(result).Inc() }

// SetOldestInFlight reports the oldest rollout held mid-deploy, in seconds.
func (m *Metrics) SetOldestInFlight(seconds float64) { m.oldestInFlight.Set(seconds) }

// IncDrift counts a detected drift event.
func (m *Metrics) IncDrift() { m.driftTotal.Inc() }

// IncOutcome counts a rollout outcome (e.g. promoted, rolled-back, failed).
func (m *Metrics) IncOutcome(outcome string) {
	m.rolloutOutcomes.WithLabelValues(outcome).Inc()
}
