package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const autoRollbackYAML = `
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
    healthCheck:
      http: https://demo/healthz
    smokeTest:
      command: ["./smoke.sh"]
      expectExit: 0
`

type fakeSmoke struct{ code int }

func (f fakeSmoke) Run(context.Context, []string) (int, error) { return f.code, nil }

type fakeDBRollback struct {
	command []string
	err     error
}

func (f *fakeDBRollback) Run(_ context.Context, command []string) error {
	f.command = append([]string(nil), command...)
	return f.err
}

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
	// Deploy succeeds healthy; the target degrades afterwards, so the
	// post-deploy gate (not the deploy-time gate) is what rolls it back.
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	c := loadAutoRollback(t)
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})
	fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"} // degrade post-deploy

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

func TestVerifyOrRollback_DatabaseRollbackHookRunsAfterAutoRollback(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	db := &fakeDBRollback{}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 1}), WithDatabaseRollbackRunner(db))
	c := loadAutoRollback(t)
	c.Spec.Rollback.Database = &config.DatabaseRollback{Command: []string{"goose", "down"}}
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})

	out, err := e.VerifyOrRollback(context.Background(), r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err != nil {
		t.Fatalf("VerifyOrRollback: %v", err)
	}
	if !out.RolledBack {
		t.Fatal("failing smoke test should auto-roll-back")
	}
	if strings.Join(db.command, " ") != "goose down" {
		t.Fatalf("database rollback command = %v", db.command)
	}
	hist, err := e.History(context.Background(), c.Spec.Target.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) == 0 || !strings.Contains(hist[0].Note, "database rollback: succeeded") {
		t.Fatalf("history note = %+v, want database rollback success", hist)
	}
}

func TestRollback_ManualRunsPersistedDatabaseHook(t *testing.T) {
	// A manual rollback has no config in hand, but the database command captured
	// on the rollout at deploy time must still run — closing the gap where only
	// auto-rollback reversed the database.
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	db := &fakeDBRollback{}
	e, _ := newEngine(t, fake, WithDatabaseRollbackRunner(db))
	c := loadAutoRollback(t)
	c.Spec.Rollback.Database = &config.DatabaseRollback{Command: []string{"goose", "down"}}
	r, err := e.Apply(context.Background(), ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Manual rollback: caller passes only the prior manifest, no DB config.
	out, err := e.Rollback(context.Background(), r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if string(out.Phase) != "rolled-back" {
		t.Errorf("phase = %q, want rolled-back", out.Phase)
	}
	if strings.Join(db.command, " ") != "goose down" {
		t.Fatalf("manual rollback database command = %v, want [goose down]", db.command)
	}
	if !strings.Contains(out.Note, "database rollback: succeeded") {
		t.Errorf("note = %q, want database rollback success", out.Note)
	}
}

func TestRollback_ManualNoDatabaseHookNoop(t *testing.T) {
	// No database command configured → manual rollback must not invoke the runner.
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	db := &fakeDBRollback{}
	e, _ := newEngine(t, fake, WithDatabaseRollbackRunner(db))
	c := loadAutoRollback(t) // no rollback.database block
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})

	if _, err := e.Rollback(context.Background(), r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if db.command != nil {
		t.Fatalf("database runner must not be called without a configured command, got %v", db.command)
	}
}

func TestVerifyOrRollback_DatabaseRollbackHookFailureIsLoud(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	db := &fakeDBRollback{err: errors.New("migration refused")}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 1}), WithDatabaseRollbackRunner(db))
	c := loadAutoRollback(t)
	c.Spec.Rollback.Database = &config.DatabaseRollback{Command: []string{"goose", "down"}}
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c})

	_, err := e.VerifyOrRollback(context.Background(), r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err == nil || !strings.Contains(err.Error(), "database rollback") {
		t.Fatalf("err = %v, want database rollback failure", err)
	}
	hist, _ := e.History(context.Background(), c.Spec.Target.Ref)
	if len(hist) == 0 || !strings.Contains(hist[0].Note, "database rollback: failed") {
		t.Fatalf("history note = %+v, want database rollback failure", hist)
	}
}
