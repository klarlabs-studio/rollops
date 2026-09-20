// Package verification records what the checks said about a deployment.
//
// A run is the record, not the mechanism. Which verifiers ran, how they were
// configured and what they talked to are an adapter's concern; what is stored
// here is the answer each one gave and when the whole thing happened, so that a
// promotion made on the strength of a verdict can be read back long after the
// verifier that produced it was reconfigured.
//
// This is the one domain package that imports pkg/verify/v1, and deliberately.
// The verdict vocabulary and the result shape are the domain's own — they are
// published there so that a plugin author can speak them (§11.1), not because
// they belong to the plugin layer. Restating them here would leave two copies
// of one fact, and `Combine` on the wrong side of the boundary that §11.3's
// "inconclusive MUST NOT silently become pass" is enforced at. INV-006 and
// INV-007 ask that the domain not depend on a transport or a provider SDK;
// verifyv1 is neither, and imports nothing but context and time.
//
// A run holds what the event log does not: the reason a verifier gave and the
// evidence it pointed at. The split is not about how sensitive they are but
// about what can be undone. An event is append-only and can never be corrected
// (INV-013), so a credential that arrives in an evidence URI is there for good;
// a run is a record, and a record can be dropped. The unrecoverable side of
// that line carries only verdicts and measurements.
package verification

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// Check is one verifier's answer kept beside the verifier that gave it. The two
// travel together because a verdict is only actionable if you know whose it is:
// an operator reading a fail has to know which check to go and look at.
type Check struct {
	Verifier verifyv1.VerifierMetadata
	Result   verifyv1.VerificationResult
}

// Run is one whole verification of one deployment — every check that was asked,
// and what each answered.
//
// It is written once, when the checks have answered, and never updated. A run
// states what was observed in a window that has closed; editing one would
// rewrite an observation, which is the same reason an approval has no Update.
type Run struct {
	ID           identity.VerificationRunID
	DeploymentID identity.DeploymentID

	// PlanID is what the checks were observing. A verdict that cannot be tied
	// to a plan cannot be read back as evidence for the promotion it allowed.
	PlanID identity.PlanID

	Checks []Check

	// StartedAt is when the checks were dispatched and FinishedAt when the last
	// one answered. Both are recorded because the window is what makes a
	// measurement interpretable — a Prometheus query over ten minutes says
	// something different from a probe that returned instantly.
	StartedAt  time.Time
	FinishedAt time.Time

	Actor identity.Principal
}

// Verdict is the one answer a caller acts on, derived from the checks rather
// than stored beside them.
//
// Two copies of the same fact can disagree, and nothing in a stored row says
// which of the two to believe. Deriving also means a run loaded from a store
// that predates a change to how verdicts combine is read under today's rules,
// which is the safe direction: `Combine` only ever becomes more blocking.
func (r Run) Verdict() verifyv1.Verdict {
	verdicts := make([]verifyv1.Verdict, len(r.Checks))
	for i, c := range r.Checks {
		verdicts[i] = c.Result.Verdict
	}
	return verifyv1.Combine(verdicts...)
}

// New stamps identity, attribution and the settling time onto r.
//
// The clock gives FinishedAt and not StartedAt: verification opens its record
// one transaction earlier (§11.5), and reading the clock here would report a
// window of zero however long the checks actually took.
//
// The actor's claims are redacted first, for the reason a deployment's are — a
// run is persisted and rendered on every surface, so a credential that reached
// it would be impossible to recall (INV-012).
func New(g identity.Generator, c identity.Clock, by identity.Principal, r Run) (Run, error) {
	id, err := identity.NewVerificationRunID(g)
	if err != nil {
		return Run{}, err
	}
	r.ID = id
	r.Actor = by.Redacted()
	r.FinishedAt = c.Now()
	if err := r.Validate(); err != nil {
		return Run{}, err
	}
	return r, nil
}

// Validate reports whether the run is a complete, auditable record.
//
// It does not check that a verdict is one this version knows. What a verifier
// said is a fact, and refusing to store an unrecognised word would lose the
// only evidence of why the run came out an error — Verdict already reports one
// for it, which is the safe reading.
func (r Run) Validate() error {
	for _, f := range []struct {
		name, value string
	}{
		{"id", string(r.ID)},
		{"deployment id", string(r.DeploymentID)},
		{"plan id", string(r.PlanID)},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("verification: no %s", f.name)
		}
	}
	// No check is a legitimate run. A verification configured with nothing to
	// ask observed nothing, and Verdict says inconclusive rather than pass —
	// refusing to record it would hide that verification was doing nothing.
	for i, c := range r.Checks {
		if strings.TrimSpace(c.Verifier.Name) == "" || strings.TrimSpace(c.Verifier.Kind) == "" {
			return fmt.Errorf("verification: check %d names no verifier", i)
		}
	}
	if r.StartedAt.IsZero() {
		return errors.New("verification: no start time")
	}
	if r.FinishedAt.IsZero() {
		return errors.New("verification: no finish time")
	}
	if r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf(
			"verification: finished %s before it started %s", r.FinishedAt, r.StartedAt)
	}
	if err := r.Actor.Validate(); err != nil {
		return fmt.Errorf("verification: %w", err)
	}
	return nil
}
