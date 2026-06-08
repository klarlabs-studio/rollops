package engine

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/rollout"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

func TestFireDueSchedules_FiresPastNotFuture(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	_ = e.Schedule(ctx, rollout.ScheduledRollout{
		ID: "s-past", TargetRef: "demo/prod/app", DueAt: now.Add(-time.Hour),
		Desired: pt.Manifest{Kind: "fake", Checksum: "due"}, Initiator: rollout.Identity{Kind: "human", Name: "felix"},
	})
	_ = e.Schedule(ctx, rollout.ScheduledRollout{
		ID: "s-future", TargetRef: "demo/prod/web", DueAt: now.Add(time.Hour),
		Desired: pt.Manifest{Kind: "fake", Checksum: "later"},
	})

	fired, err := e.FireDueSchedules(ctx, now)
	if err != nil {
		t.Fatalf("FireDueSchedules: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("fired %d, want 1 (only the past-due)", len(fired))
	}
	if len(fake.applied) != 1 || fake.applied[0].Checksum != "due" {
		t.Errorf("wrong manifest deployed: %+v", fake.applied)
	}
	if string(fired[0].Phase) != "verifying" {
		t.Errorf("fired phase = %q", fired[0].Phase)
	}

	// Fired schedule must be removed (no re-fire), the future one remains.
	remaining, _ := db.DueSchedules(ctx, now.Add(2*time.Hour))
	if len(remaining) != 1 || remaining[0].TargetRef != "demo/prod/web" {
		t.Errorf("queue after fire = %+v, want only the future one", remaining)
	}
}

func TestFireDueSchedules_Empty(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	fired, err := e.FireDueSchedules(context.Background(), time.Now())
	if err != nil || len(fired) != 0 {
		t.Fatalf("empty queue: fired=%v err=%v", fired, err)
	}
}
