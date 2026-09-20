package secrets_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/secrets"
)

// The reference and the material behind it are both literals so that a leak can
// be found by searching an error's text for the one that must never appear in
// it. The value is deliberately not shaped like a credential: what the
// assertions need is a string that could only have come from here, and a
// real-looking one would be a secret scanner's finding for as long as the file
// exists.
const (
	ref      = "prod/kubeconfig"
	material = "THE-VALUE-NO-ERROR-MAY-REPEAT"
)

type provider struct {
	give string
	err  error
	seen string
	// ctx records what the adapter passed, which is the only way to tell a
	// bound context from a fresh one.
	ctx context.Context
}

func (p *provider) Resolve(ctx context.Context, r string) (secrets.Secret, error) {
	p.seen, p.ctx = r, ctx
	if p.err != nil {
		return secrets.Secret{}, p.err
	}
	return secrets.NewSecret(p.give), nil
}

func TestAResolverNeedsAProvider(t *testing.T) {
	t.Parallel()

	if _, err := secrets.ValueResolver(context.Background(), nil); err == nil {
		t.Fatal("a resolver with nothing behind it was accepted; it would fail on the first deployment instead of at startup")
	}
}

func TestASecretReachesTheDomainAsItsValue(t *testing.T) {
	t.Parallel()

	p := &provider{give: material}
	r, err := secrets.ValueResolver(context.Background(), p)
	if err != nil {
		t.Fatalf("ValueResolver: %v", err)
	}

	got, err := value.Secret(ref).Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != material {
		t.Errorf("resolved %q, want %q", got, material)
	}
	if p.seen != ref {
		t.Errorf("provider asked for %q, want %q", p.seen, ref)
	}
}

func TestAFailureNamesTheReferenceAndNotTheValue(t *testing.T) {
	t.Parallel()

	// The provider fails, but it is one that has the material — a chain member
	// that found the secret and then could not return it is exactly the case
	// where a careless adapter puts the value in the error it wraps.
	p := &provider{give: material, err: errors.New("vault unreachable")}
	r, err := secrets.ValueResolver(context.Background(), p)
	if err != nil {
		t.Fatalf("ValueResolver: %v", err)
	}

	got, err := r.ResolveSecret(ref)
	if err == nil {
		t.Fatal("a provider that failed resolved anyway")
	}
	if got != "" {
		t.Errorf("a failed lookup returned %q; a caller that ignores the error would deploy with it", got)
	}
	if !strings.Contains(err.Error(), ref) {
		t.Errorf("error %q does not name the reference that failed", err)
	}
	if strings.Contains(err.Error(), material) {
		t.Fatal("the secret is in the error text; INV-012 puts errors in logs")
	}
}

func TestAMissingSecretIsStillAMissingSecret(t *testing.T) {
	t.Parallel()

	p := &provider{err: secrets.ErrNotFound}
	r, err := secrets.ValueResolver(context.Background(), p)
	if err != nil {
		t.Fatalf("ValueResolver: %v", err)
	}

	// Wrapping must preserve the sentinel: a composition root distinguishing
	// "no such secret" from "the vault is down" reads this, and the two differ
	// by whether retrying could help.
	if _, err := r.ResolveSecret(ref); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("error %v does not unwrap to ErrNotFound", err)
	}
}

func TestTheOperationsContextReachesTheProvider(t *testing.T) {
	t.Parallel()

	// value.Resolver takes no context, so the adapter must carry one. Proving
	// it is the caller's rather than a fresh Background is what keeps a
	// cancelled deployment from waiting on a vault nobody needs an answer from.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &provider{give: material}
	r, err := secrets.ValueResolver(ctx, p)
	if err != nil {
		t.Fatalf("ValueResolver: %v", err)
	}
	if _, err := r.ResolveSecret(ref); err != nil {
		t.Fatalf("ResolveSecret: %v", err)
	}
	if p.ctx.Err() == nil {
		t.Error("the provider was given a context that was not the caller's")
	}
}

func TestALiteralNeverReachesTheProvider(t *testing.T) {
	t.Parallel()

	// value.Ref resolves a literal itself. Asserting it here pins the division
	// of labour: an adapter that also handled literals would be a second place
	// deciding what a reference means.
	p := &provider{give: material}
	r, err := secrets.ValueResolver(context.Background(), p)
	if err != nil {
		t.Fatalf("ValueResolver: %v", err)
	}

	got, err := value.Literal("plain").Resolve(r)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "plain" {
		t.Errorf("resolved %q, want %q", got, "plain")
	}
	if p.seen != "" {
		t.Errorf("the provider was asked for %q; a literal is not a secret", p.seen)
	}
}
