package conformancev2_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	conformancev2 "go.klarlabs.de/rollops/pkg/conformance/v2"
	v1 "go.klarlabs.de/rollops/pkg/target"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// mem is a correct in-memory target. Each knob breaks exactly one axis, so a
// failing check can be traced to the misbehaviour it is supposed to catch —
// a conformance suite that cannot fail is a conformance suite that proves
// nothing.
type mem struct {
	caps targetv2.Capabilities

	live    string
	applied map[string]targetv2.ApplyResult
	planned int

	// Deliberate misbehaviours.
	lieAboutDrift  bool   // declares drift, answers unsupported
	hideDrift      bool   // does not declare drift, answers anyway
	planApplies    bool   // Plan converges, which the batch was meant to prevent
	unstable       bool   // Inspect invents a new fingerprint each call
	ignoreCancel   bool   // runs on regardless of the context
	deafIn         string // honours the context everywhere but this one verb
	leak           string // echoed into the plan diff
	acceptEmptyKey bool   // applies without an idempotency key
	forgetKeys     bool   // never replays, so a retry applies twice
	untypedErrors  bool   // fails with a bare error carrying no kind
	cannotReplay   bool   // refuses a repeated key with a typed conflict (§9.4)
	bareOnBadSpec  bool   // types the empty-key refusal but bares the malformed one
	planWanders    bool   // renders different bytes each time it is asked
	rollbackActs   bool   // does not declare rollback, refuses it, and acts anyway
	sickHealth     bool   // claims health observation, answers Unknown
	sayItReplayed  bool   // replays the same outcome under different prose
}

func newMem(caps targetv2.Capabilities) *mem {
	return &mem{caps: caps, applied: map[string]targetv2.ApplyResult{}}
}

func (m *mem) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "mem", Name: "x/test/mem", Version: "1"}
}

func (m *mem) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return m.caps, nil
}

func (m *mem) fail(op string) error {
	if m.untypedErrors {
		return errString("something went wrong")
	}
	return targetv2.Failf(targetv2.KindInvalid, op, nil, "refused")
}

// parse is the one failure every target can produce on demand: a desired
// state that does not parse. It is what the typed-error axis provokes, because
// there is no portable way to arrange a denial or an outage.
func (m *mem) parse(op string, d targetv2.DesiredState) error {
	if !bytes.HasPrefix(d.Spec, []byte("!!")) {
		return nil
	}
	if m.bareOnBadSpec {
		return errString("cannot parse")
	}
	return targetv2.Failf(targetv2.KindInvalid, op, nil, "the spec does not parse")
}

type errString string

func (e errString) Error() string { return string(e) }

// doneIn is how mem answers a context, per verb. deafIn names the one verb it
// ignores it in, which is the dangerous shape of this bug: a target that
// checks the context in Inspect and not in Apply passes an axis that only
// probes the cheap read.
func (m *mem) doneIn(ctx context.Context, op string) error {
	if m.ignoreCancel || m.deafIn == op {
		return nil
	}
	return ctx.Err()
}

func (m *mem) Inspect(ctx context.Context, _ targetv2.InspectRequest) (targetv2.ObservedState, error) {
	if err := m.doneIn(ctx, "Inspect"); err != nil {
		return targetv2.ObservedState{}, targetv2.Failf(targetv2.KindOf(err), "Inspect", err, "%v", err)
	}
	fp := m.live
	if m.unstable {
		m.planned++
		fp = m.live + string(rune('a'+m.planned%7))
	}
	return targetv2.ObservedState{
		Fingerprint: fp,
		Resources:   []targetv2.Resource{{Kind: "Thing", Name: "one", Status: "ready"}},
	}, nil
}

func (m *mem) Plan(ctx context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	if err := m.doneIn(ctx, "Plan"); err != nil {
		return targetv2.PlanResult{}, targetv2.Failf(targetv2.KindOf(err), "Plan", err, "%v", err)
	}
	if err := m.parse("Plan", req.Desired); err != nil {
		return targetv2.PlanResult{}, err
	}
	if m.planApplies {
		m.live = req.Desired.Checksum
	}
	diff := ""
	if m.live != req.Desired.Checksum {
		diff = "- " + m.live + "\n+ " + req.Desired.Checksum
	}
	if m.leak != "" {
		diff += "\ntoken: " + m.leak
	}
	rendered := req.Desired.Spec
	if m.planWanders {
		m.planned++
		rendered = append(append([]byte{}, req.Desired.Spec...), byte('a'+m.planned%7))
	}
	return targetv2.PlanResult{
		Changes:  m.live != req.Desired.Checksum,
		Diff:     diff,
		Rendered: rendered,
	}, nil
}

