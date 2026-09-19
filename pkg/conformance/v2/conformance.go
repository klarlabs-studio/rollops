// Package conformancev2 is the shared contract test every pkg/target/v2 target
// must pass — first-party and community plugins alike (§9.5). It is what keeps
// "infrastructure-agnostic" from meaning "inconsistent": the engine's
// behaviour is only as predictable as the least predictable target under it.
//
// Targets call it from their _test.go:
//
//	func TestConformance(t *testing.T) {
//	    conformancev2.Suite{New: newTarget, Desired: sample}.Run(t)
//	}
//
// A v1 target reaches the same suite through SuiteForV1, so adopting v2 does
// not mean rewriting a target in order to find out whether it was correct.
//
// Every axis returns an error rather than calling t.Fatal, so the same checks
// can run outside the testing harness — a `rollops target verify` against a
// plugin an operator is about to install is the same question asked later.
package conformancev2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/rollops/pkg/target/v1adapter"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// callBudget bounds a check that is waiting to find out whether the target
// returns at all. It is not a timeout the target must meet — it is how long
// the suite waits before reporting that the target ignored one.
const callBudget = 30 * time.Second

// ErrNotApplicable reports that an axis had nothing to measure — which is not
// the same as the target having passed it. Run turns it into a skipped
// subtest and Check leaves it out of the failures, so that the one thing a
// reader cannot conclude from a green run is that an axis was checked when it
// was not. That is the same quiet lie about capability the suite exists to
// catch, and it would be embarrassing to ship it here.
var ErrNotApplicable = errors.New("conformance: nothing to measure on this axis")

// Factory returns a fresh target. Each axis gets its own, because an axis that
// inherits the state another left behind is an axis whose verdict depends on
// the order they ran in.
type Factory func() (targetv2.Target, error)

// Suite is one target under test.
type Suite struct {
	// New constructs a fresh target.
	New Factory

	// Desired is a sample the target can converge on. It should be small and
	// disposable: the suite applies it, and applies it again.
	Desired targetv2.DesiredState

	// Invalid is a desired state the target must reject as malformed. It is
	// what makes the typed-error axis reach the target: every target refuses
	// an apply with no idempotency key, and most refuse it in code they share,
	// so an axis that probes only that has measured the guard clause rather
	// than the mapping underneath it.
	//
	// Leave it zero when the target has no malformed state to be given — a
	// fake that accepts any bytes cannot be shown to reject any. The probe is
	// then not run, and the axis reports on the guard clause alone.
	Invalid targetv2.DesiredState

	// Secrets are strings that must not appear in anything the target says —
	// diffs, inventories, health reasons, error messages (INV-012). Leave it
	// empty when the sample carries no secret; the axis is then skipped rather
	// than passed, because there was nothing to leak.
	Secrets []string
}

// axis is one named check.
type axis struct {
	name string
	run  func(context.Context, targetv2.Target, Suite) error
}

func axes() []axis {
	return []axis{
		{"CapabilitiesAreTruthful", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckCapabilitiesAreTruthful(ctx, t, s.Desired)
		}},
		{"InspectIsStable", func(ctx context.Context, t targetv2.Target, _ Suite) error {
			return CheckInspectIsStable(ctx, t)
		}},
		{"PlanHasNoSideEffects", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckPlanHasNoSideEffects(ctx, t, s.Desired)
		}},
		{"ApplyIsIdempotent", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckApplyIsIdempotent(ctx, t, s.Desired)
		}},
		{"CancellationIsHonoured", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckCancellationIsHonoured(ctx, t, s.Desired)
		}},
		{"TimeoutIsHonoured", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckTimeoutIsHonoured(ctx, t, s.Desired)
		}},
		{"SecretsStayOut", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckSecretsStayOut(ctx, t, s.Desired, s.Secrets)
		}},
		{"ErrorsAreTyped", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckErrorsAreTyped(ctx, t, s.Desired, s.Invalid)
		}},
		{"RollbackWhenDeclared", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckRollbackWhenDeclared(ctx, t, s.Desired)
		}},
		{"DriftWhenDeclared", func(ctx context.Context, t targetv2.Target, s Suite) error {
			return CheckDriftWhenDeclared(ctx, t, s.Desired)
		}},
	}
}

