package verify_test

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/analysis"
	"go.klarlabs.de/rollops/internal/verify"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// stubTarget is a target that answers Observe however the test needs it to.
type stubTarget struct {
	health targetv2.HealthStatus
	err    error
}

func (stubTarget) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "stub", Name: "stub", Version: "v2"}
}

func (stubTarget) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return targetv2.Capabilities{HealthObservation: true}, nil
}

func (stubTarget) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return targetv2.ObservedState{}, nil
}

func (stubTarget) Plan(context.Context, targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return targetv2.PlanResult{}, nil
}

func (stubTarget) Apply(context.Context, targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	return targetv2.ApplyResult{}, nil
}

func (s stubTarget) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	return targetv2.Observation{Health: s.health}, s.err
}

func (stubTarget) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

func bound(s stubTarget) *targetv2.Bound {
	return targetv2.NewBound(s, targetv2.Capabilities{HealthObservation: true})
}

func req() verifyv1.VerificationRequest {
	return verifyv1.VerificationRequest{Target: "prod/api", Revision: "sum-1"}
}

// TestHealthVerdicts covers the three answers a target can give and the fourth
// the verifier owes when the target could not be asked at all. Unhealthy is a
// fail — something looked and said no — while an Observe that errored is
// inconclusive, because nothing was observed.
func TestHealthVerdicts(t *testing.T) {
	cases := []struct {
		name string
		tgt  stubTarget
		want verifyv1.Verdict
	}{
		{"healthy", stubTarget{health: targetv2.HealthStatus{State: targetv2.HealthHealthy}}, verifyv1.VerdictPass},
		{"degraded still serves", stubTarget{health: targetv2.HealthStatus{State: targetv2.HealthDegraded}}, verifyv1.VerdictPass},
		{"unhealthy", stubTarget{health: targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: "2/5 ready"}}, verifyv1.VerdictFail},
		{"unknown is not a pass", stubTarget{}, verifyv1.VerdictInconclusive},
		{"unaskable", stubTarget{err: errors.New("api server unreachable")}, verifyv1.VerdictInconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := verify.Health{Target: bound(tc.tgt)}.Verify(context.Background(), req())
			if err != nil {
				t.Fatalf("a verdict about the target is not an error of the call: %v", err)
			}
			if res.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q (reason %q)", res.Verdict, tc.want, res.Reason)
			}
		})
	}
}

// TestHealthReasonNamesWhatTheTargetSaid keeps the fail actionable: a verdict
// with no reason is an assertion.
func TestHealthReasonNamesWhatTheTargetSaid(t *testing.T) {
	res, _ := verify.Health{
		Target: bound(stubTarget{health: targetv2.HealthStatus{State: targetv2.HealthUnhealthy, Reason: "2/5 ready"}}),
	}.Verify(context.Background(), req())
	if res.Reason == "" {
		t.Fatal("an unhealthy verdict carried no reason")
	}
}

// TestCommandVerdicts: the exit code is the observation. A command that could
// not be launched measured nothing.
func TestCommandVerdicts(t *testing.T) {
	cases := []struct {
		name string
		code int
		err  error
		want verifyv1.Verdict
	}{
		{"expected exit", 0, nil, verifyv1.VerdictPass},
		{"unexpected exit", 7, nil, verifyv1.VerdictFail},
		{"never ran", 0, errors.New("exec: \"probe\": not found"), verifyv1.VerdictInconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := verify.Command{
				Cmd:        []string{"probe"},
				ExpectExit: 0,
				Run:        func(context.Context, []string) (int, error) { return tc.code, tc.err },
			}
			res, err := v.Verify(context.Background(), req())
			if err != nil {
				t.Fatalf("unexpected call error: %v", err)
			}
			if res.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q (reason %q)", res.Verdict, tc.want, res.Reason)
			}
		})
	}
}

// scripted answers a fixed value, or refuses.
type scripted struct {
	val float64
	err error
}

func (s scripted) Query(context.Context, string) (float64, error) { return s.val, s.err }