func (m *mem) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	if err := m.doneIn(ctx, "Apply"); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindOf(err), "Apply", err, "%v", err)
	}
	if req.IdempotencyKey == "" && !m.acceptEmptyKey {
		return targetv2.ApplyResult{}, m.fail("Apply")
	}
	if err := m.parse("Apply", req.Desired); err != nil {
		return targetv2.ApplyResult{}, err
	}
	if !m.forgetKeys {
		if prior, seen := m.applied[req.IdempotencyKey]; seen {
			if m.cannotReplay {
				if m.untypedErrors {
					return targetv2.ApplyResult{}, errString("key already used")
				}
				return targetv2.ApplyResult{}, targetv2.IdempotencyConflict("Apply", req.IdempotencyKey)
			}
			if m.sayItReplayed {
				prior.Detail = "replayed: this key already converged the target"
			}
			return prior, nil
		}
	}
	changed := m.live != req.Desired.Checksum
	m.live = req.Desired.Checksum
	res := targetv2.ApplyResult{Changed: changed, Detail: "applied", Handle: "h1"}
	m.applied[req.IdempotencyKey] = res
	return res, nil
}

func (m *mem) Observe(ctx context.Context, _ targetv2.ObserveRequest) (targetv2.Observation, error) {
	if err := m.doneIn(ctx, "Observe"); err != nil {
		return targetv2.Observation{}, targetv2.Failf(targetv2.KindOf(err), "Observe", err, "%v", err)
	}
	state := targetv2.HealthHealthy
	if m.sickHealth {
		state = targetv2.HealthUnknown
	}
	return targetv2.Observation{
		Fingerprint: m.live,
		Health:      targetv2.HealthStatus{State: state, Reason: "ready"},
	}, nil
}

func (m *mem) Rollback(ctx context.Context, _ targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	if !m.caps.NativeRollback {
		if m.rollbackActs {
			m.live = ""
		}
		return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
	}
	if err := m.doneIn(ctx, "Rollback"); err != nil {
		return targetv2.RollbackResult{}, targetv2.Failf(targetv2.KindOf(err), "Rollback", err, "%v", err)
	}
	return targetv2.RollbackResult{Changed: true, Detail: "rolled back"}, nil
}

func (m *mem) Promote(ctx context.Context, _ targetv2.PromoteRequest) (targetv2.PromoteResult, error) {
	if !m.caps.ProgressiveDelivery {
		return targetv2.PromoteResult{}, targetv2.Unsupported("Promote", targetv2.CapabilityProgressiveDelivery)
	}
	if err := m.doneIn(ctx, "Promote"); err != nil {
		return targetv2.PromoteResult{}, targetv2.Failf(targetv2.KindOf(err), "Promote", err, "%v", err)
	}
	return targetv2.PromoteResult{Detail: "promoted"}, nil
}

func (m *mem) DetectDrift(ctx context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	if m.lieAboutDrift || (!m.caps.DriftDetection && !m.hideDrift) {
		return targetv2.DriftResult{}, targetv2.Unsupported("DetectDrift", targetv2.CapabilityDriftDetection)
	}
	if err := m.doneIn(ctx, "DetectDrift"); err != nil {
		return targetv2.DriftResult{}, targetv2.Failf(targetv2.KindOf(err), "DetectDrift", err, "%v", err)
	}
	return targetv2.DriftResult{Drifted: m.live != req.Desired.Checksum}, nil
}

func (m *mem) Prune(context.Context, targetv2.PruneRequest) (targetv2.PruneResult, error) {
	if !m.caps.Prune {
		return targetv2.PruneResult{}, targetv2.Unsupported("Prune", targetv2.CapabilityPrune)
	}
	return targetv2.PruneResult{Removed: 1}, nil
}

func desired() targetv2.DesiredState {
	return targetv2.DesiredState{Kind: "mem", Spec: []byte("spec"), Checksum: "abc"}
}

func healthy() targetv2.Capabilities {
	return targetv2.Capabilities{HealthObservation: true}
}

