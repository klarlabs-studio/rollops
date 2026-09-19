package gate_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/policy/gate"
	"go.klarlabs.de/rollops/internal/policy/risk"
)

func engine(t *testing.T, cfg gate.Config) *gate.Engine {
	t.Helper()
	e, err := gate.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func op(id string) plan.PlannedOperation {
	return plan.PlannedOperation{
		ID:         plan.OperationID(id),
		Target:     "web",
		Kind:       plan.OperationApply,
		Summary:    "roll out " + id,
		Reversible: true,
	}
}

// quiet is a deployment nothing about which is alarming, so that a requirement
// appearing in one of these tests came from configuration rather than risk.
func quiet() deploy.PolicyRequest {
	return deploy.PolicyRequest{
		Environment: environment.Environment{Name: "dev", Kind: environment.KindDevelopment},
		Strategy:    deployment.StrategyCanary,
		Operations:  []plan.PlannedOperation{op("a")},
	}
}

// alarming is the opposite: everything the scorer can see is as bad as it gets.
func alarming() deploy.PolicyRequest {
	req := deploy.PolicyRequest{
		Environment: environment.Environment{Name: "prod", Kind: environment.KindProduction},
		Strategy:    deployment.StrategyRecreate,
		Operations:  []plan.PlannedOperation{op("a"), op("b"), op("c")},
	}
	req.Operations[0].Reversible = false
	return req
}

func evaluate(t *testing.T, e *gate.Engine, req deploy.PolicyRequest) policy.Decision {
	t.Helper()
	d, err := e.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("the decision is not a complete record: %v", err)
	}
	return d
}

func bind(req deploy.PolicyRequest, name, ref string, mode environment.PolicyMode) deploy.PolicyRequest {
	req.Environment.Policies = append(req.Environment.Policies,
		environment.PolicyBinding{Name: name, Ref: ref, Mode: mode})
	return req
}

