package deployment_test

import (
	"slices"
	"testing"

	"go.klarlabs.de/rollops/internal/domain/deployment"
)

// The enumerations exist so a transport that has to name every value — a proto
// enum, a JSON schema, a CLI's help text — can ask rather than restate. What
// makes them worth having is that they cannot be a stale copy: these tests say
// the enumeration and the package's own validity check are the same list, so
// adding a value to one without the other fails here.

func TestEveryStatusInTheEnumerationIsAStatusTheMachineKnows(t *testing.T) {
	t.Parallel()

	got := deployment.Statuses()
	if len(got) == 0 {
		t.Fatal("Statuses() is empty")
	}
	for _, s := range got {
		if !s.Valid() {
			t.Errorf("Statuses() lists %q, which Valid() rejects", s)
		}
	}
}

func TestTheStatusEnumerationHasNoDuplicatesAndIsStablyOrdered(t *testing.T) {
	t.Parallel()

	got := deployment.Statuses()
	if !slices.IsSorted(got) {
		t.Errorf("Statuses() is not sorted, so a caller numbering it gets a different answer each run: %v", got)
	}
	if deduped := slices.Compact(slices.Clone(got)); len(deduped) != len(got) {
		t.Errorf("Statuses() repeats a value: %v", got)
	}
}

func TestEveryTerminalStatusIsAlsoAStatus(t *testing.T) {
	t.Parallel()

	all := deployment.Statuses()
	for _, s := range deployment.TerminalStatuses() {
		if !slices.Contains(all, s) {
			t.Errorf("%q ends a deployment but is missing from Statuses()", s)
		}
	}
}

func TestEveryStrategyInTheEnumerationIsAStrategyTheSystemKnows(t *testing.T) {
	t.Parallel()

	got := deployment.Strategies()
	if len(got) == 0 {
		t.Fatal("Strategies() is empty")
	}
	for _, s := range got {
		if !s.Valid() {
			t.Errorf("Strategies() lists %q, which Valid() rejects", s)
		}
	}
}

func TestAStrategyOutsideTheEnumerationIsNotValid(t *testing.T) {
	t.Parallel()

	// The point is that Valid() reads the enumeration rather than keeping its
	// own list. A strategy nobody declared must be refused by both.
	if deployment.Strategy("waterfall").Valid() {
		t.Error("Valid() accepted a strategy the enumeration does not list")
	}
}
