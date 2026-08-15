package engine

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func TestFleetStatus_AggregatesLatestPerTarget(t *testing.T) {
	eng, db := newEngine(t, &fakeTarget{})
	ctx := context.Background()
	ts := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	seed := func(id, ref string, phase rollout.Phase, updated time.Time) {
		t.Helper()
		if err := db.SaveRollout(ctx, rollout.Rollout{
			ID: id, TargetRef: ref, Phase: phase, Strategy: rollout.StrategyRolling,
			Desired:   pt.Manifest{Kind: "fake", Checksum: "sha:" + id},
			CreatedAt: updated, UpdatedAt: updated,
		}); err != nil {
			t.Fatalf("SaveRollout %s: %v", id, err)
		}
	}
	// Newest-first list: later UpdatedAt wins as "latest" for a target.
	seed("old-east", "web@east", rollout.PhaseDeploying, ts.Add(-time.Hour))
	seed("east", "web@east", rollout.PhasePromoted, ts)
	seed("west", "web@west", rollout.PhaseDeploying, ts)
	seed("eu", "web@eu", rollout.PhaseRolledBack, ts)
	seed("await", "web@await", rollout.PhaseAwaitingApproval, ts)
	seed("other", "api@east", rollout.PhasePromoted, ts) // different set

	rep, err := eng.FleetStatus(ctx, "web")
	if err != nil {
		t.Fatalf("FleetStatus: %v", err)
	}
	if rep.Name != "web" || rep.Total != 4 || rep.Promoted != 1 || rep.Active != 1 || rep.Degraded != 1 || rep.Awaiting != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Members) != 4 {
		t.Fatalf("members = %d", len(rep.Members))
	}
	// Sorted by target ref.
	if rep.Members[0].Target != "web@await" || rep.Members[0].Phase != rollout.PhaseAwaitingApproval {
		t.Fatalf("first member = %+v", rep.Members[0])
	}
	if rep.Members[1].ID != "east" { // latest for east, not old-east
		t.Fatalf("east member = %+v", rep.Members[1])
	}
}

func TestFleetStatus_PrefixAtAndExact(t *testing.T) {
	eng, db := newEngine(t, &fakeTarget{})
	ctx := context.Background()
	ts := time.Now().UTC()
	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID: "1", TargetRef: "web@east", Phase: rollout.PhasePromoted, Strategy: rollout.StrategyRolling,
		Desired: pt.Manifest{Kind: "fake", Checksum: "sha:1"}, CreatedAt: ts, UpdatedAt: ts,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID: "2", TargetRef: "webextra@x", Phase: rollout.PhasePromoted, Strategy: rollout.StrategyRolling,
		Desired: pt.Manifest{Kind: "fake", Checksum: "sha:2"}, CreatedAt: ts, UpdatedAt: ts,
	}); err != nil {
		t.Fatal(err)
	}

	rep, err := eng.FleetStatus(ctx, "web@")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 1 || rep.Name != "web" {
		t.Fatalf("prefix filter: %+v", rep)
	}

	if _, err := eng.FleetStatus(ctx, ""); err == nil {
		t.Fatal("empty filter should fail")
	}
	if _, err := eng.FleetStatus(ctx, "missing"); err != nil {
		t.Fatalf("empty fleet is ok: %v", err)
	}
	empty, err := eng.FleetStatus(ctx, "missing")
	if err != nil || empty.Total != 0 || empty.Name != "missing" {
		t.Fatalf("empty = %+v err=%v", empty, err)
	}
}
