// Package value models a configured value that is either a literal or a
// reference to a secret.
//
// A Ref never holds secret material. Resolution happens at the moment of use
// and the result is handed to the caller, not stored back — so a secret cannot
// reach normalized config, a plan, an event, a log or an API response by
// travelling inside the value that referenced it (INV-011).
package value

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNoResolver reports an attempt to resolve a secret with no provider
// configured. It is an error rather than an empty string because a deployment
// that silently receives "" for a database URL fails later and less clearly.
var ErrNoResolver = errors.New("value: no secret resolver configured")

type kind string

const (
	kindLiteral kind = "literal"
	kindSecret  kind = "secret"
)

// Resolver fetches secret material by name. Implementations live in adapters;
// the domain only ever holds the name.
type Resolver interface {
	ResolveSecret(name string) (string, error)
}

// Ref is a configured value. The zero Ref refers to nothing.
type Ref struct {
	kind    kind
	literal string
	secret  string
}

// Literal returns a reference to an inline value.
func Literal(v string) Ref { return Ref{kind: kindLiteral, literal: v} }

// Secret returns a reference to secret material held by the provider.
func Secret(name string) Ref { return Ref{kind: kindSecret, secret: name} }

// IsSecret reports whether resolving this reference requires the provider.
func (r Ref) IsSecret() bool { return r.kind == kindSecret }

// SecretName returns the name of the referenced secret, or "" for a literal.
func (r Ref) SecretName() string {
	if r.kind != kindSecret {
		return ""
	}
	return r.secret
}

// Validate reports whether the reference can be resolved.
func (r Ref) Validate() error {
	switch r.kind {
	case kindLiteral:
		return nil
	case kindSecret:
		if strings.TrimSpace(r.secret) == "" {
			return errors.New("value: secret reference has no name")
		}
		return nil
	default:
		return errors.New("value: reference is neither a literal nor a secret")
	}
}

// Resolve returns the value. A literal is returned directly rather than sent
// through the provider: a round trip for a value already in hand would widen
// what the provider is asked for to no purpose.
func (r Ref) Resolve(with Resolver) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	if r.kind == kindLiteral {
		return r.literal, nil
	}
	if with == nil {
		return "", fmt.Errorf("%w: cannot resolve %q", ErrNoResolver, r.secret)
	}
	v, err := with.ResolveSecret(r.secret)
	if err != nil {
		// The provider's error is wrapped by name, never by value.
		return "", fmt.Errorf("value: resolving secret %q: %w", r.secret, err)
	}
	return v, nil
}

// String renders the reference. A secret renders as its name, which is the
// most that may ever appear in a log or an API response.
func (r Ref) String() string {
	switch r.kind {
	case kindLiteral:
		return r.literal
	case kindSecret:
		return "secretRef:" + r.secret
	default:
		return "<unset>"
	}
}

// wire is the serialized shape. Secret material has no field to travel in.
type wire struct {
	Kind      kind   `json:"kind"`
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secretRef,omitempty"`
}

func (r Ref) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(wire{Kind: r.kind, Value: r.literal, SecretRef: r.secret})
}

func (r *Ref) UnmarshalJSON(b []byte) error {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	next := Ref{kind: w.Kind, literal: w.Value, secret: w.SecretRef}
	if err := next.Validate(); err != nil {
		return err
	}
	*r = next
	return nil
}