func hasReason(d policy.Decision, code string) bool {
	for _, r := range d.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

func TestAnUnconfiguredEnvironmentIsAllowedAndAsksForNothing(t *testing.T) {
	d := evaluate(t, engine(t, gate.Config{}), quiet())
	if !d.Allowed {
		t.Error("an environment with no policy was refused")
	}
	if len(d.Requirements) != 0 {
		t.Errorf("requirements %v came from nowhere", d.Requirements)
	}
}

// The assessment travels inside the decision rather than beside it, so there is
// one record to read when asking why an apply was gated (spec 12.4).
func TestTheDecisionCarriesTheRiskItWasJudgedOn(t *testing.T) {
	req := alarming()
	d := evaluate(t, engine(t, gate.Config{}), req)
	want := risk.Assess(risk.Signals{
		Environment: req.Environment.Kind,
		Strategy:    req.Strategy,
		Operations:  req.Operations,
	})
	if !reflect.DeepEqual(d.Risk, want) {
		t.Errorf("risk %+v, want %+v", d.Risk, want)
	}
}

func TestTheFloorAppliesWhereNoPolicyIsBound(t *testing.T) {
	e := engine(t, gate.Config{Floor: gate.Rules{Approvals: []gate.Approval{{Count: 1}}}})
	d := evaluate(t, e, quiet())
	want := []policy.Requirement{{Type: policy.RequireApproval, Count: 1}}
	if !sameRequirements(d.Requirements, want) {
		t.Errorf("requirements %+v, want %+v", d.Requirements, want)
	}
}

func TestABoundPolicyAsksForWhatItDeclares(t *testing.T) {
	e := engine(t, gate.Config{Policies: map[string]gate.Rules{
		"prod": {Approvals: []gate.Approval{{Role: "sre", Count: 2}}},
	}})
	d := evaluate(t, e, bind(alarming(), "gate", "prod", environment.PolicyEnforce))
	want := []policy.Requirement{{Type: policy.RequireApproval, Role: "sre", Count: 2}}
	if !sameRequirements(d.Requirements, want) {
		t.Errorf("requirements %+v, want %+v", d.Requirements, want)
	}
}

// A binding in warn mode is the operator asking to be told, not to be stopped.
// Attaching its requirement anyway would make the two modes the same thing.
func TestAnAdvisoryPolicyExplainsButDoesNotGate(t *testing.T) {
	e := engine(t, gate.Config{Policies: map[string]gate.Rules{
		"prod": {Approvals: []gate.Approval{{Role: "sre", Count: 2}}},
	}})
	d := evaluate(t, e, bind(alarming(), "gate", "prod", environment.PolicyWarn))
	if len(d.Requirements) != 0 {
		t.Errorf("an advisory policy gated the deployment with %+v", d.Requirements)
	}
	if !hasReason(d, gate.ReasonAdvisory) {
		t.Errorf("reasons %+v do not mention the advisory policy", d.Reasons)
	}
}

// An environment referencing a policy that is not loaded has been configured
// with a gate nobody can evaluate. Proceeding would apply the change the
// missing policy existed to hold back.
func TestAPolicyBindingToNothingIsRefused(t *testing.T) {
	d, err := engine(t, gate.Config{}).Evaluate(context.Background(),
		bind(quiet(), "gate", "nowhere", environment.PolicyEnforce))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allowed {
		t.Error("a binding to a policy that is not configured was allowed")
	}
	if !hasReason(d, gate.ReasonUnknownPolicy) {
		t.Errorf("reasons %+v do not name the missing policy", d.Reasons)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("the refusal is not a complete record: %v", err)
	}
}

// An advisory binding to a missing policy is still a missing policy: the
// operator asked for an opinion the engine cannot produce.
func TestAnAdvisoryBindingToNothingIsAlsoRefused(t *testing.T) {
	d, err := engine(t, gate.Config{}).Evaluate(context.Background(),
		bind(quiet(), "gate", "nowhere", environment.PolicyWarn))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allowed {
		t.Error("an advisory binding to a policy that is not configured was allowed")
	}
}

func TestRiskAtOrAboveTheBandAsksForAnApproval(t *testing.T) {
	e := engine(t, gate.Config{Floor: gate.Rules{ApproveAtOrAbove: policy.RiskHigh}})
	d := evaluate(t, e, alarming())
	if d.Risk.Level.Below(policy.RiskHigh) {
		t.Fatalf("the fixture scored %s, which is below the band under test", d.Risk.Level)
	}
	want := []policy.Requirement{{Type: policy.RequireApproval, Count: 1}}
	if !sameRequirements(d.Requirements, want) {
		t.Errorf("requirements %+v, want %+v", d.Requirements, want)
	}
	if !hasReason(d, gate.ReasonRiskBand) {
		t.Errorf("reasons %+v do not say the risk band is what gated it", d.Reasons)
	}
}

func TestRiskBelowTheBandAsksForNothing(t *testing.T) {
	e := engine(t, gate.Config{Floor: gate.Rules{ApproveAtOrAbove: policy.RiskHigh}})
	d := evaluate(t, e, quiet())
	if len(d.Requirements) != 0 {
		t.Errorf("a low-risk change was gated by %+v", d.Requirements)
	}
}

// No band configured means risk alone never gates. It must not read as "gate
// on everything", which is what an unconsidered zero value would do.
func TestNoBandMeansRiskAloneNeverGates(t *testing.T) {
	d := evaluate(t, engine(t, gate.Config{Floor: gate.Rules{}}), alarming())
	if len(d.Requirements) != 0 {
		t.Errorf("risk gated a deployment with no band configured: %+v", d.Requirements)
	}
}

// Two rules wanting one approval from the same people want one person, not
// two. Summing them would invent a four-eyes rule nobody wrote.
func TestTwoRulesWantingTheSameApproverDoNotAddUp(t *testing.T) {
	e := engine(t, gate.Config{
		Floor:    gate.Rules{Approvals: []gate.Approval{{Count: 1}}},
		Policies: map[string]gate.Rules{"prod": {Approvals: []gate.Approval{{Count: 2}}}},
	})
	d := evaluate(t, e, bind(alarming(), "gate", "prod", environment.PolicyEnforce))
	want := []policy.Requirement{{Type: policy.RequireApproval, Count: 2}}
	if !sameRequirements(d.Requirements, want) {
		t.Errorf("requirements %+v, want %+v", d.Requirements, want)
	}
}

func TestDifferentRolesAreDifferentRequirements(t *testing.T) {
	e := engine(t, gate.Config{
		Floor:    gate.Rules{Approvals: []gate.Approval{{Count: 1}}},
		Policies: map[string]gate.Rules{"prod": {Approvals: []gate.Approval{{Role: "sre", Count: 1}}}},
	})
	d := evaluate(t, e, bind(alarming(), "gate", "prod", environment.PolicyEnforce))
	want := []policy.Requirement{
		{Type: policy.RequireApproval, Count: 1},
		{Type: policy.RequireApproval, Role: "sre", Count: 1},
	}
	if !sameRequirements(d.Requirements, want) {
		t.Errorf("requirements %+v, want %+v", d.Requirements, want)
	}
}

// The plan hash covers the requirements in the order they are listed, so two
// evaluations of one request that differ only in order would produce two plans
// that mean the same thing and hash differently. Merging through a map is
// exactly how that gets in.
func TestRequirementsComeOutInTheSameOrderEveryTime(t *testing.T) {
	e := engine(t, gate.Config{Policies: map[string]gate.Rules{
		"a": {Approvals: []gate.Approval{{Role: "sre", Count: 1}, {Role: "dba", Count: 1}}},
		"b": {Approvals: []gate.Approval{{Role: "security", Count: 1}, {Count: 1}}},
	}})
	req := bind(bind(alarming(), "one", "a", environment.PolicyEnforce), "two", "b", environment.PolicyEnforce)
	first := evaluate(t, e, req)
	for range 50 {
		if got := evaluate(t, e, req); !reflect.DeepEqual(got.Requirements, first.Requirements) {
			t.Fatalf("requirements %+v, then %+v", first.Requirements, got.Requirements)
		}
	}
	if len(first.Requirements) != 4 {
		t.Errorf("got %d requirements, want 4", len(first.Requirements))
	}
}

// A requirement for zero approvals is discharged by doing nothing. It reads as
// a gate without being one, which is worse than no gate — so it is refused
// where it was written rather than on the first deployment of the day.
func TestAnApprovalNobodyNeedGiveIsRefusedAtStartup(t *testing.T) {
	_, err := gate.New(gate.Config{Floor: gate.Rules{Approvals: []gate.Approval{{Count: 0}}}})
	if !errors.Is(err, gate.ErrUnusableRules) {
		t.Errorf("New gave %v, want ErrUnusableRules", err)
	}
}

// A misspelled band ranks above critical, so it would silently mean "never
// gate" — configuration that fails open without saying so.
func TestAMisspelledRiskBandIsRefusedAtStartup(t *testing.T) {
	_, err := gate.New(gate.Config{Floor: gate.Rules{ApproveAtOrAbove: "hgh"}})
	if !errors.Is(err, gate.ErrUnusableRules) {
		t.Errorf("New gave %v, want ErrUnusableRules", err)
	}
}

func TestANamedPolicyIsCheckedAtStartupToo(t *testing.T) {
	_, err := gate.New(gate.Config{Policies: map[string]gate.Rules{
		"prod": {Approvals: []gate.Approval{{Role: "sre"}}},
	}})
	if !errors.Is(err, gate.ErrUnusableRules) {
		t.Errorf("New gave %v, want ErrUnusableRules", err)
	}
}

// The engine is the thing a Service is built with, so it has to satisfy the
// port by construction rather than by the composition root happening to
// compile.
func TestTheEngineIsAPolicyEngine(t *testing.T) {
	var _ deploy.PolicyEngine = engine(t, gate.Config{})
}

func TestEveryDecisionIsACompleteRecord(t *testing.T) {
	e := engine(t, gate.Config{
		Floor:    gate.Rules{ApproveAtOrAbove: policy.RiskMedium},
		Policies: map[string]gate.Rules{"prod": {Approvals: []gate.Approval{{Role: "sre", Count: 2}}}},
	})
	for _, mode := range []environment.PolicyMode{environment.PolicyEnforce, environment.PolicyWarn} {
		for _, ref := range []string{"prod", "missing"} {
			for _, req := range []deploy.PolicyRequest{quiet(), alarming()} {
				d, err := e.Evaluate(context.Background(), bind(req, "gate", ref, mode))
				if err != nil {
					t.Fatalf("%s/%s: %v", mode, ref, err)
				}
				if err := d.Validate(); err != nil {
					t.Errorf("%s/%s: %v", mode, ref, err)
				}
			}
		}
	}
}

func sameRequirements(got, want []policy.Requirement) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		// Detail is prose the engine writes for the reader; the requirement is
		// the rest, and that is what a test should pin.
		g := got[i]
		g.Detail = ""
		if g != want[i] {
			return false
		}
	}
	return true
}
