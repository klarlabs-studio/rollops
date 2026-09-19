package value

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLiteralCarriesItsValue(t *testing.T) {
	r := Literal("postgres")
	if r.IsSecret() {
		t.Error("a literal reported itself as a secret")
	}
	got, err := r.Resolve(refusingResolver{t})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "postgres" {
		t.Errorf("got %q, want %q", got, "postgres")
	}
}

func TestSecretResolvesThroughTheProvider(t *testing.T) {
	r := Secret("prod/database-url")
	if !r.IsSecret() {
		t.Error("a secret reference did not report itself as one")
	}
	got, err := r.Resolve(mapResolver{"prod/database-url": "postgres://real"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "postgres://real" {
		t.Errorf("got %q, want the resolved secret", got)
	}
}

func TestSecretResolutionFailsLoudly(t *testing.T) {
	_, err := Secret("prod/missing").Resolve(mapResolver{})
	if err == nil {
		t.Fatal("resolving an absent secret succeeded")
	}
	if strings.Contains(err.Error(), "postgres") {
		t.Error("the error carried a secret value")
	}
}

func TestResolveWithoutAProviderRefusesRatherThanReturningEmpty(t *testing.T) {
	if _, err := Secret("prod/database-url").Resolve(nil); !errors.Is(err, ErrNoResolver) {
		t.Errorf("got %v, want ErrNoResolver", err)
	}
}

// INV-012: a secret must not appear in normalized config, plans, events, logs
// or API responses. The reference is what travels; the value is fetched at the
// moment of use and never stored on the reference itself.
func TestASecretReferenceNeverRendersItsValue(t *testing.T) {
	r := Secret("prod/database-url")
	if _, err := r.Resolve(mapResolver{"prod/database-url": "postgres://real"}); err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		r.String(),
		fmt.Sprintf("%v", r),
		fmt.Sprintf("%+v", r),
		fmt.Sprintf("%#v", r),
	} {
		if strings.Contains(rendered, "postgres://real") {
			t.Errorf("a rendering leaked the secret value: %q", rendered)
		}
		if !strings.Contains(rendered, "prod/database-url") {
			t.Errorf("rendering %q does not name the reference", rendered)
		}
	}
}

func TestSerialisationKeepsTheSecretOut(t *testing.T) {
	encoded, err := json.Marshal(map[string]Ref{
		"DATABASE_URL": Secret("prod/database-url"),
		"REGION":       Literal("eu-central-1"),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "postgres") {
		t.Errorf("encoded form carried a secret value: %s", encoded)
	}

	var back map[string]Ref
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back["DATABASE_URL"].IsSecret() {
		t.Error("a secret reference decoded as a literal")
	}
	if back["DATABASE_URL"].SecretName() != "prod/database-url" {
		t.Errorf("secret name = %q", back["DATABASE_URL"].SecretName())
	}
	if back["REGION"].IsSecret() {
		t.Error("a literal decoded as a secret")
	}
}

// A literal is config, not a credential, so it may be rendered — but only a
// literal.
func TestSecretNameIsEmptyForALiteral(t *testing.T) {
	if got := Literal("x").SecretName(); got != "" {
		t.Errorf("SecretName() = %q for a literal, want empty", got)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		ref  Ref
		ok   bool
	}{
		{"literal", Literal("x"), true},
		{"empty literal", Literal(""), true},
		{"secret", Secret("prod/db"), true},
		{"secret with no name", Secret("  "), false},
		{"zero value", Ref{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ref.Validate()
			if c.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// The zero Ref refers to nothing. Every operation on it must say so rather
// than behave as an empty literal, which would deploy a blank value.
func TestTheZeroRefIsInert(t *testing.T) {
	var r Ref
	if _, err := r.Resolve(mapResolver{}); err == nil {
		t.Error("the zero Ref resolved")
	}
	if got := r.String(); got == "" {
		t.Error("the zero Ref rendered as an empty string")
	}
	if _, err := json.Marshal(r); err == nil {
		t.Error("the zero Ref encoded; it would decode as something valid")
	}
}

func TestDecodingRejectsAMalformedReference(t *testing.T) {
	for _, in := range []string{
		`{}`,
		`{"kind":"rumour","value":"x"}`,
		`{"kind":"secret"}`,
		`[]`,
	} {
		var r Ref
		if err := json.Unmarshal([]byte(in), &r); err == nil {
			t.Errorf("Unmarshal(%s) accepted a malformed reference", in)
		}
	}
}

type mapResolver map[string]string

func (m mapResolver) ResolveSecret(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("no secret named %q", name)
	}
	return v, nil
}

// refusingResolver fails the test if a literal ever reaches the secret
// provider — a round trip through a vault for a value we already hold would
// be both slow and a needless widening of what the provider sees.
type refusingResolver struct{ t *testing.T }

func (r refusingResolver) ResolveSecret(name string) (string, error) {
	r.t.Helper()
	r.t.Errorf("a literal was resolved through the secret provider as %q", name)
	return "", nil
}