// Run executes every axis as a subtest against a fresh target.
func (s Suite) Run(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, a := range axes() {
		t.Run(a.name, func(t *testing.T) {
			tgt, err := s.New()
			if err != nil {
				t.Fatalf("construct target: %v", err)
			}
			switch err := a.run(ctx, tgt, s); {
			case errors.Is(err, ErrNotApplicable):
				t.Skip(err)
			case err != nil:
				t.Error(err)
			}
		})
	}
}

// Check executes every axis and returns the failures. It reports all of them
// rather than the first: a target that is wrong in three ways should learn
// that in one run.
func (s Suite) Check(ctx context.Context) []error {
	var errs []error
	for _, a := range axes() {
		tgt, err := s.New()
		if err != nil {
			errs = append(errs, fmt.Errorf("conformance: %s: construct target: %w", a.name, err))
			continue
		}
		if err := a.run(ctx, tgt, s); err != nil && !errors.Is(err, ErrNotApplicable) {
			errs = append(errs, err)
		}
	}
	return errs
}

// SuiteForV1 puts a v1 target through the v2 suite by way of the adapter. What
// it measures is the pair, not the v1 target alone — which is the honest thing
// to measure, because the pair is what the engine will call.
func SuiteForV1(newV1 func() (v1.Target, error), meta targetv2.Metadata, desired targetv2.DesiredState) Suite {
	return Suite{
		New: func() (targetv2.Target, error) {
			inner, err := newV1()
			if err != nil {
				return nil, err
			}
			return v1adapter.New(inner, meta), nil
		},
		Desired: desired,
	}
}

// CheckCapabilitiesAreTruthful verifies the declaration in both directions: a
// capability that is not declared must not work, and one that is declared must
// not answer "unsupported". A target that under-declares is as broken as one
// that over-declares — the host routes on the declaration, so a capability it
// was never told about is one that will never be used, however well it works.
//
// Prune is checked in the negative direction only. It is the one destructive
// method in the contract, and a suite that deletes the operator's resources to
// prove it could is a suite nobody runs twice.
//
// Every mismatch is reported rather than the first: a target that declared
// four capabilities it does not have should learn all four in one run, and a
// suite that stops at the first sends its author round the loop once per
// mistake.
func CheckCapabilitiesAreTruthful(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	caps, err := tgt.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("conformance: capabilities: %w", err)
	}

	// The optional verbs live behind interfaces, so the assertion is made once
	// and checked. Asserting it unchecked is what ADR-0006 forbids: a target
	// that declares a capability it did not implement is a defect to report,
	// and a panic reports it by taking every other axis down with it.
	drifter, hasDrift := tgt.(targetv2.Drifter)
	promoter, hasPromote := tgt.(targetv2.Promoter)
	pruner, hasPrune := tgt.(targetv2.Pruner)

	probes := []struct {
		cap         targetv2.Capability
		implemented bool
		call        func() error
	}{
		{targetv2.CapabilityDriftDetection, hasDrift, func() error {
			_, err := drifter.DetectDrift(ctx, targetv2.DriftRequest{Desired: desired})
			return err
		}},
		// Rollback is on Target itself, so there is always a method to call.
		{targetv2.CapabilityNativeRollback, true, func() error {
			_, err := tgt.Rollback(ctx, targetv2.RollbackRequest{})
			return err
		}},
		{targetv2.CapabilityProgressiveDelivery, hasPromote, func() error {
			_, err := promoter.Promote(ctx, targetv2.PromoteRequest{})
			return err
		}},
		{targetv2.CapabilityPrune, hasPrune, func() error {
			_, err := pruner.Prune(ctx, targetv2.PruneRequest{})
			return err
		}},
	}

	var found []error
	for _, p := range probes {
		declared := caps.Has(p.cap)
		if !p.implemented {
			// Not declared and not implemented is the ordinary shape of a
			// target that does not do this: the host routes on the
			// declaration, so it will never reach for the method.
			if declared {
				found = append(found, fmt.Errorf(
					"conformance: the target declares %s but does not implement its method", p.cap))
			}
			continue
		}
		if declared && p.cap == targetv2.CapabilityPrune {
			continue
		}
		err := p.call()
		switch {
		case declared && targetv2.IsUnsupported(err):
			found = append(found, fmt.Errorf("conformance: the target declares %s but answers unsupported", p.cap))
		case !declared && !targetv2.IsUnsupported(err):
			found = append(found, fmt.Errorf("conformance: the target does not declare %s but did not refuse it: %v", p.cap, err))
		}
	}

	// Health is a capability with no method of its own: it is declared when
	// the target can tell healthy from not, and a declared one that answers
	// Unknown has told the engine nothing while claiming otherwise.
	obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{})
	if err != nil {
		return errors.Join(append(found, fmt.Errorf("conformance: observe: %w", err))...)
	}
	if caps.HealthObservation && obs.Health.State == targetv2.HealthUnknown {
		found = append(found, fmt.Errorf("conformance: the target declares health observation but reports unknown health"))
	}
	return errors.Join(found...)
}

