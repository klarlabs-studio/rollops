package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store"
	"go.klarlabs.de/rollops/pkg/target"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "rollops.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func sampleRollout(id string, phase rollout.Phase) rollout.Rollout {
	ts := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	return rollout.Rollout{
		ID:         id,
		TargetRef:  "petmed/prod/api",
		Phase:      phase,
		Strategy:   rollout.StrategyCanary,
		Desired:    target.Manifest{Kind: "ssh", Spec: []byte(`{"host":"x"}`), Checksum: "sha:abc"},
		RiskScore:  0.42,
		Initiator:  rollout.Identity{Kind: "agent", Name: "nomi"},
		StepIndex:  2,
		StepTotal:  3,
		StepWeight: 50,
		CreatedAt:  ts,
		UpdatedAt:  ts,
	}
}

func TestOpen_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()
	db2, err := Open(path) // re-run migrations on existing db
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	db2.Close()
}

func TestStore_Interface(t *testing.T) {
	var _ store.Store = openTemp(t) // compile-time: *Store satisfies store.Store
}

func TestSaveLoadRollout_RoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	in := sampleRollout("r1", rollout.PhaseDeploying)
	in.Note = "database rollback: succeeded"
	if err := db.SaveRollout(ctx, in); err != nil {
		t.Fatalf("SaveRollout: %v", err)
	}
	got, err := db.LoadRollout(ctx, "r1")
	if err != nil {
		t.Fatalf("LoadRollout: %v", err)
	}
	if got.ID != in.ID || got.TargetRef != in.TargetRef || got.Phase != in.Phase {
		t.Errorf("core fields mismatch: %+v", got)
	}
	if got.Strategy != in.Strategy || got.RiskScore != in.RiskScore {
		t.Errorf("strategy/score mismatch: %+v", got)
	}
	if got.StepIndex != 2 || got.StepTotal != 3 || got.StepWeight != 50 {
		t.Errorf("step progress mismatch: %+v", got)
	}
	if got.Initiator.Kind != in.Initiator.Kind || got.Initiator.Name != in.Initiator.Name {
		t.Errorf("initiator = %+v want %+v", got.Initiator, in.Initiator)
	}
	if got.Desired.Kind != "ssh" || string(got.Desired.Spec) != `{"host":"x"}` || got.Desired.Checksum != "sha:abc" {
		t.Errorf("manifest mismatch: %+v", got.Desired)
	}
	if !got.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("createdAt = %v want %v", got.CreatedAt, in.CreatedAt)
	}
	if got.Note != "database rollback: succeeded" {
		t.Errorf("note = %q, want it persisted on the row", got.Note)
	}
}

