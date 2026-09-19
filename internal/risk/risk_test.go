package risk

import (
	"strings"
	"testing"

	policyv1 "go.klarlabs.de/rollops/pkg/policy/v1"
)

func TestScore_MonotonicAndBounded(t *testing.T) {
	w := DefaultWeights()
	low := Score(Signals{Criticality: "low", Environment: "dev", ChangeType: "config", BlastRadius: 0, Strategy: "canary"}, w)
	high := Score(Signals{Criticality: "critical", Environment: "prod", ChangeType: "schema", BlastRadius: 20, Strategy: "blue-green"}, w)
	if low != 0 {
		t.Errorf("lowest-risk score = %v, want 0", low)
	}
	if high != 1 {
		t.Errorf("highest-risk score = %v, want 1", high)
	}
	mid := Score(Signals{Criticality: "high", Environment: "staging", ChangeType: "code", BlastRadius: 5, Strategy: "rolling"}, w)
	if mid <= low || mid >= high {
		t.Errorf("mid score %v should sit strictly between %v and %v", mid, low, high)
	}
}

func TestScore_BlastRadiusSaturates(t *testing.T) {
	w := DefaultWeights()
	a := Score(Signals{BlastRadius: 10}, w)
	b := Score(Signals{BlastRadius: 1000}, w)
	if a != b {
		t.Errorf("blast radius should saturate at MaxBlastRadius: %v != %v", a, b)
	}
}

func TestScore_HistoricalFailuresAreOptIn(t *testing.T) {
	base := Signals{Criticality: "low", Environment: "dev", ChangeType: "config", BlastRadius: 0, Strategy: "canary"}
	if got := Score(Signals{RecentFailures: 3}, DefaultWeights()); got != 0 {
		t.Fatalf("default historical weight should be inert, got score %v", got)
	}

	w := DefaultWeights()
	w.History = 0.25
	w.MaxRecentFailures = 2
	none := Score(base, w)
	withFailures := Score(Signals{RecentFailures: 3}, w)
	if withFailures <= none {
		t.Fatalf("historical failures should increase risk: none=%v withFailures=%v", none, withFailures)
	}
}

// needsApproval reports what the gate's decision means for the caller: a
// requirement nobody has satisfied yet.
func needsApproval(t *testing.T, d policyv1.PolicyDecision) bool {
	t.Helper()
	return policyv1.Permits(d, nil) != nil
}

func TestGate_AutoProceedBelowThreshold(t *testing.T) {
	g := Gate{Threshold: 0.7}
	d, err := g.Evaluate(Signals{Criticality: "low", Environment: "dev", ChangeType: "config", Strategy: "canary"})
	if err != nil {
		t.Fatal(err)
	}
	if needsApproval(t, d) {
		t.Errorf("low-risk change should auto-proceed; reasons=%v score=%v", d.Reasons, d.Risk.Score)
	}
}

func TestGate_RequiresApprovalAboveThreshold(t *testing.T) {
	g := Gate{Threshold: 0.5}
	d, _ := g.Evaluate(Signals{Criticality: "critical", Environment: "prod", ChangeType: "schema", BlastRadius: 8, Strategy: "blue-green"})
	if !needsApproval(t, d) {
		t.Errorf("high-risk change must require approval; score=%v", d.Risk.Score)
	}
}

func TestGate_SensitiveOverridesLowScore(t *testing.T) {
	// Low computed score, but the sensitive policy flags schema changes.
	g := Gate{Threshold: 0.99, SensitiveExpr: `changeType == "schema"`}
	d, err := g.Evaluate(Signals{Criticality: "low", Environment: "dev", ChangeType: "schema", Strategy: "canary"})
	if err != nil {
		t.Fatal(err)
	}
	if !needsApproval(t, d) || !Sensitive(d) {
		t.Errorf("sensitive expr should force approval regardless of score; d=%+v", d)
	}
}

