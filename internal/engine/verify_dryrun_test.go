package engine

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/audit"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// gateByName returns the named gate from a report, failing the test if the
// report does not carry it — every gate is always reported.
func gateByName(t *testing.T, rep VerifyReport, name string) GateResult {
	t.Helper()
	for _, g := range rep.Gates {
		if g.Gate == name {
			return g
		}
	}
	t.Fatalf("report has no %q gate: %+v", name, rep.Gates)
	return GateResult{}
}

// TestVerify_ReportsEveryGate proves a dry run accounts for all three gates —
// no gate is silently missing from the report.
func TestVerify_ReportsEveryGate(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(rep.Gates) != 3 {
		t.Fatalf("got %d gates, want all 3 reported: %+v", len(rep.Gates), rep.Gates)
	}
	for _, name := range []string{GateHealth, GateSmoke, GateAnalysis} {
		gateByName(t, rep, name)
	}
	if !rep.OK || rep.Reason != "" {
		t.Errorf("OK=%v reason=%q, want a clean pass", rep.OK, rep.Reason)
	}
	if rep.RolloutID != r.ID || rep.TargetRef != r.TargetRef {
		t.Errorf("report identifies %s/%s, want %s/%s", rep.RolloutID, rep.TargetRef, r.ID, r.TargetRef)
	}
	// Health passed and says so; analysis was never configured here.
	if g := gateByName(t, rep, GateHealth); g.Status != GatePass || g.Detail != "healthy" {
		t.Errorf("health gate = %+v, want pass/healthy", g)
	}
	if g := gateByName(t, rep, GateAnalysis); g.Status != GateSkipped {
		t.Errorf("analysis gate = %+v, want skipped", g)
	}
}

// TestVerify_ChangesNothing is the core dry-run guarantee: running a
// verification — passing or failing — leaves the stored rollout untouched.
func TestVerify_ChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		smoke int
	}{
		{"passing gates", 0},
		{"failing gates", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
			e, db := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: tc.smoke}))
			ctx := context.Background()
			r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			before, err := db.LoadRollout(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			histBefore, err := e.History(ctx, r.TargetRef)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := e.Verify(ctx, r.ID); err != nil {
				t.Fatalf("verify: %v", err)
			}

			after, err := db.LoadRollout(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Phase != before.Phase {
				t.Errorf("phase moved %q → %q; a dry run must not transition", before.Phase, after.Phase)
			}
			if !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Errorf("UpdatedAt moved %v → %v; a dry run must not write the rollout", before.UpdatedAt, after.UpdatedAt)
			}
			if after.Note != before.Note {
				t.Errorf("note changed %q → %q", before.Note, after.Note)
			}
			histAfter, err := e.History(ctx, r.TargetRef)
			if err != nil {
				t.Fatal(err)
			}
			if len(histAfter) != len(histBefore) {
				t.Errorf("history grew %d → %d; a dry run must not append", len(histBefore), len(histAfter))
			}
		})
	}
}