func TestLoadRollout_NotFound(t *testing.T) {
	db := openTemp(t)
	_, err := db.LoadRollout(context.Background(), "nope")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestSaveRollout_UpsertsAndRecordsHistory(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.SaveRollout(ctx, sampleRollout("r1", rollout.PhaseValidating)); err != nil {
		t.Fatal(err)
	}
	next := sampleRollout("r1", rollout.PhaseDeploying)
	next.Note = "analysis passed"
	if err := db.SaveRollout(ctx, next); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadRollout(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != rollout.PhaseDeploying {
		t.Errorf("upsert: phase = %q want deploying", got.Phase)
	}
	hist, err := db.History(ctx, "petmed/prod/api")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history len = %d want 2", len(hist))
	}
	if hist[0].Phase != rollout.PhaseDeploying { // newest first
		t.Errorf("history[0].phase = %q want deploying (newest first)", hist[0].Phase)
	}
	if hist[0].Note != "analysis passed" {
		t.Errorf("history[0].note = %q", hist[0].Note)
	}
}

func TestLease_AcquireRenewReleaseAndExpire(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	ok, err := db.AcquireLease(ctx, "target:api", "a", time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("first acquire ok=%v err=%v", ok, err)
	}
	ok, err = db.AcquireLease(ctx, "target:api", "b", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("different owner must not acquire unexpired lease")
	}
	ok, err = db.AcquireLease(ctx, "target:api", "a", time.Minute, now.Add(2*time.Second))
	if err != nil || !ok {
		t.Fatalf("same owner renew ok=%v err=%v", ok, err)
	}
	if err := db.ReleaseLease(ctx, "target:api", "b"); err != nil {
		t.Fatal(err)
	}
	ok, _ = db.AcquireLease(ctx, "target:api", "b", time.Minute, now.Add(3*time.Second))
	if ok {
		t.Fatal("non-owner release must not free lease")
	}
	if err := db.ReleaseLease(ctx, "target:api", "a"); err != nil {
		t.Fatal(err)
	}
	ok, _ = db.AcquireLease(ctx, "target:api", "b", time.Minute, now.Add(4*time.Second))
	if !ok {
		t.Fatal("owner release should free lease")
	}

	ok, _ = db.AcquireLease(ctx, "target:db", "a", time.Second, now)
	if !ok {
		t.Fatal("lease target:db")
	}
	ok, _ = db.AcquireLease(ctx, "target:db", "b", time.Minute, now.Add(2*time.Second))
	if !ok {
		t.Fatal("expired lease should be acquirable by another owner")
	}
}

func TestObservedState_RoundTrip(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.ObservedState(ctx, "petmed/prod/api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound before any observe, got: %v", err)
	}
	ts := rollout.TargetState{
		TargetRef:  "petmed/prod/api",
		Observed:   target.Fingerprint{Value: "fp1", Meta: map[string]string{"rev": "9"}},
		ObservedAt: time.Now().UTC(),
	}
	if err := db.SaveObservedState(ctx, ts); err != nil {
		t.Fatalf("SaveObservedState: %v", err)
	}
	fp, err := db.ObservedState(ctx, "petmed/prod/api")
	if err != nil {
		t.Fatalf("ObservedState: %v", err)
	}
	if fp.Value != "fp1" || fp.Meta["rev"] != "9" {
		t.Errorf("fingerprint mismatch: %+v", fp)
	}
	// upsert
	ts.Observed.Value = "fp2"
	if err := db.SaveObservedState(ctx, ts); err != nil {
		t.Fatal(err)
	}
	fp, _ = db.ObservedState(ctx, "petmed/prod/api")
	if fp.Value != "fp2" {
		t.Errorf("upsert observed: %q want fp2", fp.Value)
	}
}

func TestObservedFingerprints_BulkRead(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for ref, val := range map[string]string{"a/prod/x": "fpa", "b/prod/y": "fpb"} {
		if err := db.SaveObservedState(ctx, rollout.TargetState{TargetRef: ref, Observed: target.Fingerprint{Value: val}, ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ObservedFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a/prod/x"] != "fpa" || got["b/prod/y"] != "fpb" {
		t.Errorf("bulk fingerprints = %v", got)
	}
	// A never-observed target is simply absent.
	if _, ok := got["c/prod/z"]; ok {
		t.Error("unobserved target must not appear")
	}
}

func TestSchedules_DueFiltering(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	past := rollout.ScheduledRollout{ID: "s-past", TargetRef: "a", DueAt: now.Add(-time.Hour), Desired: target.Manifest{Kind: "ssh"}, Initiator: rollout.Identity{Kind: "human", Name: "felix"}}
	future := rollout.ScheduledRollout{ID: "s-future", TargetRef: "b", DueAt: now.Add(time.Hour), Desired: target.Manifest{Kind: "ftp"}}
	if err := db.Schedule(ctx, past); err != nil {
		t.Fatal(err)
	}
	if err := db.Schedule(ctx, future); err != nil {
		t.Fatal(err)
	}
	due, err := db.DueSchedules(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "s-past" {
		t.Fatalf("DueSchedules = %+v, want only s-past", due)
	}
	if due[0].Initiator.Name != "felix" || due[0].Desired.Kind != "ssh" {
		t.Errorf("schedule fields not round-tripped: %+v", due[0])
	}
}
