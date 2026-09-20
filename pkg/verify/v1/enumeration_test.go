package verifyv1_test

import (
	"testing"

	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

func TestTheVerdictsRunFromPromoteToDoNotPromote(t *testing.T) {
	t.Parallel()

	got := verifyv1.Verdicts()
	if len(got) == 0 {
		t.Fatal("Verdicts() is empty")
	}
	if got[0] != verifyv1.VerdictPass {
		t.Errorf("Verdicts() starts at %q; the only verdict that lets a promotion through should come first", got[0])
	}
	// Combine reports the most blocking verdict present. Folding the
	// enumeration through it pairwise says the order the enumeration publishes
	// is the order Combine enforces, so a verdict inserted in the wrong place
	// fails here rather than misleading whatever renders the list.
	for i := 1; i < len(got); i++ {
		if worst := verifyv1.Combine(got[i-1], got[i]); worst != got[i] {
			t.Errorf("Verdicts() has %q before %q, but combining them gives %q", got[i-1], got[i], worst)
		}
	}
}

func TestEveryVerdictInTheEnumerationIsOneCombineRecognises(t *testing.T) {
	t.Parallel()

	// Combine answers ERROR for a verdict it does not know, so a verdict that
	// combines with itself to anything else was never in the ranking.
	for _, v := range verifyv1.Verdicts() {
		if got := verifyv1.Combine(v, v); got != v {
			t.Errorf("Verdicts() lists %q, which Combine does not rank: combining it with itself gives %q", v, got)
		}
	}
}
