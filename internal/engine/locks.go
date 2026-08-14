package engine

import (
	"context"
	"errors"
	"sync"

	"go.klarlabs.de/rollops/internal/store"
)

// ErrTargetBusy is returned when a rollout is attempted against a target that
// another rollout is already operating on. Two rollouts never touch the same
// target concurrently.
var ErrTargetBusy = errors.New("engine: target busy: a rollout is already in progress")

// keyedLocks is a set of non-blocking advisory locks keyed by string (target
// ref, or repo for the reconciler). TryAcquire never blocks — a contended key
// fails fast so the caller can report busy rather than queue unboundedly.
type keyedLocks struct {
	mu   sync.Mutex
	held map[string]bool
}

func newKeyedLocks() *keyedLocks {
	return &keyedLocks{held: make(map[string]bool)}
}

// TryAcquire takes the lock for key if free, returning a release func and true.
// If the key is already held it returns nil and false without blocking.
func (k *keyedLocks) TryAcquire(key string) (release func(), ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.held[key] {
		return nil, false
	}
	k.held[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Lock()
			delete(k.held, key)
			k.mu.Unlock()
		})
	}, true
}

// acquireTarget takes the in-process lock and the Store lease for targetRef.
// Apply and Tick re-acquire per call and release when they return — occupancy
// between ticks is the deploying/paused phase, not a held lease. A second Apply
// against an in-flight canary is refused as ErrTargetBusy.
func (e *Engine) acquireTarget(ctx context.Context, targetRef string) (func(), bool, error) {
	localRelease, ok := e.locks.TryAcquire(targetRef)
	if !ok {
		return nil, false, nil
	}
	leases, ok := e.store.(store.LeaseStore)
	if !ok {
		return localRelease, true, nil
	}
	key := "target:" + targetRef
	acquired, err := leases.AcquireLease(ctx, key, e.owner, e.leaseTTL, e.now())
	if err != nil {
		localRelease()
		return nil, false, err
	}
	if !acquired {
		localRelease()
		return nil, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = leases.ReleaseLease(context.Background(), key, e.owner)
			localRelease()
		})
	}, true, nil
}
