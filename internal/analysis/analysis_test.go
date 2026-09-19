package analysis

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// scriptProvider returns scripted values per query; each query string advances
// independently so measurement N reads series[q][N].
type scriptProvider struct {
	series map[string][]float64
	calls  map[string]int
}

func (p *scriptProvider) Query(_ context.Context, q string) (float64, error) {
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	vals := p.series[q]
	idx := p.calls[q]
	p.calls[q]++
	if idx >= len(vals) {
		idx = len(vals) - 1
	}
	return vals[idx], nil
}

func tmpl(cond string, count, limit int) Template {
	return Template{
		Metrics:      []Metric{{Name: "errorRate", Query: "err"}, {Name: "p99", Query: "lat"}},
		Condition:    cond,
		Count:        count,
		FailureLimit: limit,
	}
}

func newAnalyzer(t *testing.T, p MetricsProvider, tm Template) *Analyzer {
	t.Helper()
	a, err := New(p, tm)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.sleep = func(time.Duration) {} // no real waits in tests
	return a
}

func TestAnalyzer_PassesWhenHealthy(t *testing.T) {
	p := &scriptProvider{series: map[string][]float64{"err": {0.01, 0.02, 0.01}, "lat": {120, 130, 110}}}
	a := newAnalyzer(t, p, tmpl("errorRate < 0.05 && p99 < 500", 3, 0))
	r := a.Run(context.Background())
	if r.Verdict != verifyv1.VerdictPass {
		t.Fatalf("verdict = %q; reason=%q measurements=%d", r.Verdict, r.Reason, len(r.Measurements))
	}
}

func TestAnalyzer_FailsWhenConditionBreaches(t *testing.T) {
	// errorRate spikes above threshold every measurement → exceeds FailureLimit.
	p := &scriptProvider{series: map[string][]float64{"err": {0.2, 0.2, 0.2}, "lat": {120, 120, 120}}}
	a := newAnalyzer(t, p, tmpl("errorRate < 0.05 && p99 < 500", 3, 1))
	r := a.Run(context.Background())
	if r.Verdict != verifyv1.VerdictFail {
		t.Fatalf("verdict = %q, want a fail when the error rate breaches threshold", r.Verdict)
	}
	if !strings.Contains(r.Reason, "failed") {
		t.Errorf("reason = %q", r.Reason)
	}
}

func TestAnalyzer_ToleratesTransientWithinLimit(t *testing.T) {
	// One bad sample then recovery; FailureLimit=1 tolerates a single failure.
	p := &scriptProvider{series: map[string][]float64{"err": {0.2, 0.01, 0.01}, "lat": {120, 120, 120}}}
	a := newAnalyzer(t, p, tmpl("errorRate < 0.05", 3, 1))
	if r := a.Run(context.Background()); r.Verdict != verifyv1.VerdictPass {
		t.Fatalf("single transient breach should be tolerated; verdict=%q reason=%q", r.Verdict, r.Reason)
	}
}

func TestNew_RejectsImpossibleFailureLimit(t *testing.T) {
	// FailureLimit >= Count can never trip (Run fails only when the streak
	// EXCEEDS the limit), so the analysis could never fail — a canary that fails
	// every sample would be reported Passed. Reject at construction (fail closed).
	if _, err := New(&scriptProvider{}, tmpl("errorRate < 1", 3, 3)); err == nil {
		t.Error("failureLimit == count must be rejected")
	}
	if _, err := New(&scriptProvider{}, tmpl("errorRate < 1", 2, 5)); err == nil {
		t.Error("failureLimit > count must be rejected")
	}
	// Count defaults to 1 in Run, so a FailureLimit of 1 with an unset Count is
	// also impossible-to-fail and must be rejected.
	if _, err := New(&scriptProvider{}, Template{
		Metrics: []Metric{{Name: "errorRate", Query: "err"}}, Condition: "errorRate < 1", FailureLimit: 1,
	}); err == nil {
		t.Error("failureLimit 1 with default count 1 must be rejected")
	}
	// The boundary just below the count is valid.
	if _, err := New(&scriptProvider{}, tmpl("errorRate < 1", 3, 2)); err != nil {
		t.Errorf("failureLimit < count must be accepted: %v", err)
	}
}

