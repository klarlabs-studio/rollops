package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// countingSmoke records how many times the smoke gate ran, so a test can prove
// a path runs it exactly once (or not at all).
type countingSmoke struct {
	code  int
	err   error
	calls int
}

func (c *countingSmoke) Run(context.Context, []string) (int, error) {
	c.calls++
	return c.code, c.err
}

// countingMetrics is fixedMetrics that records whether it was queried at all,
// so a test can prove a gate short-circuited before analysis.
type countingMetrics struct {
	value float64
	calls int
}

func (c *countingMetrics) Query(context.Context, string) (float64, error) {
	c.calls++
	return c.value, nil
}

// TestApply_CapturesSmokeTest proves the smoke-test descriptor is captured on
// the rollout at deploy time, so a later manual Verify/Promote can run the same
// gate as the auto path without the config in hand.
func TestApply_CapturesSmokeTest(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("load rollout: %v", err)
	}
	if len(got.SmokeTest) == 0 {
		t.Fatal("Apply should capture spec.rollback.smokeTest on the rollout, got none")
	}
	var st config.SmokeTest
	if err := json.Unmarshal(got.SmokeTest, &st); err != nil {
		t.Fatalf("captured smoke test is not valid JSON: %v", err)
	}
	if strings.Join(st.Command, " ") != "./smoke.sh" {
		t.Errorf("captured smoke command = %v, want [./smoke.sh]", st.Command)
	}
}

// TestApply_NoSmokeTestLeavesEmpty proves a config without a smoke test leaves
// the descriptor empty, so the len==0 guard in the manual gate holds.
func TestApply_NoSmokeTestLeavesEmpty(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("load rollout: %v", err)
	}
	if len(got.SmokeTest) != 0 {
		t.Errorf("no smokeTest should leave SmokeTest empty, got %q", got.SmokeTest)
	}
}

// TestVerify_RunsSmokeTestFails proves a manual Verify runs the same smoke gate
// as the auto path: a healthy target still fails Verify when the smoke command
// exits non-zero.
func TestVerify_RunsSmokeTestFails(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	smoke := &countingSmoke{code: 1}
	e, _ := newEngine(t, fake, WithSmokeRunner(smoke))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	smoke.calls = 0 // ignore any deploy-path runs; count the Verify gate only
	if _, err := e.Verify(ctx, r.ID); err == nil {
		t.Fatal("manual verify should fail when the smoke test exits non-zero, even with a healthy target")
	} else if !strings.Contains(err.Error(), "smoke") {
		t.Errorf("verify error = %q, want a smoke-test failure", err)
	}
	if smoke.calls != 1 {
		t.Errorf("smoke ran %d times during Verify, want exactly 1", smoke.calls)
	}
}

// TestVerify_RunsSmokeTestPasses proves a healthy target plus a passing smoke
// test clears Verify.
func TestVerify_RunsSmokeTestPasses(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Verify(ctx, r.ID); err != nil {
		t.Fatalf("healthy target + passing smoke should verify: %v", err)
	}
}

// TestVerify_SmokeNoopWhenAbsent proves a rollout with no captured smoke test
// verifies on health alone — the len(r.SmokeTest)==0 guard holds, so the
// health-only behaviour of configs without a smoke test is unchanged.
func TestVerify_SmokeNoopWhenAbsent(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	smoke := &countingSmoke{code: 1} // would fail if it ran
	e, _ := newEngine(t, fake, WithSmokeRunner(smoke))
	ctx := context.Background()
	// fakeYAML carries no rollback.smokeTest, so r.SmokeTest stays empty.
	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Verify(ctx, r.ID); err != nil {
		t.Fatalf("no captured smoke test: verify should be health-only: %v", err)
	}
	if smoke.calls != 0 {
		t.Errorf("smoke ran %d times with no captured smoke test, want 0", smoke.calls)
	}
}

// TestVerify_SmokeRunsBeforeAnalysis proves the manual gate keeps the auto
// path's ordering (health → smoke → analysis): a failing smoke test short-
// circuits before the metrics provider is ever consulted.
func TestVerify_SmokeRunsBeforeAnalysis(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	metrics := &countingMetrics{value: 0.01} // would pass if consulted
	e, _ := newEngine(t, fake,
		WithSmokeRunner(&countingSmoke{code: 1}),
		WithMetricAnalysis(), WithMetricsProvider(metrics))
	ctx := context.Background()
	c, err := config.Load([]byte(smokeAnalysisYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Verify(ctx, r.ID); err == nil {
		t.Fatal("failing smoke should fail verify")
	} else if !strings.Contains(err.Error(), "smoke") {
		t.Errorf("verify error = %q, want the smoke failure to win", err)
	}
	if metrics.calls != 0 {
		t.Errorf("analysis ran %d times after a failing smoke gate, want 0 (smoke short-circuits)", metrics.calls)
	}
}

// TestPromote_RunsSmokeTestFails proves a direct manual Promote — issuable on a
// freshly-deployed rollout without a prior Verify — cannot skip the smoke gate.
func TestPromote_RunsSmokeTestFails(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 1}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Promote(ctx, r.ID); err == nil {
		t.Fatal("direct promote must not bypass a failing smoke gate")
	} else if !strings.Contains(err.Error(), "smoke") {
		t.Errorf("promote error = %q, want a smoke-test failure", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want it held at verifying (not promoted)", got.Phase)
	}
}

// TestPromote_RunsSmokeTestPasses proves the promote gate lets a passing smoke
// test through to promoted.
func TestPromote_RunsSmokeTestPasses(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	pr, err := e.Promote(ctx, r.ID)
	if err != nil {
		t.Fatalf("passing smoke should promote: %v", err)
	}
	if pr.Phase != rollout.PhasePromoted {
		t.Errorf("phase = %q, want promoted", pr.Phase)
	}
}

// TestVerifyOrRollback_SmokeRunsOnce proves the auto path is unchanged: it runs
// the smoke gate exactly once (through runPostDeployChecks), not twice via the
// manual gate that promoteWithNote sits behind.
func TestVerifyOrRollback_SmokeRunsOnce(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	smoke := &countingSmoke{code: 0}
	e, _ := newEngine(t, fake, WithSmokeRunner(smoke))
	ctx := context.Background()
	c := loadAutoRollback(t)
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	smoke.calls = 0
	out, err := e.VerifyOrRollback(ctx, r.ID, pt.Manifest{Kind: "fake"}, c)
	if err != nil {
		t.Fatalf("VerifyOrRollback: %v", err)
	}
	if out.RolledBack {
		t.Fatal("healthy + passing smoke should promote")
	}
	if smoke.calls != 1 {
		t.Errorf("auto path ran smoke %d times, want exactly 1", smoke.calls)
	}
}

const smokeAnalysisYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: medium
    spec:
      x: 1
  strategy:
    type: rolling
  rollback:
    auto: true
    smokeTest:
      command: ["./smoke.sh"]
      expectExit: 0
  analysis:
    provider: prometheus
    address: http://prom:9090
    interval: 1s
    condition: "errorRate < 0.05"
    metrics:
      - name: errorRate
        query: "sum(rate(errors[1m]))"
`
