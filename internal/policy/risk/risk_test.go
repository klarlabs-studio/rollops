package risk_test

import (
	"testing"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/policy/risk"
)

// op returns one ordinary operation: reversible, nothing secret, one field.
func op(id string) plan.PlannedOperation {
	return plan.PlannedOperation{
		ID:         plan.OperationID(id),
		Target:     "web",
		Kind:       plan.OperationApply,
		Summary:    "roll out " + id,
		Reversible: true,
		Diff:       plan.Diff{Changes: []plan.Change{{Path: "image", From: "v1", To: "v2"}}},
	}
}

func ops(n int) []plan.PlannedOperation {
	out := make([]plan.PlannedOperation, 0, n)
	for i := range n {
		out = append(out, op(string(rune('a'+i%26))+string(rune('0'+i/26))))
	}
	return out
}

// quiet is the least alarming thing that can be deployed: a gradual rollout of
// one undoable change to somewhere nobody depends on.
func quiet() risk.Signals {
	return risk.Signals{
		Environment: environment.KindDevelopment,
		Strategy:    deployment.StrategyCanary,
		Operations:  []plan.PlannedOperation{op("a")},
	}
}

func codes(a policy.RiskAssessment) []string {
	out := make([]string, 0, len(a.Factors))
	for _, f := range a.Factors {
		out = append(out, f.Code)
	}
	return out
}

func names(t *testing.T, a policy.RiskAssessment, code string) {
	t.Helper()
	for _, f := range a.Factors {
		if f.Code == code {
			if f.Message == "" {
				t.Errorf("factor %q explains itself with nothing", code)
			}
			return
		}
	}
	t.Errorf("assessment names %v, want it to name %q", codes(a), code)
}

func TestAQuietChangeIsLowRiskAndHasNothingToReport(t *testing.T) {
	a := risk.Assess(quiet())
	if a.Level != policy.RiskLow {
		t.Errorf("level %s, want %s", a.Level, policy.RiskLow)
	}
	if len(a.Factors) != 0 {
		t.Errorf("a change that raised nothing reported %v", codes(a))
	}
}

// Section 12.4 asks for explainable factors. The rule here is stricter than
// "some explanation": every signal that pushed the score up is named, so a
// factor's absence means that signal contributed nothing rather than that
// nobody looked.
func TestProductionIsNamedAsWhatRaisedTheRisk(t *testing.T) {
	s := quiet()
	s.Environment = environment.KindProduction
	a := risk.Assess(s)
	if a.Level == policy.RiskLow {
		t.Errorf("a production deploy scored %v, which bands as %s", a.Score, a.Level)
	}
	names(t, a, risk.FactorEnvironment)
}

// A custom environment is one whose purpose the operator declined to state.
// Reading that as harmless would let an unlabelled production environment
// through the gate that its kind exists to enforce.
func TestAnUnstatedEnvironmentIsNotTreatedAsHarmless(t *testing.T) {
	s, c := quiet(), quiet()
	s.Environment = environment.KindProduction
	c.Environment = environment.KindCustom
	if risk.Assess(c).Score < risk.Assess(s).Score {
		t.Errorf("custom scored %v, production %v", risk.Assess(c).Score, risk.Assess(s).Score)
	}
}

func TestEnvironmentsAreOrderedByHowMuchIsAtStake(t *testing.T) {
	score := func(k environment.Kind) float64 {
		s := quiet()
		s.Environment = k
		return risk.Assess(s).Score
	}
	ordered := []environment.Kind{
		environment.KindDevelopment,
		environment.KindPreview,
		environment.KindStaging,
		environment.KindProduction,
	}
	for i := 1; i < len(ordered); i++ {
		if score(ordered[i-1]) >= score(ordered[i]) {
			t.Errorf("%s scored %v, %s scored %v",
				ordered[i-1], score(ordered[i-1]), ordered[i], score(ordered[i]))
		}
	}
}

// A change with no way back is the one a reviewer most needs to be told about:
// every other gate can be undone by deploying again.
func TestAnOperationThatCannotBeUndoneIsNamed(t *testing.T) {
	s := quiet()
	s.Operations = []plan.PlannedOperation{op("a"), op("b")}
	s.Operations[1].Reversible = false
	a := risk.Assess(s)
	names(t, a, risk.FactorIrreversible)
	if a.Score <= risk.Assess(quiet()).Score {
		t.Error("an irreversible operation did not raise the score")
	}
}