// TestMetricsVerdictsPassThrough proves the analysis verdict reaches the
// contract unflattened — the whole point of R9. The five-verdict vocabulary
// would be wasted if the verifier collapsed it back to a bool on the way out.
func TestMetricsVerdictsPassThrough(t *testing.T) {
	cases := []struct {
		name string
		prov analysis.MetricsProvider
		want verifyv1.Verdict
	}{
		{"healthy canary", scripted{val: 0.01}, verifyv1.VerdictPass},
		{"breaching canary", scripted{val: 0.9}, verifyv1.VerdictFail},
		{"unreachable backend", scripted{err: errors.New("connection refused")}, verifyv1.VerdictInconclusive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			an, err := analysis.New(tc.prov, analysis.Template{
				Metrics:      []analysis.Metric{{Name: "errorRate", Query: "err"}},
				Condition:    "errorRate < 0.05",
				Count:        2,
				FailureLimit: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			res, verr := verify.Metrics{Analyzer: an}.Verify(context.Background(), req())
			if verr != nil {
				t.Fatalf("unexpected call error: %v", verr)
			}
			if res.Verdict != tc.want {
				t.Errorf("verdict = %q, want %q (reason %q)", res.Verdict, tc.want, res.Reason)
			}
		})
	}
}

// TestMetricsReportsWhatItMeasured: a verdict with measurements behind it can
// be read back by whoever has to act on it.
func TestMetricsReportsWhatItMeasured(t *testing.T) {
	an, err := analysis.New(scripted{val: 0.01}, analysis.Template{
		Metrics:      []analysis.Metric{{Name: "errorRate", Query: "err"}},
		Condition:    "errorRate < 0.05",
		Count:        2,
		FailureLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := verify.Metrics{Analyzer: an}.Verify(context.Background(), req())
	if len(res.Measurements) == 0 {
		t.Fatal("a passing analysis reported no measurements")
	}
	for _, m := range res.Measurements {
		if m.Name != "errorRate" {
			t.Errorf("measurement name = %q, want the metric it came from", m.Name)
		}
	}
}

// TestEveryVerifierReportsWhenItRan: a result with no window cannot be lined up
// against anything else that happened during the deploy.
func TestEveryVerifierReportsWhenItRan(t *testing.T) {
	an, _ := analysis.New(scripted{val: 0.01}, analysis.Template{
		Metrics: []analysis.Metric{{Name: "errorRate", Query: "err"}}, Condition: "errorRate < 0.05", Count: 1,
	})
	verifiers := []verifyv1.Verifier{
		verify.Health{Target: bound(stubTarget{health: targetv2.HealthStatus{State: targetv2.HealthHealthy}})},
		verify.Command{Cmd: []string{"probe"}, Run: func(context.Context, []string) (int, error) { return 0, nil }},
		verify.Metrics{Analyzer: an},
	}
	for _, v := range verifiers {
		kind := v.Metadata().Kind
		if kind == "" {
			t.Error("a verifier did not say what kind it is")
		}
		res, err := v.Verify(context.Background(), req())
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if res.StartedAt.IsZero() || res.FinishedAt.IsZero() {
			t.Errorf("%s: result has no window (%v..%v)", kind, res.StartedAt, res.FinishedAt)
		}
		if res.FinishedAt.Before(res.StartedAt) {
			t.Errorf("%s: finished before it started", kind)
		}
	}
}

// TestAnAbandonedVerifierSaysSoWithoutAsking mirrors targetv2.Abandoned: a check
// that only notices the abort when its backend does will report whatever its
// cache last held.
func TestAnAbandonedVerifierSaysSoWithoutAsking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reached := false
	v := verify.Command{
		Cmd: []string{"probe"},
		Run: func(context.Context, []string) (int, error) { reached = true; return 0, nil },
	}
	res, err := v.Verify(ctx, req())
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != verifyv1.VerdictCancelled {
		t.Errorf("verdict = %q, want %q", res.Verdict, verifyv1.VerdictCancelled)
	}
	if reached {
		t.Error("the command ran for a caller that had already gone")
	}
}
