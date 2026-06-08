package engine

import (
	"errors"
	"sync"
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
