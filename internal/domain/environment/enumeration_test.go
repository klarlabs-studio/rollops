package environment

import "testing"

// The enumerations exist so a transport that has to name every value can ask
// rather than restate. What makes them worth having is that they are the list
// the package already validates against, so these tests are what holds the two
// together: publishing a vocabulary and enforcing it are one fact.

func TestEveryEnvironmentKindInTheEnumerationCanBeDeclared(t *testing.T) {
	t.Parallel()

	got := Kinds()
	if len(got) == 0 {
		t.Fatal("Kinds() is empty")
	}
	for _, k := range got {
		e := valid()
		e.Kind = k
		if err := e.Validate(); err != nil {
			t.Errorf("Kinds() lists %q, which Validate() rejects: %v", k, err)
		}
	}
}

func TestAnEnvironmentKindOutsideTheEnumerationIsRejected(t *testing.T) {
	t.Parallel()

	e := valid()
	e.Kind = Kind("ephemeral")
	if err := e.Validate(); err == nil {
		t.Error("Validate() accepted a kind the enumeration does not list")
	}
}

func TestOneOfTheDeclaredKindsIsProduction(t *testing.T) {
	t.Parallel()

	// IsProduction decides whether the strictest guardrails apply. If the kind
	// it compares against ever fell out of the enumeration, a transport would
	// publish a vocabulary with no way to say "production".
	var found bool
	for _, k := range Kinds() {
		e := valid()
		e.Kind = k
		found = found || e.IsProduction()
	}
	if !found {
		t.Error("no kind in Kinds() is production")
	}
}

func TestEveryPolicyModeInTheEnumerationCanBeBound(t *testing.T) {
	t.Parallel()

	got := PolicyModes()
	if len(got) == 0 {
		t.Fatal("PolicyModes() is empty")
	}
	for _, m := range got {
		e := valid()
		e.Policies = []PolicyBinding{{Name: "gate", Ref: "policy/gate", Mode: m}}
		if err := e.Validate(); err != nil {
			t.Errorf("PolicyModes() lists %q, which Validate() rejects: %v", m, err)
		}
	}
}

func TestAPolicyModeOutsideTheEnumerationIsRejected(t *testing.T) {
	t.Parallel()

	e := valid()
	e.Policies = []PolicyBinding{
		{Name: "gate", Ref: "policy/gate", Mode: PolicyMode("audit")},
	}
	if err := e.Validate(); err == nil {
		t.Error("Validate() accepted a policy mode the enumeration does not list")
	}
}
