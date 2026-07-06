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
)

// MetricsProvider answers a provider-specific query with a single scalar value
// (e.g. PromQL via Prometheus, a Datadog query, a CloudWatch metric). This one
// method is the entire seam — any backend implements it.
type MetricsProvider interface {
	Query(ctx context.Context, query string) (float64, error)
}

// Metric binds a CEL variable name to a provider query.
type Metric struct {
	Name  string // CEL variable name (e.g. "errorRate")
	Query string // provider query string
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
type Result struct {
	Passed       bool
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
	// consecutive-failure streak EXCEEDS FailureLimit, so a FailureLimit at or
	// above the measurement count can never trip — a canary that fails every
	// sample (or whose provider errors every time) would be reported Passed.
	// Reject that impossible-to-fail configuration outright. Count defaults to 1
	// in Run, so mirror that default here.
	count := t.Count
	if count <= 0 {
		count = 1
	}
	if t.FailureLimit >= count {
		return nil, fmt.Errorf("analysis: failureLimit (%d) must be less than count (%d); otherwise the analysis can never fail", t.FailureLimit, count)
	}
	vars := make([]cel.EnvOption, 0, len(t.Metrics)+1)
	for _, m := range t.Metrics {
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
		v, err := a.provider.Query(ctx, m.Query)
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
// consecutive failing measurements exceed FailureLimit.
func (a *Analyzer) Run(ctx context.Context) Result {
	count := a.tmpl.Count
	if count <= 0 {
		count = 1
	}
	var res Result
	consecutiveFail := 0
	for i := 0; i < count; i++ {
		if i > 0 && a.tmpl.Interval > 0 {
			a.sleep(a.tmpl.Interval)
		}
		mm := a.measure(ctx)
		res.Measurements = append(res.Measurements, mm)
		if mm.Err != nil || !mm.Passed {
			consecutiveFail++
			if consecutiveFail > a.tmpl.FailureLimit {
				res.Passed = false
				if mm.Err != nil {
					res.Reason = mm.Err.Error()
				} else {
					res.Reason = fmt.Sprintf("condition %q failed %d consecutive times", a.tmpl.Condition, consecutiveFail)
				}
				return res
			}
			continue
		}
		consecutiveFail = 0
	}
	res.Passed = true
	return res
}

func toAny(m map[string]float64) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