// CheckInspectIsStable verifies that looking twice at an unchanged target
// answers the same thing. An unstable fingerprint is drift the engine will
// chase forever.
func CheckInspectIsStable(ctx context.Context, tgt targetv2.Target) error {
	first, err := tgt.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil {
		return fmt.Errorf("conformance: inspect (1): %w", err)
	}
	second, err := tgt.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil {
		return fmt.Errorf("conformance: inspect (2): %w", err)
	}
	if first.Fingerprint != second.Fingerprint {
		return fmt.Errorf("conformance: fingerprint is unstable: %q then %q with nothing between",
			first.Fingerprint, second.Fingerprint)
	}
	if len(first.Resources) != len(second.Resources) {
		return fmt.Errorf("conformance: the inventory changed size (%d then %d) with nothing between",
			len(first.Resources), len(second.Resources))
	}
	return nil
}

// CheckPlanHasNoSideEffects verifies §9.5's hardest requirement to get right: a
// plan that changes anything has already half-applied the batch it was meant to
// guard, and the operator reviewing it is reviewing something that happened.
func CheckPlanHasNoSideEffects(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	before, err := tgt.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil {
		return fmt.Errorf("conformance: inspect before plan: %w", err)
	}
	if _, err := tgt.Plan(ctx, targetv2.PlanRequest{Desired: desired}); err != nil {
		return fmt.Errorf("conformance: plan: %w", err)
	}
	after, err := tgt.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil {
		return fmt.Errorf("conformance: inspect after plan: %w", err)
	}
	if before.Fingerprint != after.Fingerprint {
		return fmt.Errorf("conformance: Plan had a side effect: the fingerprint moved from %q to %q",
			before.Fingerprint, after.Fingerprint)
	}
	if len(before.Resources) != len(after.Resources) {
		return fmt.Errorf("conformance: Plan had a side effect: the inventory went from %d to %d resources",
			len(before.Resources), len(after.Resources))
	}
	return nil
}

// CheckApplyIsIdempotent verifies §9.4 from the side that matters: the case the
// key exists for is a crash between the call and its response, so the retry
// carries the key the lost call used and must not apply a second time.
//
// §9.4 allows two answers, and the suite has to accept both or it is holding
// targets to a contract the spec does not state. A provider that can remember
// the key replays the first result; one whose substrate cannot promise that
// refuses with a typed conflict. What neither may do is apply a second time
// and call it a replay — nor refuse with a bare error, because a caller
// retrying after a lost response has to tell a conflict it can stop on from a
// failure it must try again.
func CheckApplyIsIdempotent(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	key := targetv2.IdempotencyKeyFor("conformance", "op-1")
	first, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired, IdempotencyKey: key})
	if err != nil {
		return fmt.Errorf("conformance: first apply: %w", err)
	}
	replay, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired, IdempotencyKey: key})
	switch {
	case targetv2.KindOf(err) == targetv2.KindConflict:
		// The provider cannot guarantee replay and said so. §9.4's second
		// branch: the caller learns the key is spent rather than being handed
		// a result the target did not stand behind.
	case err != nil:
		return fmt.Errorf("conformance: a repeated idempotency key was refused as kind %q, want a replayed result or %q: %w",
			targetv2.KindOf(err), targetv2.KindConflict, err)
	case replay != first:
		return fmt.Errorf("conformance: the same key did not replay: got %+v, want the first result %+v",
			replay, first)
	}

	// A different key for the same desired state is a second intent, not a
	// retry. It must be applied — and find nothing to do.
	converged, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        desired,
		IdempotencyKey: targetv2.IdempotencyKeyFor("conformance", "op-2"),
	})
	if err != nil {
		return fmt.Errorf("conformance: apply after converging: %w", err)
	}
	if converged.Changed {
		return fmt.Errorf("conformance: applying an already-applied desired state reported a change")
	}
	return nil
}