// suiteFor runs every axis against one configuration and reports which failed.
func suiteFor(t *testing.T, build func() *mem, secrets ...string) []error {
	t.Helper()
	s := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return build(), nil },
		Desired: desired(),
		Secrets: secrets,
	}
	return s.Check(context.Background())
}

func TestAConformantTargetPassesEveryAxis(t *testing.T) {
	errs := suiteFor(t, func() *mem {
		return newMem(targetv2.Capabilities{
			HealthObservation: true, DriftDetection: true, NativeRollback: true,
		})
	})
	if len(errs) != 0 {
		t.Fatalf("a conformant target failed: %v", errs)
	}
}

func TestAMinimalTargetPassesEveryAxis(t *testing.T) {
	// The axes that need a capability must be satisfied by not having it, or
	// the suite would push every target to declare things it cannot do.
	errs := suiteFor(t, func() *mem { return newMem(healthy()) })
	if len(errs) != 0 {
		t.Fatalf("a target with no optional capabilities failed: %v", errs)
	}
}

// TestAnAxisWithNothingToMeasureSaysSo separates "passed" from "never ran".
// A suite that names no secret has nothing to look for, and reporting that as
// a green tick is the same quiet lie about capability the suite exists to
// catch — one axis short of what the reader was told was measured.
func TestAnAxisWithNothingToMeasureSaysSo(t *testing.T) {
	tgt := newMem(healthy())
	desired := targetv2.DesiredState{Kind: "mem", Spec: []byte("x"), Checksum: "sum-1"}

	err := conformancev2.CheckSecretsStayOut(context.Background(), tgt, desired, nil)
	if !errors.Is(err, conformancev2.ErrNotApplicable) {
		t.Fatalf("an axis with no secret to look for returned %v, want ErrNotApplicable", err)
	}

	// Not applicable is not a failure either: Check must not report it.
	errs := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return newMem(healthy()), nil },
		Desired: desired,
	}.Check(context.Background())
	if len(errs) != 0 {
		t.Fatalf("a skipped axis was reported as a failure: %v", errs)
	}
}

// eachBreakageIsCaught is the real assertion about the suite: every
// misbehaviour it exists to find must actually fail it.
func TestEachBreakageIsCaught(t *testing.T) {
	cases := []struct {
		name    string
		build   func() *mem
		secrets []string
		wants   string // a substring of the failure that must be reported
	}{
		{
			name: "declares drift it cannot do",
			build: func() *mem {
				m := newMem(targetv2.Capabilities{HealthObservation: true, DriftDetection: true})
				m.lieAboutDrift = true
				return m
			},
			wants: "drift",
		},
		{
			name: "does drift it never declared",
			build: func() *mem {
				m := newMem(healthy())
				m.hideDrift = true
				return m
			},
			wants: "drift",
		},
		{
			name: "claims health it does not observe",
			build: func() *mem {
				m := newMem(healthy())
				m.sickHealth = true
				return m
			},
			wants: "health",
		},
		{
			name: "plans by applying",
			build: func() *mem {
				m := newMem(healthy())
				m.planApplies = true
				return m
			},
			wants: "side effect",
		},
		{
			name: "invents a fingerprint every look",
			build: func() *mem {
				m := newMem(healthy())
				m.unstable = true
				return m
			},
			wants: "fingerprint",
		},
		{
			name: "runs on after cancellation",
			build: func() *mem {
				m := newMem(healthy())
				m.ignoreCancel = true
				return m
			},
			wants: "cancel",
		},
		{
			name: "applies without an idempotency key",
			build: func() *mem {
				m := newMem(healthy())
				m.acceptEmptyKey = true
				return m
			},
			wants: "idempotency key",
		},
		{
			name: "forgets the key, so a retry applies twice",
			build: func() *mem {
				m := newMem(healthy())
				m.forgetKeys = true
				return m
			},
			wants: "replay",
		},
		{
			name: "fails without saying what kind of failure",
			build: func() *mem {
				m := newMem(healthy())
				m.untypedErrors = true
				m.acceptEmptyKey = false
				return m
			},
			wants: "kind",
		},
		{
			name: "puts a secret in the plan",
			build: func() *mem {
				m := newMem(healthy())
				m.leak = "hunter2"
				return m
			},
			secrets: []string{"hunter2"},
			wants:   "redact",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := suiteFor(t, tc.build, tc.secrets...)
			if len(errs) == 0 {
				t.Fatalf("the suite passed a target that %s", tc.name)
			}
			joined := strings.ToLower(joinErrs(errs))
			if !strings.Contains(joined, tc.wants) {
				t.Errorf("the failure does not mention %q: %s", tc.wants, joined)
			}
		})
	}
}

