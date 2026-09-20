package secrets

import (
	"context"
	"errors"
	"fmt"

	"go.klarlabs.de/rollops/internal/domain/value"
)

// resolverFunc adapts a function to value.Resolver.
type resolverFunc func(name string) (string, error)

// ResolveSecret calls f.
func (f resolverFunc) ResolveSecret(name string) (string, error) { return f(name) }

// ValueResolver adapts a Provider to the value.Resolver that configuration
// references are resolved through, or reports that it was given nothing to
// resolve with.
//
// The context is bound here rather than taken per call because value.Resolver
// has none to take: the domain holds a secret's name and nothing about how long
// fetching it may run. So a resolver is built for one operation and lives as
// long as it does. The alternative — reaching for context.Background inside the
// adapter — compiles just as well and quietly discards cancellation, leaving a
// deployment nobody is waiting for still waiting on a vault.
//
// Reveal is called here because value.Ref.Resolve answers with a string, which
// is where Secret's protection necessarily ends. What keeps that from being a
// leak is who asks: the only caller is the target resolver, which hands the
// value straight to a substrate. Nothing between the two renders it, and the
// error path below is written so that a failure cannot carry it either
// (INV-012).
func ValueResolver(ctx context.Context, p Provider) (value.Resolver, error) {
	if p == nil {
		return nil, errors.New("secrets: no provider to resolve references with")
	}
	return resolverFunc(func(name string) (string, error) {
		s, err := p.Resolve(ctx, name)
		if err != nil {
			// The reference is named and the provider's error is wrapped, so
			// that ErrNotFound still unwraps: a caller telling "no such secret"
			// apart from "the vault is down" is deciding whether a retry could
			// help. Nothing of s is mentioned — a provider that found the
			// material and then failed is exactly the case a careless wrap
			// turns into a logged credential.
			return "", fmt.Errorf("secrets: resolving %q: %w", name, err)
		}
		return s.Reveal(), nil
	}), nil
}