// contextVerbs lists the calls that must come back when the context is
// already done, given what this target declared. Probing only Inspect finds
// the harmless version of the bug: Inspect is the cheap read, and Apply is the
// call still changing the operator's infrastructure after they pressed cancel.
//
// Prune is left out. It is the one destructive verb in the contract, and the
// whole premise of these axes is that a broken target acts anyway — so
// including it would mean deleting the operator's resources to find out.
// A target that ignores a dead context in Prune is caught by the same bug
// showing up in Apply, which is where it will show up first.
func contextVerbs(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) ([]struct {
	op   string
	call func(context.Context) error
}, error,
) {
	type verb = struct {
		op   string
		call func(context.Context) error
	}

	vs := []verb{
		{"Inspect", func(c context.Context) error {
			_, err := tgt.Inspect(c, targetv2.InspectRequest{})
			return err
		}},
		{"Plan", func(c context.Context) error {
			_, err := tgt.Plan(c, targetv2.PlanRequest{Desired: desired})
			return err
		}},
		{"Apply", func(c context.Context) error {
			_, err := tgt.Apply(c, targetv2.ApplyRequest{
				Desired:        desired,
				IdempotencyKey: targetv2.IdempotencyKeyFor("conformance", "abandoned"),
			})
			return err
		}},
		{"Observe", func(c context.Context) error {
			_, err := tgt.Observe(c, targetv2.ObserveRequest{})
			return err
		}},
	}

	// The optional verbs answer Unsupported when they are not declared, which
	// is not the kind these axes are looking for. They are probed only where
	// the target said the call goes through to the substrate.
	caps, err := tgt.Capabilities(ctx)
	if err != nil {
		return nil, fmt.Errorf("conformance: capabilities: %w", err)
	}
	if caps.NativeRollback {
		vs = append(vs, verb{"Rollback", func(c context.Context) error {
			_, err := tgt.Rollback(c, targetv2.RollbackRequest{})
			return err
		}})
	}
	if d, ok := tgt.(targetv2.Drifter); ok && caps.DriftDetection {
		vs = append(vs, verb{"DetectDrift", func(c context.Context) error {
			_, err := d.DetectDrift(c, targetv2.DriftRequest{Desired: desired})
			return err
		}})
	}
	if p, ok := tgt.(targetv2.Promoter); ok && caps.ProgressiveDelivery {
		vs = append(vs, verb{"Promote", func(c context.Context) error {
			_, err := p.Promote(c, targetv2.PromoteRequest{})
			return err
		}})
	}
	return vs, nil
}

// CheckCancellationIsHonoured verifies a cancelled call comes back, and comes
// back as cancelled. A target that runs on has taken the operator's abort as a
// suggestion.
func CheckCancellationIsHonoured(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	return eachAbandoned(ctx, tgt, desired, cancelled, "cancel", targetv2.KindCanceled)
}

// CheckTimeoutIsHonoured verifies the same for a deadline that has already
// passed. It is a separate axis because the two arrive as different kinds, and
// a host retries one where it must not retry the other.
func CheckTimeoutIsHonoured(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	return eachAbandoned(ctx, tgt, desired, expired, "timeout", targetv2.KindTimeout)
}

