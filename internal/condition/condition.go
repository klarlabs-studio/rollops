// Package condition compiles and evaluates the CEL expressions embedded in
// Rollops config — risk-gate conditions (risk.sensitive), rollback triggers
// (rollback.trigger), and promotion criteria. CEL is the one conditional-logic
// surface; there is no bespoke DSL, and relicta's governance DSL stays separate.
//
// Expressions see a fixed, typed set of rollout-decision variables (Input).
// Compile is strict: unknown variables, syntax errors, and non-bool results are
// rejected at compile time so malformed conditions fail loudly during config
// validation rather than silently at rollout time.
package condition

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// Input is the typed activation a condition is evaluated against — the
// observability-free signals available when gating or rolling back a change.
type Input struct {
	Criticality    string  // low | medium | high | critical
	Environment    string  // dev | staging | prod | <custom>
	ChangeType     string  // config | code | schema
	BlastRadius    int     // count of downstream dependents
	Strategy       string  // rolling | canary | blue-green
	Score          float64 // computed risk score, 0..1
	RecentFailures int     // rollback count inside the configured lookback
	HistoryRisk    float64 // normalized recent failure risk, 0..1
}

func (in Input) activation() map[string]any {
	return map[string]any{
		"criticality":    in.Criticality,
		"environment":    in.Environment,
		"changeType":     in.ChangeType,
		"blastRadius":    int64(in.BlastRadius),
		"strategy":       in.Strategy,
		"score":          in.Score,
		"recentFailures": int64(in.RecentFailures),
		"historyRisk":    in.HistoryRisk,
	}
}

// env is the shared CEL environment declaring the variable schema. Built once;
// CEL environments are safe for concurrent use.
var env = mustEnv()

func mustEnv() *cel.Env {
	e, err := cel.NewEnv(
		cel.Variable("criticality", cel.StringType),
		cel.Variable("environment", cel.StringType),
		cel.Variable("changeType", cel.StringType),
		cel.Variable("blastRadius", cel.IntType),
		cel.Variable("strategy", cel.StringType),
		cel.Variable("score", cel.DoubleType),
		cel.Variable("recentFailures", cel.IntType),
		cel.Variable("historyRisk", cel.DoubleType),
	)
	if err != nil {
		panic(fmt.Sprintf("condition: build CEL env: %v", err))
	}
	return e
}

// Program is a compiled, reusable condition.
type Program struct {
	src string
	prg cel.Program
}

// Compile type-checks an expression against the variable schema and requires it
// to yield a bool. The returned Program is safe to reuse and to evaluate
// concurrently.
func Compile(expr string) (Program, error) {
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return Program{}, fmt.Errorf("condition %q: %w", expr, iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return Program{}, fmt.Errorf("condition %q: must evaluate to bool, got %s", expr, ast.OutputType())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return Program{}, fmt.Errorf("condition %q: build program: %w", expr, err)
	}
	return Program{src: expr, prg: prg}, nil
}

// EvalBool evaluates the compiled condition against in.
func (p Program) EvalBool(in Input) (bool, error) {
	out, _, err := p.prg.Eval(in.activation())
	if err != nil {
		return false, fmt.Errorf("condition %q: eval: %w", p.src, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("condition %q: non-bool result %v", p.src, out.Value())
	}
	return b, nil
}

// Eval is the one-shot convenience: compile then evaluate. Hot paths should
// Compile once and reuse the Program.
func Eval(expr string, in Input) (bool, error) {
	p, err := Compile(expr)
	if err != nil {
		return false, err
	}
	return p.EvalBool(in)
}

// Check reports whether expr is a well-formed bool condition, for config
// validation. It discards the compiled program.
func Check(expr string) error {
	_, err := Compile(expr)
	return err
}
