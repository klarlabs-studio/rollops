package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// 2026-10-24: 14 vorhut rollouts were recorded as rolled-back with empty
// notes. The reason (API server refusing connections) reached only the
// container log, which rotated away within hours. The rollout row is the
// durable record, so it must carry the reason.
func TestApply_TargetFailure_KeepsTheReasonOnTheRollout(t *testing.T) {
	fake := &fakeTarget{applyErr: errors.New("kubectl apply -f -: dial tcp 10.43.0.1:443: connect: connection refused")}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err == nil {
		t.Fatal("expected apply error")
	}
	got, _ := db.LoadRollout(ctx, r.ID)
	if got.Phase != rollout.PhaseRolledBack {
		t.Fatalf("phase = %q, want rolled-back", got.Phase)
	}
	if !strings.Contains(got.Note, "connection refused") {
		t.Errorf("rollout note = %q, want the failure reason", got.Note)
	}
	hist, _ := db.History(ctx, got.TargetRef)
	// History is newest first.
	if len(hist) == 0 || !strings.Contains(hist[0].Note, "connection refused") {
		t.Errorf("latest history entry %+v does not carry the reason", hist)
	}
}

func TestApplyScheduled_TargetFailure_KeepsTheReasonOnTheRollout(t *testing.T) {
	fake := &fakeTarget{applyErr: errors.New("connection refused")}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	r, err := e.applyScheduled(ctx, rollout.ScheduledRollout{
		ID:        "sc-1",
		TargetRef: "petmed/prod/api",
		Desired:   pt.Manifest{Kind: "fake", Checksum: "sha:new"},
		Initiator: rollout.Identity{Kind: "human", Name: "felix"},
	})
	if err == nil {
		t.Fatal("expected apply error")
	}
	got, _ := db.LoadRollout(ctx, r.ID)
	if !strings.Contains(got.Note, "connection refused") {
		t.Errorf("rollout note = %q, want the failure reason", got.Note)
	}
}
