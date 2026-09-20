package identity_test

import (
	"testing"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

func TestEveryPrincipalTypeInTheEnumerationCanBeAttributed(t *testing.T) {
	t.Parallel()

	got := identity.PrincipalTypes()
	if len(got) == 0 {
		t.Fatal("PrincipalTypes() is empty")
	}
	for _, ty := range got {
		p := identity.Principal{ID: "u-1", Type: ty}
		if err := p.Validate(); err != nil {
			t.Errorf("PrincipalTypes() lists %q, which Validate() rejects: %v", ty, err)
		}
	}
}

func TestAPrincipalTypeOutsideTheEnumerationCannotBeAttributed(t *testing.T) {
	t.Parallel()

	err := identity.Principal{ID: "u-1", Type: identity.PrincipalType("robot")}.Validate()
	if err == nil {
		t.Error("Validate() attributed a mutation to a principal type the enumeration does not list")
	}
}
