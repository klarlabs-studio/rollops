// Package secrets resolves credentials at execution time from established
// vaults — never storing them locally. A resolved Secret redacts itself in any
// string/log context (String returns "***"); only Reveal exposes the value, and
// only the code that hands it to a target calls it. This is the boundary that
// keeps secret material out of logs, plans, diffs, and MCP responses.
package secrets

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Secret wraps a resolved credential so it cannot be accidentally logged.
type Secret struct {
	value string
}

// NewSecret wraps a raw value. Use only inside a Provider.
func NewSecret(v string) Secret { return Secret{value: v} }

// String redacts — Secret is safe to log or serialize.
func (s Secret) String() string { return "***" }

// GoString redacts in %#v contexts too.
func (s Secret) GoString() string { return "secrets.Secret(***)" }

// Reveal returns the raw value. Call only at the point of handing it to a target.
func (s Secret) Reveal() string { return s.value }

// IsZero reports an unset secret.
func (s Secret) IsZero() bool { return s.value == "" }

// Provider resolves a secret reference to a value at execution time.
type Provider interface {
	Resolve(ctx context.Context, ref string) (Secret, error)
}

// ErrNotFound indicates the reference does not resolve.
var ErrNotFound = fmt.Errorf("secrets: not found")

// EnvProvider resolves references from environment variables, optionally under
// a prefix. This is the workload-identity-friendly default: the orchestrator
// injects credentials into the process environment, nothing is on disk.
type EnvProvider struct {
	Prefix string
}

// Resolve maps ref to an env var: Prefix + uppercased ref with '-'/'/'→'_'.
func (p EnvProvider) Resolve(_ context.Context, ref string) (Secret, error) {
	key := p.Prefix + envKey(ref)
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return Secret{}, fmt.Errorf("%w: env %s", ErrNotFound, key)
	}
	return NewSecret(v), nil
}

func envKey(ref string) string {
	r := strings.ToUpper(ref)
	r = strings.ReplaceAll(r, "-", "_")
	r = strings.ReplaceAll(r, "/", "_")
	r = strings.ReplaceAll(r, ".", "_")
	return r
}

// Chain tries providers in order, returning the first hit.
type Chain struct {
	Providers []Provider
}

// Resolve walks the chain.
func (c Chain) Resolve(ctx context.Context, ref string) (Secret, error) {
	var last error
	for _, p := range c.Providers {
		s, err := p.Resolve(ctx, ref)
		if err == nil {
			return s, nil
		}
		last = err
	}
	if last == nil {
		last = ErrNotFound
	}
	return Secret{}, last
}
