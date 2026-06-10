package engine

import (
	"context"
	"testing"
	"time"
)

func TestKeyedLocks_MutualExclusion(t *testing.T) {
	k := newKeyedLocks()
	rel, ok := k.TryAcquire("a")
	if !ok {
		t.Fatal("first acquire should succeed")
	}
	if _, ok := k.TryAcquire("a"); ok {
		t.Fatal("second acquire of held key must fail")
	}
	if _, ok := k.TryAcquire("b"); !ok {
		t.Fatal("different key must be independent")
	}
	rel()
	if _, ok := k.TryAcquire("a"); !ok {
		t.Fatal("key must be reacquirable after release")
	}
}

func TestKeyedLocks_ReleaseIdempotent(t *testing.T) {
	k := newKeyedLocks()
	rel, _ := k.TryAcquire("a")
	rel()
	rel() // must not panic or over-delete
	if _, ok := k.TryAcquire("a"); !ok {
		t.Fatal("key should be free")
	}
}

// While a target is locked, Apply against it must fail fast with ErrTargetBusy.
func TestApply_TargetBusy(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	c := loadConfig(t)

	release, ok := e.locks.TryAcquire(c.Spec.Target.Ref)
	if !ok {
		t.Fatal("precondition: acquire target lock")
	}
	defer release()

	_, err := e.Apply(context.Background(), ApplyRequest{Config: c})
	if err != ErrTargetBusy {
		t.Fatalf("err = %v, want ErrTargetBusy", err)
	}
	if len(fake.applied) != 0 {
		t.Fatal("busy target must not be applied to")
	}
}

func TestApply_TargetBusyAcrossEngineInstances(t *testing.T) {
	fake := &fakeTarget{}
	e1, db := newEngine(t, fake, WithLeaseOwner("one"), WithLeaseTTL(time.Minute))
	e2 := *e1
	e2.locks = newKeyedLocks()
	e2.owner = "two"

	c := loadConfig(t)
	release, ok, err := e1.acquireTarget(context.Background(), c.Spec.Target.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("precondition: acquire shared target lease")
	}
	defer release()
	_ = db

	_, err = e2.Apply(context.Background(), ApplyRequest{Config: c})
	if err != ErrTargetBusy {
		t.Fatalf("err = %v, want ErrTargetBusy", err)
	}
	if len(fake.applied) != 0 {
		t.Fatal("busy target must not be applied to")
	}
}