func joinErrs(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

// v1mem is a v1 target, to prove a v1 implementation reaches the v2 suite
// through the adapter rather than needing a suite of its own.
type v1mem struct{ live string }

// reject is the v1 target speaking v2's vocabulary, which is what §9.6 asks
// of a v1 target that wants to keep working: the adapter cannot classify an
// error it was handed no vocabulary for, so a kind the host branches on has to
// come from here.
func (v *v1mem) reject(op string, m v1.Manifest) error {
	if !bytes.HasPrefix(m.Spec, []byte("!!")) {
		return nil
	}
	return targetv2.Failf(targetv2.KindInvalid, op, nil, "the spec does not parse")
}

func (v *v1mem) Apply(_ context.Context, m v1.Manifest) (v1.Result, error) {
	if err := v.reject("Apply", m); err != nil {
		return v1.Result{}, err
	}
	changed := v.live != m.Checksum
	v.live = m.Checksum
	return v1.Result{Changed: changed, Detail: "applied"}, nil
}

func (v *v1mem) Observe(context.Context) (v1.Fingerprint, error) {
	return v1.Fingerprint{Value: v.live}, nil
}

func (v *v1mem) Health(context.Context) (v1.HealthStatus, error) {
	return v1.HealthStatus{State: v1.HealthHealthy, Reason: "ready"}, nil
}

// TestAV1TargetConformsThroughTheAdapter measures the pair, not the v1 target
// alone — which is the honest thing to measure, because the pair is what the
// engine calls. Invalid is set so the typed-error axis reaches the v1 target
// through the adapter: without it the axis stops at the adapter's own guard
// clause, which is how the adapter flattened every underlying kind to internal
// and passed this suite for as long as it did.
func TestAV1TargetConformsThroughTheAdapter(t *testing.T) {
	s := conformancev2.SuiteForV1(
		func() (v1.Target, error) { return &v1mem{}, nil },
		targetv2.Metadata{Kind: "mem", Name: "x/test/v1", Version: "1"},
		desired(),
	)
	s.Invalid = malformed()

	if errs := s.Check(context.Background()); len(errs) != 0 {
		t.Fatalf("a v1 target failed the v2 suite: %v", errs)
	}
}

func TestRunReportsThroughTheTestingHarness(t *testing.T) {
	// The harness path is what targets actually call; it must survive a real
	// *testing.T rather than only the error-returning form.
	conformancev2.Suite{
		New: func() (targetv2.Target, error) {
			return newMem(targetv2.Capabilities{HealthObservation: true, DriftDetection: true}), nil
		},
		Desired: desired(),
	}.Run(t)
}

// bones implements the mandatory v2 contract and nothing else, while
// declaring every optional capability. It is the target ADR-0006 was written
// about: the optional verbs live behind interfaces, so a capability the target
// declared and did not implement is a type assertion away from a panic.
//
// It cannot embed mem, because embedding would promote the very methods it is
// meant not to have.
type bones struct{ inner *mem }

func newBones(caps targetv2.Capabilities) *bones { return &bones{inner: newMem(caps)} }

// overclaiming declares every optional capability without implementing one.
func overclaiming() targetv2.Capabilities {
	return targetv2.Capabilities{
		HealthObservation:   true,
		DriftDetection:      true,
		NativeRollback:      true,
		ProgressiveDelivery: true,
		Prune:               true,
	}
}

func (b *bones) Metadata() targetv2.Metadata { return b.inner.Metadata() }

func (b *bones) Capabilities(ctx context.Context) (targetv2.Capabilities, error) {
	return b.inner.Capabilities(ctx)
}

func (b *bones) Inspect(ctx context.Context, r targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return b.inner.Inspect(ctx, r)
}

func (b *bones) Plan(ctx context.Context, r targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return b.inner.Plan(ctx, r)
}

func (b *bones) Apply(ctx context.Context, r targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	return b.inner.Apply(ctx, r)
}

func (b *bones) Observe(ctx context.Context, r targetv2.ObserveRequest) (targetv2.Observation, error) {
	return b.inner.Observe(ctx, r)
}

func (b *bones) Rollback(ctx context.Context, r targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return b.inner.Rollback(ctx, r)
}

// TestADeclaredCapabilityWithNoMethodIsReportedNotPanicked is the axis doing
// the job it exists for on the target most likely to need it. A suite that
// panics tells the author their test binary crashed; it does not tell them
// which capability they declared and did not build — and it takes the rest of
// the axes down with it, so nothing else they got wrong is reported either.
func TestADeclaredCapabilityWithNoMethodIsReportedNotPanicked(t *testing.T) {
	errs := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return newBones(overclaiming()), nil },
		Desired: desired(),
	}.Check(context.Background())

	if len(errs) == 0 {
		t.Fatalf("a target declaring four capabilities it does not implement passed every axis")
	}
	joined := fmt.Sprint(errs)
	for _, want := range []string{"drift", "progressive-delivery", "prune"} {
		if !strings.Contains(joined, want) {
			t.Errorf("nothing reported the undeclared %s method; got %v", want, errs)
		}
	}
}

