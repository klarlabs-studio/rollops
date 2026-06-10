package risk

import "testing"

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

func TestGate_AutoProceedBelowThreshold(t *testing.T) {
	g := Gate{Threshold: 0.7}
	d, err := g.Evaluate(Signals{Criticality: "low", Environment: "dev", ChangeType: "config", Strategy: "canary"})
	if err != nil {
		t.Fatal(err)
	}
	if d.NeedsApproval {
		t.Errorf("low-risk change should auto-proceed; reason=%q score=%v", d.Reason, d.Score)
	}
}

func TestGate_RequiresApprovalAboveThreshold(t *testing.T) {
	g := Gate{Threshold: 0.5}
	d, _ := g.Evaluate(Signals{Criticality: "critical", Environment: "prod", ChangeType: "schema", BlastRadius: 8, Strategy: "blue-green"})
	if !d.NeedsApproval {
		t.Errorf("high-risk change must require approval; score=%v", d.Score)
	}
}

func TestGate_SensitiveOverridesLowScore(t *testing.T) {
	// Low computed score, but the sensitive policy flags schema changes.
	g := Gate{Threshold: 0.99, SensitiveExpr: `changeType == "schema"`}
	d, err := g.Evaluate(Signals{Criticality: "low", Environment: "dev", ChangeType: "schema", Strategy: "canary"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsApproval || !d.Sensitive {
		t.Errorf("sensitive expr should force approval regardless of score; d=%+v", d)
	}
}

func TestGate_BadSensitiveExpr(t *testing.T) {
	g := Gate{Threshold: 0.5, SensitiveExpr: `changeType ===`}
	if _, err := g.Evaluate(Signals{}); err == nil {
		t.Fatal("malformed sensitive expression should error")
	}
}
