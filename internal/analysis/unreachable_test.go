package analysis

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// errAfter answers the first n queries per metric, then errors as an
// unreachable Prometheus does.
type errAfter struct {
	n     int
	calls map[string]int
}

func (p *errAfter) Query(_ context.Context, q string) (float64, error) {
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[q]++
	if p.calls[q] > p.n {
		return 0, errors.New("dial tcp prometheus:9090: connect: connection refused")
	}
	return 0.01, nil
}

// Errors within the failure limit at the end of the run passed the analysis
// and promoted the rollout, though the last thing rollops knew about the
// canary was that it could not measure it.
func TestAnalyzer_TrailingErrorsFail(t *testing.T) {
	a := newAnalyzer(t, &errAfter{n: 1}, Template{
		Metrics: []Metric{{Name: "errorRate", Query: "err"}}, Condition: "errorRate < 0.05", Count: 3, FailureLimit: 2,
	})
	r := a.Run(context.Background())
	if r.Passed {
		t.Fatal("analysis passed although its last measurements could not be taken")
	}
	if !strings.Contains(r.Reason, "connection refused") {
		t.Errorf("reason = %q; want the measurement error", r.Reason)
	}
}

func TestAnalyzer_NoSuccessfulMeasurementFails(t *testing.T) {
	a := newAnalyzer(t, &errAfter{n: 0}, Template{
		Metrics: []Metric{{Name: "errorRate", Query: "err"}}, Condition: "errorRate < 0.05", Count: 3, FailureLimit: 2,
	})
	if r := a.Run(context.Background()); r.Passed {
		t.Fatal("analysis passed without a single measurement")
	}
}

// An error in the middle that recovers is still tolerated.
func TestAnalyzer_RecoveredErrorPasses(t *testing.T) {
	p := &flaky{failAt: 1}
	a := newAnalyzer(t, p, Template{
		Metrics: []Metric{{Name: "errorRate", Query: "err"}}, Condition: "errorRate < 0.05", Count: 3, FailureLimit: 1,
	})
	if r := a.Run(context.Background()); !r.Passed {
		t.Fatalf("a recovered transient error must be tolerated; reason=%q", r.Reason)
	}
}

// flaky errors on measurement failAt (0-based) for every metric.
type flaky struct {
	failAt int
	calls  map[string]int
}

func (p *flaky) Query(_ context.Context, q string) (float64, error) {
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	i := p.calls[q]
	p.calls[q]++
	if i == p.failAt {
		return 0, errors.New("timeout")
	}
	return 0.01, nil
}