// TestAMandatoryOnlyTargetPassesEveryAxis is the shape most targets are: it
// implements the six methods the contract requires and none of the optional
// interfaces, and declares nothing it cannot do. A suite that fails this
// pushes every target to implement verbs it has no use for, which is how a
// contract ends up with four methods that all answer unsupported.
func TestAMandatoryOnlyTargetPassesEveryAxis(t *testing.T) {
	errs := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return newBones(healthy()), nil },
		Desired: desired(),
	}.Check(context.Background())
	if len(errs) != 0 {
		t.Fatalf("a target implementing only the mandatory contract failed: %v", errs)
	}
}

// TestAProviderThatCannotReplaySaysSo is the second branch §9.4 allows, and
// the suite has to accept it or it is holding targets to a contract the spec
// does not state. A provider whose substrate has no way to remember the key
// can refuse the repeat outright: what it must not do is apply a second time
// and call that a replay.
func TestAProviderThatCannotReplaySaysSo(t *testing.T) {
	errs := suiteFor(t, func() *mem {
		m := newMem(healthy())
		m.cannotReplay = true
		return m
	})
	if len(errs) != 0 {
		t.Fatalf("a target that refused a repeated key with a typed conflict failed: %v", errs)
	}
}

// TestAnUntypedRefusalOfARepeatedKeyIsStillAFailure keeps the branch above
// from becoming a hole. "It errored" is not the same as "it refused": a caller
// retrying after a lost response has to tell a conflict it can stop on from a
// failure it must try again.
func TestAnUntypedRefusalOfARepeatedKeyIsStillAFailure(t *testing.T) {
	tgt := newMem(healthy())
	tgt.cannotReplay = true
	tgt.untypedErrors = true

	err := conformancev2.CheckApplyIsIdempotent(context.Background(), tgt, desired())
	if err == nil {
		t.Fatalf("a bare error on a repeated key passed the idempotency axis")
	}
}

// TestAReplayMaySayThatItIsOne draws the line §9.4 actually draws. "The same
// semantic result" is what the engine acts on — whether anything changed, and
// the handle it can look the operation up by. Detail is prose for a person,
// and the most useful thing it can say on a retry is that this was a retry.
// A suite that demands byte-identical prose forbids exactly that, and buys
// nothing: no caller branches on it.
func TestAReplayMaySayThatItIsOne(t *testing.T) {
	errs := suiteFor(t, func() *mem {
		m := newMem(healthy())
		m.sayItReplayed = true
		return m
	})
	if len(errs) != 0 {
		t.Fatalf("a target whose replay said so in its detail failed: %v", errs)
	}
}

// TestAReplayThatChangedTheAnswerIsStillAFailure keeps the allowance above
// from swallowing the thing the axis exists for: prose may differ, the outcome
// may not.
func TestAReplayThatChangedTheAnswerIsStillAFailure(t *testing.T) {
	tgt := newMem(healthy())
	tgt.forgetKeys = true

	if err := conformancev2.CheckApplyIsIdempotent(context.Background(), tgt, desired()); err == nil {
		t.Fatal("a target that applied twice under one key passed the idempotency axis")
	}
}