func TestNew_RejectsBadCondition(t *testing.T) {
	if _, err := New(&scriptProvider{}, Template{Metrics: []Metric{{Name: "x", Query: "q"}}, Condition: "x <"}); err == nil {
		t.Error("malformed condition should error")
	}
	if _, err := New(&scriptProvider{}, Template{Metrics: []Metric{{Name: "x", Query: "q"}}, Condition: "x"}); err == nil {
		t.Error("non-bool condition should error")
	}
	if _, err := New(&scriptProvider{}, Template{Condition: "true"}); err == nil {
		t.Error("no metrics should error")
	}
}

type rt struct{ body string }

func (r rt) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(r.body)), Header: make(http.Header)}, nil
}

func TestPrometheus_ParsesVector(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"api"},"value":[1700000000,"0.037"]}]}}`
	p := Prometheus{Addr: "http://prom", Client: rt{body: body}}
	v, err := p.Query(context.Background(), "rate(err[1m])")
	if err != nil {
		t.Fatal(err)
	}
	if v != 0.037 {
		t.Errorf("value = %v, want 0.037", v)
	}
}

func TestPrometheus_NoData(t *testing.T) {
	p := Prometheus{Addr: "http://prom", Client: rt{body: `{"status":"success","data":{"resultType":"vector","result":[]}}`}}
	if _, err := p.Query(context.Background(), "q"); err == nil {
		t.Error("empty result should error")
	}
}

// #175: a multi-series vector must not silently gate on Result[0]. One healthy
// series plus one fully-broken series would otherwise promote.
func TestPrometheus_RejectsMultiSeries(t *testing.T) {
	const twoSeries = `{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"ecosystem":"npm"},"value":[1,"0"]},
{"metric":{"ecosystem":"Packagist"},"value":[1,"1"]}]}}`
	p := Prometheus{Addr: "http://prom", Client: rt{body: twoSeries}}
	_, err := p.Query(context.Background(), "sum by (ecosystem) (rate(suppressed[5m]))")
	if err == nil {
		t.Fatal("multi-series vector must error, not return Result[0]")
	}
	if !strings.Contains(err.Error(), "2 series") {
		t.Errorf("error should name the series count: %v", err)
	}
	if !strings.Contains(err.Error(), "aggregation") {
		t.Errorf("error should tell the author how to opt in: %v", err)
	}
	if !strings.Contains(err.Error(), "ecosystem") {
		t.Errorf("error should name a label set so the query can be narrowed: %v", err)
	}
}

func TestPrometheus_Aggregates(t *testing.T) {
	const twoSeries = `{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"ecosystem":"npm"},"value":[1,"0"]},
{"metric":{"ecosystem":"Packagist"},"value":[1,"1"]}]}}`
	p := Prometheus{Addr: "http://prom", Client: rt{body: twoSeries}}
	for _, tc := range []struct {
		agg  string
		want float64
	}{
		{"max", 1},
		{"min", 0},
		{"sum", 1},
		{"any", 0}, // first series, only when aggregation is explicit
	} {
		t.Run(tc.agg, func(t *testing.T) {
			samples, err := p.QuerySeries(context.Background(), "q")
			if err != nil {
				t.Fatal(err)
			}
			got, err := Aggregate(samples, tc.agg)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Aggregate(%s) = %v, want %v", tc.agg, got, tc.want)
			}
		})
	}
}

func TestNew_RejectsUnknownAggregation(t *testing.T) {
	_, err := New(&scriptProvider{}, Template{
		Metrics:   []Metric{{Name: "x", Query: "q", Aggregation: "median"}},
		Condition: "x < 1",
	})
	if err == nil {
		t.Fatal("unknown aggregation must be rejected")
	}
	if !strings.Contains(err.Error(), "aggregation") {
		t.Errorf("error = %v", err)
	}
}

func TestAnalyzer_AggregatesMultiSeries(t *testing.T) {
	// max of {0, 1} is 1 → condition suppressionRate == 0 fails (fail closed).
	const twoSeries = `{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"ecosystem":"npm"},"value":[1,"0"]},
{"metric":{"ecosystem":"Packagist"},"value":[1,"1"]}]}}`
	a := newAnalyzer(t, Prometheus{Addr: "http://prom", Client: rt{body: twoSeries}},
		Template{
			Metrics:   []Metric{{Name: "suppressionRate", Query: "sum by (ecosystem) (rate(suppressed[5m]))", Aggregation: "max"}},
			Condition: "suppressionRate == 0.0",
			Count:     1,
		})
	r := a.Run(context.Background())
	if r.Verdict != verifyv1.VerdictFail {
		t.Fatalf("verdict = %q: max aggregation must surface the broken series", r.Verdict)
	}
}

func TestAnalyzer_AggregationRequiresSeriesProvider(t *testing.T) {
	// scriptProvider has no QuerySeries — aggregation must fail closed, not
	// silently fall back to a scalar Query that cannot see other series.
	a := newAnalyzer(t, &scriptProvider{series: map[string][]float64{"q": {0}}},
		Template{
			Metrics:   []Metric{{Name: "x", Query: "q", Aggregation: "max"}},
			Condition: "x == 0.0",
			Count:     1,
		})
	r := a.Run(context.Background())
	// Inconclusive rather than fail: a provider that cannot answer the query
	// has said nothing about the canary, which is exactly the distinction the
	// verdict vocabulary exists to keep.
	if r.Verdict != verifyv1.VerdictInconclusive {
		t.Fatalf("verdict = %q, want %q", r.Verdict, verifyv1.VerdictInconclusive)
	}
	if !strings.Contains(r.Reason, "aggregation") {
		t.Errorf("reason = %q", r.Reason)
	}
}

// End-to-end: Prometheus provider feeding the analyzer.
func TestAnalyzer_WithPrometheus(t *testing.T) {
	body := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"0.01"]}]}}`
	a := newAnalyzer(t, Prometheus{Addr: "http://prom", Client: rt{body: body}},
		Template{Metrics: []Metric{{Name: "errorRate", Query: "rate(err[1m])"}}, Condition: "errorRate < 0.05", Count: 2})
	if r := a.Run(context.Background()); r.Verdict != verifyv1.VerdictPass {
		t.Fatalf("prometheus-fed analysis verdict = %q; reason=%q", r.Verdict, r.Reason)
	}
}

