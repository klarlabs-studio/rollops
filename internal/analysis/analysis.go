// Package analysis is the generic, provider-agnostic metric-based rollout
// analysis seam (TDD Phase 2, decoupled from any specific observability stack).
// A MetricsProvider answers scalar queries; an analysis Template declares named
// metrics plus a CEL success condition over them. The Analyzer measures
// repeatedly during a canary bake and reports pass/fail — a fourth post-deploy
// signal alongside health, smoke, and step error. It is opt-in: with no
// analysis configured, rollouts stay observability-free.
package analysis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/cel-go/cel"

	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// MetricsProvider answers a provider-specific query with a single scalar value
// (e.g. PromQL via Prometheus, a Datadog query, a CloudWatch metric). This one
// method is the entire seam — any backend implements it.
type MetricsProvider interface {
	Query(ctx context.Context, query string) (float64, error)
}

// SeriesSample is one labelled value from a multi-series query result.
type SeriesSample struct {
	Labels map[string]string
	Value  float64
}

// SeriesProvider is an OPTIONAL capability: a MetricsProvider that can return
// every series of a query. Required when Metric.Aggregation is set, so a
// multi-series vector is reduced deliberately rather than by taking Result[0].
type SeriesProvider interface {
	QuerySeries(ctx context.Context, query string) ([]SeriesSample, error)
}

// ValidAggregation names the reductions Metric.Aggregation accepts.
var ValidAggregation = map[string]bool{
	"max": true,
	"min": true,
	"sum": true,
	"any": true, // first series, only when opted in explicitly
}

// Aggregate reduces a multi-series result to one scalar. Unknown aggregations
// and empty inputs are errors (fail closed).
func Aggregate(samples []SeriesSample, agg string) (float64, error) {
	if len(samples) == 0 {
		return 0, fmt.Errorf("analysis: aggregation %q over empty result", agg)
	}
	switch agg {
	case "any":
		return samples[0].Value, nil
	case "max":
		m := samples[0].Value
		for _, s := range samples[1:] {
			if s.Value > m {
				m = s.Value
			}
		}
		return m, nil
	case "min":
		m := samples[0].Value
		for _, s := range samples[1:] {
			if s.Value < m {
				m = s.Value
			}
		}
		return m, nil
	case "sum":
		var sum float64
		for _, s := range samples {
			sum += s.Value
		}
		return sum, nil
	default:
		return 0, fmt.Errorf("analysis: unknown aggregation %q (want max|min|sum|any)", agg)
	}
}

// Metric binds a CEL variable name to a provider query.
type Metric struct {
	Name        string // CEL variable name (e.g. "errorRate")
	Query       string // provider query string
	Aggregation string // "", or max|min|sum|any — required for intentional multi-series vectors
}

// Template declares an analysis run.
type Template struct {
	Metrics      []Metric
	Condition    string        // CEL bool over the metric names; true == healthy
	Interval     time.Duration // wait between measurements
	Count        int           // number of measurements (defaults to 1)
	FailureLimit int           // consecutive failing measurements tolerated before failing the run
}

// Measurement is one sampling of all metrics + the condition verdict.
type Measurement struct {
	Values map[string]float64
	Passed bool
	Err    error
}

// Result is the outcome of an analysis run.
//
// The verdict is not a bool on purpose (§11.3). A canary whose error rate
// breached and a canary whose metrics backend was unreachable are different
// observations, and only the first is evidence about the deploy — collapsing
// them loses the distinction the promotion decision needs.
type Result struct {
	Verdict      verifyv1.Verdict
	Measurements []Measurement
	Reason       string
}

// Analyzer evaluates a Template against a provider.
type Analyzer struct {
	provider MetricsProvider
	tmpl     Template
	prog     cel.Program
	sleep    func(time.Duration)
}