// malformed is a desired state every mem rejects. It is what makes the
// typed-error axis reach the target at all: the empty-key probe is answered by
// a guard clause every target shares, so an axis that stops there has measured
// the guard and not the mapping underneath it.
func malformed() targetv2.DesiredState {
	return targetv2.DesiredState{Kind: "mem", Spec: []byte("!! not a spec"), Checksum: "bad"}
}

// TestTheTypedErrorAxisReachesTheTarget is the axis doing the job the v1
// adapter's flattened kinds got past. Every target refuses an apply with no
// idempotency key, and most refuse it in shared code, so a suite that probes
// only that learns nothing about how the target maps its own failures.
func TestTheTypedErrorAxisReachesTheTarget(t *testing.T) {
	tgt := newMem(healthy())
	tgt.bareOnBadSpec = true

	// The guard clause is still correct, so the old probe passes.
	if _, err := tgt.Apply(context.Background(), targetv2.ApplyRequest{Desired: desired()}); !errors.As(err, new(*targetv2.Error)) {
		t.Fatalf("the empty-key refusal is untyped, so this target cannot isolate the malformed probe")
	}

	err := conformancev2.CheckErrorsAreTyped(context.Background(), tgt, desired(), malformed())
	if err == nil {
		t.Fatalf("a target that refuses a malformed spec with a bare error passed the typed-error axis")
	}
}

// TestTheTypedErrorAxisSkipsWhatItCannotProvoke keeps the probe honest for a
// subject that has no malformed state to offer — a target whose fake accepts
// anything cannot be shown to reject anything, and reporting that as a pass
// is the quiet lie the suite exists to catch.
func TestTheTypedErrorAxisSkipsWhatItCannotProvoke(t *testing.T) {
	tgt := newMem(healthy())
	tgt.bareOnBadSpec = true

	if err := conformancev2.CheckErrorsAreTyped(context.Background(), tgt, desired(), targetv2.DesiredState{}); err != nil {
		t.Fatalf("an axis with no malformed state to send reported %v, want the guard-clause probe alone", err)
	}
}

// TestASoundTargetPassesTheMalformedProbe is the other half: a target that
// types its own refusal must pass.
func TestASoundTargetPassesTheMalformedProbe(t *testing.T) {
	errs := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return newMem(healthy()), nil },
		Desired: desired(),
		Invalid: malformed(),
	}.Check(context.Background())
	if len(errs) != 0 {
		t.Fatalf("a target that types its refusal of a malformed spec failed: %v", errs)
	}
}

// TestEveryVerbHonoursTheContext is the axis widened to where it matters. A
// target that checks the context in Inspect and not in Apply is the dangerous
// version of this bug, not the harmless one: Inspect is the cheap read, and
// Apply is the call still changing the operator's infrastructure after they
// pressed cancel.
func TestEveryVerbHonoursTheContext(t *testing.T) {
	for _, op := range []string{"Inspect", "Plan", "Apply", "Observe", "Rollback", "DetectDrift"} {
		t.Run("deaf in "+op, func(t *testing.T) {
			build := func() *mem {
				m := newMem(targetv2.Capabilities{
					HealthObservation: true, NativeRollback: true, DriftDetection: true,
				})
				m.deafIn = op
				return m
			}
			if errs := suiteFor(t, build); len(errs) == 0 {
				t.Errorf("a target that ignores a cancelled context in %s passed every axis", op)
			}
		})
	}
}

// TestAPlanThatWandersIsCaught is the half of side-effect freedom that is not
// about the substrate. Two plans of the same desired state must agree: the
// operator approves what a plan said, and the apply that follows is only
// bound to it if asking again would have said the same thing.
func TestAPlanThatWandersIsCaught(t *testing.T) {
	errs := suiteFor(t, func() *mem {
		m := newMem(healthy())
		m.planWanders = true
		return m
	})
	if len(errs) == 0 {
		t.Fatalf("a target whose plan renders different bytes each time passed every axis")
	}
}

// TestARefusedRollbackThatActedAnywayIsCaught closes the gap between saying no
// and doing nothing. A target that answers unsupported and rolls back regardless
// has told the engine to fall back to applying the previous desired state — on
// top of a substrate it already moved.
func TestARefusedRollbackThatActedAnywayIsCaught(t *testing.T) {
	errs := suiteFor(t, func() *mem {
		m := newMem(healthy())
		m.rollbackActs = true
		return m
	})
	if len(errs) == 0 {
		t.Fatalf("a target that refused rollback and rolled back anyway passed every axis")
	}
}
