package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rr.Code)
	}
	return rr.Body.String()
}

func TestMetrics_RecordsAndExposes(t *testing.T) {
	m := New()
	m.IncDrift()
	m.IncDrift()
	m.IncOutcome("promoted")
	m.IncOutcome("rolled-back")
	m.ObserveReconcile(50*time.Millisecond, "reconciled")

	out := scrape(t, m)
	for _, want := range []string{
		"rollops_drift_detected_total 2",
		`rollops_rollout_outcomes_total{outcome="promoted"} 1`,
		`rollops_rollout_outcomes_total{outcome="rolled-back"} 1`,
		`rollops_reconcile_total{result="reconciled"} 1`,
		"rollops_reconcile_latency_seconds_count 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, out)
		}
	}
}