func eachAbandoned(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState, dead context.Context, what string, want targetv2.Kind) error {
	vs, err := contextVerbs(ctx, tgt, desired)
	if err != nil {
		return err
	}
	var found []error
	for _, v := range vs {
		if err := checkAbandoned(ctx, what, want, func() error { return v.call(dead) }); err != nil {
			found = append(found, fmt.Errorf("%w (in %s)", err, v.op))
		}
	}
	return errors.Join(found...)
}

func checkAbandoned(ctx context.Context, what string, want targetv2.Kind, call func() error) error {
	done := make(chan error, 1)
	go func() { done <- call() }()

	timer := time.NewTimer(callBudget)
	defer timer.Stop()

	select {
	case err := <-done:
		if err == nil {
			return fmt.Errorf("conformance: the target ignored the %s and succeeded anyway", what)
		}
		if got := targetv2.KindOf(err); got != want {
			return fmt.Errorf("conformance: a %s was reported as kind %q, want %q", what, got, want)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("conformance: the target did not return within %s of the %s", callBudget, what)
	case <-ctx.Done():
		return fmt.Errorf("conformance: %s check abandoned: %w", what, ctx.Err())
	}
}

// CheckSecretsStayOut verifies INV-012 where it is easiest to violate: a diff,
// an inventory, a health reason or an error message that quotes what it was
// given. Secrets do not persist, and a target that echoes one into a plan has
// persisted it in every audit record that plan appears in.
func CheckSecretsStayOut(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState, secrets []string) error {
	if len(secrets) == 0 {
		return ErrNotApplicable
	}

	var said []string
	record := func(parts ...string) { said = append(said, parts...) }

	if plan, err := tgt.Plan(ctx, targetv2.PlanRequest{Desired: desired}); err != nil {
		record(err.Error())
	} else {
		record(plan.Diff, string(plan.Rendered))
		record(plan.Blockers...)
	}

	if state, err := tgt.Inspect(ctx, targetv2.InspectRequest{}); err != nil {
		record(err.Error())
	} else {
		record(state.Fingerprint)
		for _, r := range state.Resources {
			record(r.Kind, r.Name, r.Namespace, r.Status, r.Parent)
		}
		for k, v := range state.Meta {
			record(k, v)
		}
	}

	if obs, err := tgt.Observe(ctx, targetv2.ObserveRequest{}); err != nil {
		record(err.Error())
	} else {
		record(obs.Fingerprint, obs.Health.Reason)
		for k, v := range obs.Meta {
			record(k, v)
		}
	}

	haystack := strings.Join(said, "\x00")
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(haystack, secret) {
			return fmt.Errorf("conformance: the target did not redact a secret: it appears in what Plan, Inspect or Observe returned")
		}
	}
	return nil
}

// CheckErrorsAreTyped verifies a failure the host can act on. A bare error says
// only that something went wrong, which leaves a caller to choose between
// retrying what it must not and refusing what it should retry.
//
// It probes twice. The first is the failure every target must produce: §9.4
// makes the idempotency key mandatory, so an apply without one has to be
// refused. That one is cheap and universal, and it is also answered by a guard
// clause targets tend to share — so on its own it measures the guard rather
// than the target's own mapping, which is how a whole adapter can flatten
// every underlying cause and still pass.
//
// The second sends Invalid, a desired state the target itself must reject, and
// is the one that reaches the target. It is skipped when the subject offers no
// Invalid, because a target that cannot be given anything malformed cannot be
// shown to reject anything.
func CheckErrorsAreTyped(ctx context.Context, tgt targetv2.Target, desired, invalid targetv2.DesiredState) error {
	_, err := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: desired})
	if err == nil {
		return fmt.Errorf("conformance: the target applied without an idempotency key; §9.4 makes it mandatory")
	}
	if err := typedInvalid(err, "a missing idempotency key"); err != nil {
		return err
	}

	if invalid.Kind == "" && len(invalid.Spec) == 0 {
		return nil
	}
	// Plan may or may not notice. A target whose plan-time inspection is
	// optional — every minimal v1 target, since v1's Diff, Render and
	// Preflight are all optional interfaces — has nothing to check the bytes
	// with until it applies them. What it must not do is fail in some other
	// vocabulary, so the kind is checked when there is one and nothing is
	// concluded from Plan succeeding.
	if _, err := tgt.Plan(ctx, targetv2.PlanRequest{Desired: invalid}); err != nil {
		if err := typedInvalid(err, "Plan of a malformed desired state"); err != nil {
			return err
		}
	}

	// Apply must notice. It is where the target acts, and one that accepts a
	// desired state the subject says it rejects has applied something nobody
	// checked.
	const what = "Apply of a malformed desired state"
	_, err = tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        invalid,
		IdempotencyKey: targetv2.IdempotencyKeyFor("conformance", "invalid"),
	})
	if err == nil {
		return fmt.Errorf("conformance: the target applied a desired state the subject says it rejects")
	}
	return typedInvalid(err, what)
}

