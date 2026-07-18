package engine

import (
	"context"
	"testing"

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
	if _, err := e.Promote(ctx, r.ID); err != nil {
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
	if _, err := e.Promote(ctx, r.ID); err == nil {
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

// TestPromote_SkipsHealthGate documents the deliberate asymmetry: Verify is the
// full dry run (health + smoke + analysis), while Promote gates on smoke and
// analysis only, so an unhealthy target does not block a manual promotion.
func TestPromote_SkipsHealthGate(t *testing.T) {
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
	// Verify sees the unhealthy target...
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK {
		t.Fatal("verify should report the unhealthy target")
	}
	// ...but Promote does not gate on health.
	pr, err := e.Promote(ctx, r.ID)
	if err != nil {
		t.Fatalf("promote must not gate on health: %v", err)
	}
	if pr.Phase != rollout.PhasePromoted {
		t.Errorf("phase = %q, want promoted", pr.Phase)
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