// TestVerify_RepeatableOnAPromotedRollout proves the dry run is callable on a
// settled rollout and stays read-only there too — it answers "do the gates pass
// right now?", independent of phase.
func TestVerify_RepeatableOnAPromotedRollout(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Promote(ctx, r.ID, rollout.Identity{}, false); err != nil {
		t.Fatalf("promote: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify on a promoted rollout: %v", err)
	}
	if !rep.OK {
		t.Errorf("promoted + healthy should verify: %+v", rep.Gates)
	}
	if rep.Phase != string(rollout.PhasePromoted) {
		t.Errorf("report phase = %q, want promoted (reported, not changed)", rep.Phase)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != rollout.PhasePromoted {
		t.Errorf("stored phase = %q, want it left at promoted", got.Phase)
	}
}

// TestVerify_UnreadableDescriptorFailsClosed proves a captured descriptor that
// cannot be decoded is an operational error, not a silent pass.
func TestVerify_UnreadableDescriptorFailsClosed(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	stored, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.SmokeTest = []byte(`{"command": not json`)
	if err := db.SaveRollout(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Verify(ctx, r.ID); err == nil {
		t.Fatal("an unreadable smoke descriptor must fail closed, not verify clean")
	}
	// Promote must refuse it too.
	if _, err := e.Promote(ctx, r.ID, rollout.Identity{}, false); err == nil {
		t.Fatal("an unreadable smoke descriptor must block promote")
	}
}

// TestVerify_DegradedTargetStillPasses pins the health verdict boundary: only
// Unhealthy fails, and the report names the state that passed.
func TestVerify_DegradedTargetStillPasses(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthDegraded, Reason: "1/2 replicas"}}
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("degraded is not unhealthy — it should pass: %+v", rep.Gates)
	}
	if g := gateByName(t, rep, GateHealth); g.Detail != "degraded" {
		t.Errorf("health gate = %+v, want the degraded state named", g)
	}
}

// TestPromote_GatesOnHealth proves promote enforces the SAME gates verify
// dry-runs — including health. Verify is promote's dry run, not a superset.
func TestPromote_GatesOnHealth(t *testing.T) {
	// Deploy healthy (the deploy path has its own health gate), then degrade the
	// target so only the post-deploy gates see it unhealthy.
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"}

	// Verify reports the unhealthy target...
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK {
		t.Fatal("verify should report the unhealthy target")
	}
	// ...and promote refuses it for the same reason, pointing at the override.
	pr, err := e.Promote(ctx, r.ID, rollout.Identity{Kind: "human", Name: "felix"}, false)
	if err == nil {
		t.Fatal("promote must not advance past a failing health gate")
	}
	if !strings.Contains(err.Error(), "health") || !strings.Contains(err.Error(), "force") {
		t.Errorf("error = %q, want the health failure and how to override", err)
	}
	if pr.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want it held at verifying", pr.Phase)
	}
}

// TestPromote_ForceOverridesFailingGate proves the break-glass path: force
// promotes past a failing gate, and the bypass is recorded — on the rollout's
// note and in the audit trail — so it is never silent.
func TestPromote_ForceOverridesFailingGate(t *testing.T) {
	var auditLog bytes.Buffer
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 1}), WithAudit(audit.New(&auditLog)))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Unforced: blocked by the failing smoke gate.
	if _, err := e.Promote(ctx, r.ID, rollout.Identity{Kind: "human", Name: "felix"}, false); err == nil {
		t.Fatal("unforced promote should be blocked by the failing smoke gate")
	}
	// Forced: promotes anyway.
	pr, err := e.Promote(ctx, r.ID, rollout.Identity{Kind: "human", Name: "felix"}, true)
	if err != nil {
		t.Fatalf("forced promote should succeed: %v", err)
	}
	if pr.Phase != rollout.PhasePromoted {
		t.Fatalf("phase = %q, want promoted", pr.Phase)
	}
	if !strings.Contains(pr.Note, "bypassed") {
		t.Errorf("note = %q, want the bypass recorded on the rollout", pr.Note)
	}
	stored, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Note, "bypassed") {
		t.Errorf("persisted note = %q, want the bypass durable", stored.Note)
	}
	// The audit trail names the action, the actor, and the bypass.
	entry := auditLog.String()
	for _, want := range []string{"promote", "felix", "bypassed"} {
		if !strings.Contains(entry, want) {
			t.Errorf("audit log missing %q:\n%s", want, entry)
		}
	}
}

