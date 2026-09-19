package identity

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// Generator is the port every identifier comes from. It exists so a test can
// pin the ids a plan or an event stream produces (spec §29); nothing outside
// this file calls uuid.NewV7 (ADR-0001).
type Generator interface {
	// NewID returns a canonical UUID string. Callers add the type prefix.
	NewID() (string, error)
}

// GeneratorFunc adapts a function to Generator.
type GeneratorFunc func() (string, error)

func (f GeneratorFunc) NewID() (string, error) { return f() }

// NewGenerator returns the production generator: UUIDv7, so ids sort by
// creation time as text and inserts stay append-mostly in the store.
func NewGenerator() Generator {
	return GeneratorFunc(func() (string, error) {
		id, err := uuid.NewV7()
		if err != nil {
			return "", fmt.Errorf("identity: generate uuidv7: %w", err)
		}
		return id.String(), nil
	})
}

// NewFixedGenerator returns a generator that yields the same id forever. Use it
// where a test asserts an exact identifier; use NewSequenceGenerator where it
// needs several distinct ones.
func NewFixedGenerator(id string) Generator {
	return GeneratorFunc(func() (string, error) { return id, nil })
}

// NewSequenceGenerator returns a deterministic generator whose ids increase in
// both text and creation order, so a test can assert ordering without the
// nondeterminism of a real clock.
func NewSequenceGenerator() Generator {
	var (
		mu sync.Mutex
		n  uint64
	)
	return GeneratorFunc(func() (string, error) {
		mu.Lock()
		n++
		v := n
		mu.Unlock()
		// Version 7, variant 10 — a well-formed UUID whose remaining bits are
		// the counter, zero-padded so text order matches numeric order.
		return fmt.Sprintf("00000000-0000-7000-8000-%012d", v), nil
	})
}
