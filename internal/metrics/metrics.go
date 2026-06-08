// Package metrics is Rolloffs' self-observability: the daemon exposes its own
// health and Prometheus-style metrics (reconcile latency, drift count, rollout
// outcomes) so operators can monitor Rolloffs itself — independent of the
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
}

// New builds the metric set on a fresh registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		reconcileLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rolloffs_reconcile_latency_seconds",
			Help:    "Time spent in a single reconcile.",
			Buckets: prometheus.DefBuckets,
		}),
		driftTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rolloffs_drift_detected_total",
			Help: "Number of drift events detected.",
		}),
		rolloutOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rolloffs_rollout_outcomes_total",
			Help: "Rollout outcomes by result.",
		}, []string{"outcome"}),
		reconcileTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rolloffs_reconcile_total",
			Help: "Reconcile runs by result (insync, reconciled, error).",
		}, []string{"result"}),
	}
	reg.MustRegister(m.reconcileLatency, m.driftTotal, m.rolloutOutcomes, m.reconcileTotal)
	return m
}

// Handler returns the Prometheus scrape handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ObserveReconcile records a reconcile's duration and result.
func (m *Metrics) ObserveReconcile(d time.Duration, result string) {
	m.reconcileLatency.Observe(d.Seconds())
	m.reconcileTotal.WithLabelValues(result).Inc()
}

// IncDrift counts a detected drift event.
func (m *Metrics) IncDrift() { m.driftTotal.Inc() }

// IncOutcome counts a rollout outcome (e.g. promoted, rolled-back, failed).
func (m *Metrics) IncOutcome(outcome string) {
	m.rolloutOutcomes.WithLabelValues(outcome).Inc()
}