// TestPromote_AuditsUnforcedPromotion proves ordinary promotions are audited
// too — the trail records who completed a rollout, not only who overrode a gate.
func TestPromote_AuditsUnforcedPromotion(t *testing.T) {
	var auditLog bytes.Buffer
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: 0}), WithAudit(audit.New(&auditLog)))
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := e.Promote(ctx, r.ID, rollout.Identity{Kind: "human", Name: "felix"}, false); err != nil {
		t.Fatalf("promote: %v", err)
	}
	entry := auditLog.String()
	if !strings.Contains(entry, "promote") || !strings.Contains(entry, "felix") {
		t.Errorf("audit log should record the promotion and its actor:\n%s", entry)
	}
	if strings.Contains(entry, "bypassed") {
		t.Errorf("an unforced promote must not be logged as a bypass:\n%s", entry)
	}
}

// TestVerifyThenPromote_Agree is the contract the whole design rests on: verify
// is promote's dry run. Whatever verify says, promote does — for every gate
// outcome, not just the happy path.
func TestVerifyThenPromote_Agree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		smokeCode int
		unhealthy bool
	}{
		{"all gates pass", 0, false},
		{"smoke fails", 1, false},
		{"health fails", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
			e, _ := newEngine(t, fake, WithSmokeRunner(fakeSmoke{code: tc.smokeCode}))
			ctx := context.Background()
			r, err := e.Apply(ctx, ApplyRequest{Config: loadAutoRollback(t)})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			if tc.unhealthy {
				fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"}
			}
			rep, err := e.Verify(ctx, r.ID)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			_, promoteErr := e.Promote(ctx, r.ID, rollout.Identity{}, false)
			if rep.OK != (promoteErr == nil) {
				t.Errorf("verify said OK=%v but promote err=%v — the dry run must predict the real thing", rep.OK, promoteErr)
			}
		})
	}
}

// TestVerify_MatchesAutoPathVerdict is the anti-drift guarantee: for the same
// rollout and the same gates, the dry run reaches the same verdict as the auto
// path (VerifyOrRollback), which decides between promote and rollback.
func TestVerify_MatchesAutoPathVerdict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		smokeCode int
		wantOK    bool
	}{
		{"gates pass", 0, true},
		{"smoke fails", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := loadAutoRollback(t)

			// Dry run.
			fake1 := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
			e1, _ := newEngine(t, fake1, WithSmokeRunner(fakeSmoke{code: tc.smokeCode}))
			r1, err := e1.Apply(ctx, ApplyRequest{Config: c})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			rep, err := e1.Verify(ctx, r1.ID)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}

			// Auto path, same inputs.
			fake2 := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
			e2, _ := newEngine(t, fake2, WithSmokeRunner(fakeSmoke{code: tc.smokeCode}))
			r2, err := e2.Apply(ctx, ApplyRequest{Config: c})
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			out, err := e2.VerifyOrRollback(ctx, r2.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
			if err != nil {
				t.Fatalf("VerifyOrRollback: %v", err)
			}

			if rep.OK != tc.wantOK {
				t.Errorf("dry run OK = %v, want %v (%+v)", rep.OK, tc.wantOK, rep.Gates)
			}
			// The dry run predicting "pass" must mean the auto path promotes, and
			// predicting "fail" must mean it rolls back. Any divergence here is the
			// gate paths drifting apart.
			if rep.OK == out.RolledBack {
				t.Errorf("dry run OK=%v but auto path rolledBack=%v — the paths disagree", rep.OK, out.RolledBack)
			}
		})
	}
}

// TestGatesFromConfig_HealthEnablement pins when the auto path runs the health
// gate: a configured health check, or auto-rollback being on.
func TestGatesFromConfig_HealthEnablement(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	for _, tc := range []struct {
		name string
		spec config.Rollback
		want bool
	}{
		{"health check configured", config.Rollback{HealthCheck: &config.HealthCheck{HTTP: "https://x/healthz"}}, true},
		{"auto rollback on", config.Rollback{Auto: true}, true},
		{"neither", config.Rollback{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &config.Config{}
			c.Spec.Rollback = tc.spec
			if got := e.gatesFromConfig(c).health; got != tc.want {
				t.Errorf("health enabled = %v, want %v", got, tc.want)
			}
		})
	}
}
