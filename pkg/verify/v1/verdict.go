package verifyv1

import (
	"context"
	"errors"
)

// Verdict is what one check concluded (§11.3). There are five, and only one of
// them lets a promotion through.
type Verdict string

const (
	// VerdictPass — the check ran and the thing it measured was acceptable.
	VerdictPass Verdict = "pass"
	// VerdictFail — the check ran and the thing it measured was not.
	VerdictFail Verdict = "fail"
	// VerdictInconclusive — the check ran and could not tell. A backend that
	// did not answer, a query with no data, a deadline that expired.
	VerdictInconclusive Verdict = "inconclusive"
	// VerdictError — the check itself broke, before it could measure anything.
	VerdictError Verdict = "error"
	// VerdictCancelled — the caller went away before the check finished.
	VerdictCancelled Verdict = "cancelled"
)

// blocking ranks the verdicts from "promote" to "do not promote". The order is
// what Combine reports, not what it enforces: a real no outranks a check that
// broke, because a check that ran and said no is a stronger statement than one
// that never ran; a check that broke outranks one the caller abandoned, and an
// abandoned check outranks one that simply could not tell.
var blocking = map[Verdict]int{
	VerdictPass:         0,
	VerdictInconclusive: 1,
	VerdictCancelled:    2,
	VerdictError:        3,
	VerdictFail:         4,
}

// Combine reduces the verdicts of several checks to the one a caller acts on.
//
// This function is §11.3's "inconclusive MUST NOT silently become pass" and
// nothing else: the aggregate is the most blocking verdict present, so a pass
// requires that every check passed. Combining nothing is inconclusive rather
// than a pass, because a verification that ran no checks observed nothing, and
// a verdict this package does not recognise is an error rather than a pass,
// since the one thing that must never be assumed is the permissive answer.
//
// What a non-pass verdict then costs — rollback, pause, promote anyway — is
// policy's decision (§11.4), not this package's.
func Combine(verdicts ...Verdict) Verdict {
	if len(verdicts) == 0 {
		return VerdictInconclusive
	}
	worst := VerdictPass
	for _, v := range verdicts {
		rank, known := blocking[v]
		if !known {
			return VerdictError
		}
		if rank > blocking[worst] {
			worst = v
		}
	}
	return worst
}

// Interrupted reports the verdict owed to a check whose context ended before it
// could answer, or "" while the context is still live. Verifiers call it the
// way targets call targetv2.Abandoned: a check that only notices the abort when
// its backend does will report whatever its cache last held.
//
// A cancelled caller and an expired deadline are kept apart on purpose. The
// first is a decision someone made; the second is the case the MUST exists for
// — a backend that never answered in time told us nothing.
func Interrupted(ctx context.Context) Verdict {
	err := ctx.Err()
	switch {
	case errors.Is(err, context.Canceled):
		return VerdictCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return VerdictInconclusive
	default:
		return ""
	}
}