func TestAChangeToASecretIsNamed(t *testing.T) {
	s := quiet()
	s.Operations[0].Diff.Changes = append(s.Operations[0].Diff.Changes,
		plan.Change{Path: "database_url", Sensitive: true})
	a := risk.Assess(s)
	names(t, a, risk.FactorSensitive)
	if a.Score <= risk.Assess(quiet()).Score {
		t.Error("a sensitive change did not raise the score")
	}
}

// One operation is not a blast radius, so the count only starts counting past
// the first: otherwise every plan that exists would carry the factor and it
// would stop meaning anything.
func TestASingleOperationIsNotABlastRadius(t *testing.T) {
	for _, c := range codes(risk.Assess(quiet())) {
		if c == risk.FactorBlastRadius {
			t.Error("one operation was reported as blast radius")
		}
	}
}

func TestAWideChangeIsNamedAndScoresHigherThanANarrowOne(t *testing.T) {
	wide, narrow := quiet(), quiet()
	wide.Operations = ops(12)
	narrow.Operations = ops(2)
	a := risk.Assess(wide)
	names(t, a, risk.FactorBlastRadius)
	if a.Score <= risk.Assess(narrow).Score {
		t.Errorf("12 operations scored %v, 2 scored %v", a.Score, risk.Assess(narrow).Score)
	}
}

func TestStrategiesAreOrderedByHowMuchMovesAtOnce(t *testing.T) {
	score := func(st deployment.Strategy) float64 {
		s := quiet()
		s.Strategy = st
		return risk.Assess(s).Score
	}
	ordered := []deployment.Strategy{
		deployment.StrategyCanary,
		deployment.StrategyRolling,
		deployment.StrategyBlueGreen,
		deployment.StrategyRecreate,
	}
	for i := 1; i < len(ordered); i++ {
		if score(ordered[i-1]) >= score(ordered[i]) {
			t.Errorf("%s scored %v, %s scored %v",
				ordered[i-1], score(ordered[i-1]), ordered[i], score(ordered[i]))
		}
	}
	names(t, risk.Assess(risk.Signals{
		Environment: environment.KindDevelopment,
		Strategy:    deployment.StrategyRecreate,
		Operations:  []plan.PlannedOperation{op("a")},
	}), risk.FactorStrategy)
}

func TestTheWorstOfEverythingIsCritical(t *testing.T) {
	s := risk.Signals{
		Environment: environment.KindProduction,
		Strategy:    deployment.StrategyRecreate,
		Operations:  ops(20),
	}
	s.Operations[0].Reversible = false
	s.Operations[1].Diff.Changes = []plan.Change{{Path: "token", Sensitive: true}}
	a := risk.Assess(s)
	if a.Level != policy.RiskCritical {
		t.Errorf("level %s at score %v, want %s", a.Level, a.Score, policy.RiskCritical)
	}
}

// The assessment is fed straight into a Decision, which refuses a score outside
// [0,1] and a raised level with nothing to point at. Assess must never produce
// either, whatever it is handed.
func TestEveryAssessmentIsAUsablePolicyInput(t *testing.T) {
	kinds := []environment.Kind{
		environment.KindDevelopment, environment.KindPreview, environment.KindStaging,
		environment.KindProduction, environment.KindCustom, environment.Kind("invented"),
	}
	strategies := []deployment.Strategy{
		deployment.StrategyCanary, deployment.StrategyRolling,
		deployment.StrategyBlueGreen, deployment.StrategyRecreate, deployment.Strategy(""),
	}
	for _, k := range kinds {
		for _, st := range strategies {
			for _, n := range []int{0, 1, 7, 40} {
				s := risk.Signals{Environment: k, Strategy: st, Operations: ops(n)}
				if n > 0 {
					s.Operations[0].Reversible = false
				}
				a := risk.Assess(s)
				if err := a.Validate(); err != nil {
					t.Fatalf("%s/%s/%d operations: %v", k, st, n, err)
				}
			}
		}
	}
}

// Nothing to deploy is nothing to be afraid of, and a plan with no operations
// is refused before it reaches policy anyway.
func TestNothingToDoIsNoRisk(t *testing.T) {
	a := risk.Assess(risk.Signals{
		Environment: environment.KindDevelopment,
		Strategy:    deployment.StrategyCanary,
	})
	if a.Level != policy.RiskLow {
		t.Errorf("level %s, want %s", a.Level, policy.RiskLow)
	}
}
