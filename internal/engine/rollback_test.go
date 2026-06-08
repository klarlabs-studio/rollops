package engine

import (
	"context"
	"testing"

	"go.klarlabs.de/rolloffs/internal/config"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

const autoRollbackYAML = `
apiVersion: rolloffs.klarlabs.de/v1
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
    healthCheck:
      http: https://demo/healthz
    smokeTest:
      command: ["./smoke.sh"]
      expectExit: 0
`

type fakeSmoke struct{ code int }

func (f fakeSmoke) Run(context.Context, []string) (int, error) { return f.code, nil }

func loadAutoRollback(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]byte(autoRollbackYAML))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func TestVerifyOrRollback_HealthyPromotes(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	c := loadAutoRollback(t)
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})

	out, err := e.VerifyOrRollback(context.Background(), r.ID, pt.Manifest{Kind: "fake"}, c)
	if err != nil {
		t.Fatalf("VerifyOrRollback: %v", err)
	}
	if out.RolledBack {
		t.Error("healthy + passing smoke should promote, not roll back")
	}
	if string(out.Rollout.Phase) != "promoted" {
		t.Errorf("phase = %q, want promoted", out.Rollout.Phase)
	}
}

func TestVerifyOrRollback_UnhealthyRollsBack(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"}}
	e, _ := newEngine(t, fake)
	c := loadAutoRollback(t)
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})

	prior := pt.Manifest{Kind: "fake", Checksum: "prior"}
	out, err := e.VerifyOrRollback(context.Background(), r.ID, prior, c)
	if err != nil {
		t.Fatalf("VerifyOrRollback: %v", err)
	}
	if !out.RolledBack {
		t.Fatal("unhealthy target should auto-roll-back")
	}
	if string(out.Rollout.Phase) != "rolled-back" {
		t.Errorf("phase = %q, want rolled-back", out.Rollout.Phase)
	}
}

func TestVerifyOrRollback_SmokeFailureRollsBack(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 1})) // smoke fails
	c := loadAutoRollback(t)
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})

	out, err := e.VerifyOrRollback(context.Background(), r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err != nil {
		t.Fatalf("VerifyOrRollback: %v", err)
	}
	if !out.RolledBack {
		t.Error("failing smoke test should auto-roll-back")
	}
}
