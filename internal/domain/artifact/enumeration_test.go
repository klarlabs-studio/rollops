package artifact_test

import (
	"slices"
	"testing"

	"go.klarlabs.de/rollops/internal/domain/artifact"
)

func TestTheKindEnumerationIsTheSetArtifactsAreValidatedAgainst(t *testing.T) {
	t.Parallel()

	got := artifact.Kinds()
	if len(got) == 0 {
		t.Fatal("Kinds() is empty")
	}
	if !slices.IsSorted(got) {
		t.Errorf("Kinds() is not sorted, so a caller numbering it gets a different answer each run: %v", got)
	}
	if deduped := slices.Compact(slices.Clone(got)); len(deduped) != len(got) {
		t.Errorf("Kinds() repeats a value: %v", got)
	}
	for _, k := range got {
		if !k.Valid() {
			t.Errorf("Kinds() lists %q, which Valid() rejects", k)
		}
	}
}

func TestAKindOutsideTheEnumerationIsNotValid(t *testing.T) {
	t.Parallel()

	if artifact.Kind("container_image").Valid() {
		t.Error("Valid() accepted a kind the enumeration does not list")
	}
}