// flakyProvider answers, or refuses, on a per-measurement script. It is the
// backend being down rather than the canary being bad.
type flakyProvider struct {
	up   []bool // whether the backend answers measurement N
	n    int
	last float64
}

func (p *flakyProvider) Query(_ context.Context, _ string) (float64, error) {
	i := p.n
	p.n++
	if i >= len(p.up) {
		i = len(p.up) - 1
	}
	if !p.up[i] {
		return 0, errors.New("connection refused")
	}
	return p.last, nil
}

// TestABackendThatNeverAnsweredIsNotAPass is §11.3's MUST measured where it
// actually bites. A provider error and a breaching metric are not the same
// observation: the first means nobody looked, and a run that tolerated enough
// of them to fall out of the loop used to report a pass the backend never gave.
func TestABackendThatNeverAnsweredIsNotAPass(t *testing.T) {
	// The query is answered on the middle sample only, so neither error is
	// consecutive enough to trip FailureLimit — the tolerance window is exactly
	// what used to carry an unmeasured run to the end.
	p := &flakyProvider{up: []bool{false, true, false}, last: 0.01}
	a := newAnalyzer(t, p, tmpl("errorRate < 0.05 && p99 < 500", 3, 1))

	r := a.Run(context.Background())
	if r.Verdict == verifyv1.VerdictPass {
		t.Fatal("a run whose metrics backend refused two of three samples reported a pass")
	}
	if r.Verdict != verifyv1.VerdictInconclusive {
		t.Errorf("verdict = %q, want %q", r.Verdict, verifyv1.VerdictInconclusive)
	}
	if !strings.Contains(r.Reason, "connection refused") {
		t.Errorf("reason = %q, want it to name what went unanswered", r.Reason)
	}
}

// TestABreachingMetricIsStillAFail keeps the two apart from the other side: an
// unreachable backend must not launder a real breach into "we could not tell".
func TestABreachingMetricIsStillAFail(t *testing.T) {
	p := &scriptProvider{series: map[string][]float64{"err": {0.2, 0.2, 0.2}, "lat": {120, 120, 120}}}
	a := newAnalyzer(t, p, tmpl("errorRate < 0.05", 3, 1))
	if r := a.Run(context.Background()); r.Verdict != verifyv1.VerdictFail {
		t.Errorf("verdict = %q, want %q", r.Verdict, verifyv1.VerdictFail)
	}
}

// TestAnAbandonedRunSaysSo: a caller who walked away gets that answer rather
// than the failure their own cancellation caused.
func TestAnAbandonedRunSaysSo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &scriptProvider{series: map[string][]float64{"err": {0.01}, "lat": {120}}}
	a := newAnalyzer(t, p, tmpl("errorRate < 0.05", 3, 1))
	if r := a.Run(ctx); r.Verdict != verifyv1.VerdictCancelled {
		t.Errorf("verdict = %q, want %q", r.Verdict, verifyv1.VerdictCancelled)
	}
}