func TestGate_BadSensitiveExpr(t *testing.T) {
	g := Gate{Threshold: 0.5, SensitiveExpr: `changeType ===`}
	d, err := g.Evaluate(Signals{})
	if err == nil {
		t.Fatal("malformed sensitive expression should error")
	}
	// Fail CLOSED: an expression that would not compile decided nothing, and a
	// caller that ignores the error must not find permission in what it got.
	if policyv1.Permits(d, nil) == nil {
		t.Fatal("a gate that errored handed back a decision that authorizes")
	}
}

// TestGate_ApprovalIsARequirementNotADenial keeps the gate on the right side of
// §12.4. An above-threshold change is still an allowed operation — one with a
// condition attached — because a risk score that could refuse outright would be
// the parallel authorization system the spec forbids. The distinction is what
// sends the rollout to awaiting-approval rather than to failed.
func TestGate_ApprovalIsARequirementNotADenial(t *testing.T) {
	g := Gate{Threshold: 0.1}
	d, err := g.Evaluate(Signals{Criticality: "critical", Environment: "prod", ChangeType: "schema", Strategy: "blue-green"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Fatal("a high score denied the operation outright instead of requiring approval")
	}
	if len(d.Requirements) != 1 || d.Requirements[0].Kind != policyv1.RequireHumanApproval {
		t.Fatalf("requirements = %v, want a single human approval", d.Requirements)
	}
}

// TestAssess_ExplainsTheScore is §12.4's "SHOULD include explainable factors".
// A number nobody can decompose is a number nobody can argue with, and the
// operator arguing with it is the one who has to decide whether to approve.
func TestAssess_ExplainsTheScore(t *testing.T) {
	a := Assess(Signals{
		Criticality: "critical", Environment: "prod", ChangeType: "schema",
		BlastRadius: 8, Strategy: "blue-green",
	}, DefaultWeights())

	if len(a.Factors) == 0 {
		t.Fatal("an assessment with a score and no factors explains nothing")
	}
	var sum float64
	for _, f := range a.Factors {
		if f.Name == "" || f.Detail == "" {
			t.Errorf("factor %+v does not say what it is or what it saw", f)
		}
		sum += f.Weight
	}
	// The factors have to account for the score, or they are decoration rather
	// than an explanation of it.
	if diff := sum - a.Score; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("factor weights sum to %v but the score is %v", sum, a.Score)
	}
}

// TestAssess_HistoryIsOnlyExplainedWhenItCounts: a factor contributing nothing
// because history was never configured would read as "we looked at your
// rollback record and it was clean", which is not what happened.
func TestAssess_HistoryIsOnlyExplainedWhenItCounts(t *testing.T) {
	a := Assess(Signals{Criticality: "low", RecentFailures: 3}, DefaultWeights())
	for _, f := range a.Factors {
		if f.Name == FactorHistory {
			t.Fatal("history was explained as a factor while its weight was zero")
		}
	}

	w := DefaultWeights()
	w.History = 0.5
	on := Assess(Signals{Criticality: "low", RecentFailures: 3}, w)
	found := false
	for _, f := range on.Factors {
		if f.Name == FactorHistory {
			found = true
			if !strings.Contains(f.Detail, "3") {
				t.Errorf("history detail = %q, want the failure count in it", f.Detail)
			}
		}
	}
	if !found {
		t.Error("configured history did not appear among the factors")
	}
}

// TestAssess_LevelTracksScore gives the score a band a human and a CEL
// expression can both read.
func TestAssess_LevelTracksScore(t *testing.T) {
	low := Assess(Signals{Criticality: "low", Environment: "dev", ChangeType: "config", Strategy: "canary"}, DefaultWeights())
	if low.Level != policyv1.RiskLow {
		t.Errorf("level = %q at score %v, want %q", low.Level, low.Score, policyv1.RiskLow)
	}
	high := Assess(Signals{
		Criticality: "critical", Environment: "prod", ChangeType: "schema",
		BlastRadius: 20, Strategy: "blue-green",
	}, DefaultWeights())
	if high.Level != policyv1.RiskCritical {
		t.Errorf("level = %q at score %v, want %q", high.Level, high.Score, policyv1.RiskCritical)
	}
}