func typedInvalid(err error, what string) error {
	var te *targetv2.Error
	if !errors.As(err, &te) {
		return fmt.Errorf("conformance: the refusal of %s does not carry a kind the host can act on: %v", what, err)
	}
	if te.Kind != targetv2.KindInvalid {
		return fmt.Errorf("conformance: %s was reported as kind %q, want %q", what, te.Kind, targetv2.KindInvalid)
	}
	return nil
}

// CheckRollbackWhenDeclared exercises rollback on a target that claims it, and
// verifies refusal on one that does not. The engine falls back to applying the
// previous desired state when a target cannot roll back natively, so both
// answers are correct — pretending is not.
func CheckRollbackWhenDeclared(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	caps, err := tgt.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("conformance: capabilities: %w", err)
	}
	if !caps.NativeRollback {
		if _, err := tgt.Rollback(ctx, targetv2.RollbackRequest{}); !targetv2.IsUnsupported(err) {
			return fmt.Errorf("conformance: rollback is not declared but was not refused: %v", err)
		}
		return nil
	}

	applied, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        desired,
		IdempotencyKey: targetv2.IdempotencyKeyFor("conformance", "rollback"),
	})
	if err != nil {
		return fmt.Errorf("conformance: apply before rollback: %w", err)
	}
	if _, err := tgt.Rollback(ctx, targetv2.RollbackRequest{Handle: applied.Handle}); err != nil {
		return fmt.Errorf("conformance: rollback is declared but failed: %w", err)
	}
	return nil
}

// CheckDriftWhenDeclared verifies that a target which just converged does not
// report drift. Drift that is always on is an alert nobody reads.
func CheckDriftWhenDeclared(ctx context.Context, tgt targetv2.Target, desired targetv2.DesiredState) error {
	caps, err := tgt.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("conformance: capabilities: %w", err)
	}
	drifter, ok := tgt.(targetv2.Drifter)
	if !ok {
		if caps.DriftDetection {
			return fmt.Errorf("conformance: the target declares %s but does not implement DetectDrift",
				targetv2.CapabilityDriftDetection)
		}
		// A target that neither declares drift nor implements it is not
		// hiding anything — the host routes on the declaration and will never
		// reach for the method. There is nothing here to measure.
		return ErrNotApplicable
	}
	if !caps.DriftDetection {
		if _, err := drifter.DetectDrift(ctx, targetv2.DriftRequest{Desired: desired}); !targetv2.IsUnsupported(err) {
			return fmt.Errorf("conformance: drift detection is not declared but was not refused: %v", err)
		}
		return nil
	}

	if _, err := tgt.Apply(ctx, targetv2.ApplyRequest{
		Desired:        desired,
		IdempotencyKey: targetv2.IdempotencyKeyFor("conformance", "drift"),
	}); err != nil {
		return fmt.Errorf("conformance: apply before drift check: %w", err)
	}
	res, err := drifter.DetectDrift(ctx, targetv2.DriftRequest{Desired: desired})
	if err != nil {
		return fmt.Errorf("conformance: drift detection is declared but failed: %w", err)
	}
	if res.Drifted {
		return fmt.Errorf("conformance: the target reported drift against the state it had just applied")
	}
	return nil
}