// New compiles the template's CEL condition against its metric names.
func New(provider MetricsProvider, t Template) (*Analyzer, error) {
	if t.Condition == "" {
		return nil, fmt.Errorf("analysis: condition is required")
	}
	if len(t.Metrics) == 0 {
		return nil, fmt.Errorf("analysis: at least one metric is required")
	}
	// Fail CLOSED at construction: Run fails the analysis only once the
	// consecutive-breach streak EXCEEDS FailureLimit, so a FailureLimit at or
	// above the measurement count can never trip — a canary that breaches every
	// sample would still be reported as a pass. Reject that impossible-to-fail
	// configuration outright. Count defaults to 1 in Run, so mirror that default
	// here.
	count := t.Count
	if count <= 0 {
		count = 1
	}
	if t.FailureLimit >= count {
		return nil, fmt.Errorf("analysis: failureLimit (%d) must be less than count (%d); otherwise the analysis can never fail", t.FailureLimit, count)
	}
	vars := make([]cel.EnvOption, 0, len(t.Metrics)+1)
	for _, m := range t.Metrics {
		if m.Aggregation != "" && !ValidAggregation[m.Aggregation] {
			return nil, fmt.Errorf("analysis: metric %q: unknown aggregation %q (want max|min|sum|any)", m.Name, m.Aggregation)
		}
		vars = append(vars, cel.Variable(m.Name, cel.DoubleType))
	}
	// Allow `p99 < 500` (double vs int literal) — friendlier conditions.
	vars = append(vars, cel.CrossTypeNumericComparisons(true))
	env, err := cel.NewEnv(vars...)
	if err != nil {
		return nil, fmt.Errorf("analysis: build env: %w", err)
	}
	ast, iss := env.Compile(t.Condition)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("analysis: condition %q: %w", t.Condition, iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("analysis: condition %q must evaluate to bool", t.Condition)
	}
	prog, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("analysis: program: %w", err)
	}
	return &Analyzer{provider: provider, tmpl: t, prog: prog, sleep: time.Sleep}, nil
}

// measure samples every metric once and evaluates the condition.
func (a *Analyzer) measure(ctx context.Context) Measurement {
	vals := make(map[string]float64, len(a.tmpl.Metrics))
	for _, m := range a.tmpl.Metrics {
		v, err := a.queryMetric(ctx, m)
		if err != nil {
			return Measurement{Values: vals, Err: fmt.Errorf("metric %q: %w", m.Name, err)}
		}
		vals[m.Name] = v
	}
	out, _, err := a.prog.Eval(toAny(vals))
	if err != nil {
		return Measurement{Values: vals, Err: err}
	}
	passed, _ := out.Value().(bool)
	return Measurement{Values: vals, Passed: passed}
}

// Run measures Count times (waiting Interval between), failing the run once
// consecutive breaching measurements exceed FailureLimit.
//
// A measurement the provider could not answer is tracked separately from one
// whose condition breached, because they are not the same claim. FailureLimit
// tolerates a flaky canary, and reusing it to tolerate a flaky backend meant a
// run could fall out of the loop having measured nothing and report a pass —
// §11.3's "inconclusive MUST NOT silently become pass". An unanswered sample
// now taints the whole run: the tolerance window still stops a dead backend
// from stalling the bake, but the verdict it reaches is inconclusive.
func (a *Analyzer) Run(ctx context.Context) Result {
	count := a.tmpl.Count
	if count <= 0 {
		count = 1
	}
	var res Result
	breaches, unanswered := 0, 0
	var lastErr error
	for i := 0; i < count; i++ {
		if v := verifyv1.Interrupted(ctx); v != "" {
			res.Verdict, res.Reason = v, ctx.Err().Error()
			return res
		}
		if i > 0 && a.tmpl.Interval > 0 {
			a.sleep(a.tmpl.Interval)
		}
		mm := a.measure(ctx)
		res.Measurements = append(res.Measurements, mm)
		switch {
		case mm.Err != nil:
			// The breach streak is left intact: a sample nobody could read
			// neither confirms nor clears the one before it, and resetting
			// would let a flaky backend buy a breaching canary extra rope.
			lastErr = mm.Err
			unanswered++
			if unanswered > a.tmpl.FailureLimit {
				res.Verdict, res.Reason = verifyv1.VerdictInconclusive, mm.Err.Error()
				return res
			}
		case !mm.Passed:
			breaches++
			if breaches > a.tmpl.FailureLimit {
				res.Verdict = verifyv1.VerdictFail
				res.Reason = fmt.Sprintf("condition %q failed %d consecutive times", a.tmpl.Condition, breaches)
				return res
			}
		default:
			breaches = 0
		}
	}
	if unanswered > 0 {
		res.Verdict, res.Reason = verifyv1.VerdictInconclusive, lastErr.Error()
		return res
	}
	res.Verdict = verifyv1.VerdictPass
	return res
}

// queryMetric resolves one metric to a scalar. With Aggregation set, the
// provider must expose SeriesProvider so every series is visible; without it,
// MetricsProvider.Query is used (and Prometheus rejects multi-series there).
func (a *Analyzer) queryMetric(ctx context.Context, m Metric) (float64, error) {
	if m.Aggregation == "" {
		return a.provider.Query(ctx, m.Query)
	}
	sp, ok := a.provider.(SeriesProvider)
	if !ok {
		return 0, fmt.Errorf("aggregation %q requires a series-capable metrics provider", m.Aggregation)
	}
	samples, err := sp.QuerySeries(ctx, m.Query)
	if err != nil {
		return 0, err
	}
	return Aggregate(samples, m.Aggregation)
}

func toAny(m map[string]float64) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
