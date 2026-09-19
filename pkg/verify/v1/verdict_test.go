package verifyv1_test

import (
	"context"
	"testing"
	"time"

	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// TestAnInconclusiveCheckNeverAddsUpToAPass is §11.3's only MUST. A check that
// could not tell is not a check that said yes, and the one place the two could
// be confused is where several checks become one answer.
func TestAnInconclusiveCheckNeverAddsUpToAPass(t *testing.T) {
	got := verifyv1.Combine(verifyv1.VerdictPass, verifyv1.VerdictInconclusive, verifyv1.VerdictPass)
	if got == verifyv1.VerdictPass {
		t.Fatal("a set holding an inconclusive check added up to a pass")
	}
	if got != verifyv1.VerdictInconclusive {
		t.Errorf("the aggregate was %q, want %q", got, verifyv1.VerdictInconclusive)
	}
}

// TestOnlyAllPassesIsAPass is the same rule stated over every verdict there is,
// so a verdict added later cannot quietly join the pass path.
func TestOnlyAllPassesIsAPass(t *testing.T) {
	all := []verifyv1.Verdict{
		verifyv1.VerdictPass,
		verifyv1.VerdictFail,
		verifyv1.VerdictInconclusive,
		verifyv1.VerdictError,
		verifyv1.VerdictCancelled,
	}
	for _, v := range all {
		got := verifyv1.Combine(verifyv1.VerdictPass, v)
		if v == verifyv1.VerdictPass {
			if got != verifyv1.VerdictPass {
				t.Errorf("two passes added up to %q", got)
			}
			continue
		}
		if got == verifyv1.VerdictPass {
			t.Errorf("a pass alongside %q added up to a pass", v)
		}
	}
}

// TestNoChecksIsNotAPass keeps the empty case on the honest side of the MUST: a
// verification that ran nothing observed nothing, and "nothing said no" is not
// "something said yes".
func TestNoChecksIsNotAPass(t *testing.T) {
	if got := verifyv1.Combine(); got != verifyv1.VerdictInconclusive {
		t.Errorf("combining no checks gave %q, want %q", got, verifyv1.VerdictInconclusive)
	}
}

// TestTheAggregateIsTheMostBlockingVerdictPresent pins the order the report is
// rendered from. Any order satisfies the MUST; this one is chosen so the answer
// names the strongest thing that was actually observed.
func TestTheAggregateIsTheMostBlockingVerdictPresent(t *testing.T) {
	cases := []struct {
		name string
		in   []verifyv1.Verdict
		want verifyv1.Verdict
	}{
		{"a real no outranks a check that broke", []verifyv1.Verdict{verifyv1.VerdictError, verifyv1.VerdictFail}, verifyv1.VerdictFail},
		{"a broken check outranks an abandoned one", []verifyv1.Verdict{verifyv1.VerdictCancelled, verifyv1.VerdictError}, verifyv1.VerdictError},
		{"an abandoned check outranks one that could not tell", []verifyv1.Verdict{verifyv1.VerdictInconclusive, verifyv1.VerdictCancelled}, verifyv1.VerdictCancelled},
		{"every pass is a pass", []verifyv1.Verdict{verifyv1.VerdictPass, verifyv1.VerdictPass}, verifyv1.VerdictPass},
		{"an unknown verdict is not trusted to be a pass", []verifyv1.Verdict{verifyv1.VerdictPass, verifyv1.Verdict("weather")}, verifyv1.VerdictError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifyv1.Combine(tc.in...); got != tc.want {
				t.Errorf("Combine(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAnAbandonedContextHasItsOwnVerdict keeps the two ways a check can be cut
// short apart. A caller who walked away gets "cancelled"; a deadline that
// expired gets "inconclusive", because a backend that never answered in time is
// exactly the case the MUST exists for.
func TestAnAbandonedContextHasItsOwnVerdict(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := verifyv1.Interrupted(cancelled); got != verifyv1.VerdictCancelled {
		t.Errorf("a cancelled context gave %q, want %q", got, verifyv1.VerdictCancelled)
	}

	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	if got := verifyv1.Interrupted(expired); got != verifyv1.VerdictInconclusive {
		t.Errorf("an expired deadline gave %q, want %q", got, verifyv1.VerdictInconclusive)
	}
}

// TestALiveContextHasNoVerdictOfItsOwn: the check still has to do the work.
func TestALiveContextHasNoVerdictOfItsOwn(t *testing.T) {
	if got := verifyv1.Interrupted(context.Background()); got != "" {
		t.Errorf("a live context gave %q, want no verdict", got)
	}
}
