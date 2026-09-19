package conformancev2_test

import (
	"context"
	"errors"
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
	leak           string // echoed into the plan diff
	acceptEmptyKey bool   // applies without an idempotency key
	forgetKeys     bool   // never replays, so a retry applies twice
	untypedErrors  bool   // fails with a bare error carrying no kind
	sickHealth     bool   // claims health observation, answers Unknown
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

type errString string

func (e errString) Error() string { return string(e) }

func (m *mem) done(ctx context.Context) error {
	if m.ignoreCancel {
		return nil
	}
	return ctx.Err()
}

func (m *mem) Inspect(ctx context.Context, _ targetv2.InspectRequest) (targetv2.ObservedState, error) {
	if err := m.done(ctx); err != nil {
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
	if err := m.done(ctx); err != nil {
		return targetv2.PlanResult{}, targetv2.Failf(targetv2.KindOf(err), "Plan", err, "%v", err)
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
	return targetv2.PlanResult{Changes: m.live != req.Desired.Checksum, Diff: diff}, nil
}

func (m *mem) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	if err := m.done(ctx); err != nil {
		return targetv2.ApplyResult{}, targetv2.Failf(targetv2.KindOf(err), "Apply", err, "%v", err)
	}
	if req.IdempotencyKey == "" && !m.acceptEmptyKey {
		return targetv2.ApplyResult{}, m.fail("Apply")
	}
	if !m.forgetKeys {
		if prior, seen := m.applied[req.IdempotencyKey]; seen {
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
	if err := m.done(ctx); err != nil {
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
		return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
	}
	if err := m.done(ctx); err != nil {
		return targetv2.RollbackResult{}, targetv2.Failf(targetv2.KindOf(err), "Rollback", err, "%v", err)
	}
	return targetv2.RollbackResult{Changed: true, Detail: "rolled back"}, nil
}

func (m *mem) Promote(context.Context, targetv2.PromoteRequest) (targetv2.PromoteResult, error) {
	if !m.caps.ProgressiveDelivery {
		return targetv2.PromoteResult{}, targetv2.Unsupported("Promote", targetv2.CapabilityProgressiveDelivery)
	}
	return targetv2.PromoteResult{Detail: "promoted"}, nil
}

func (m *mem) DetectDrift(_ context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	if m.lieAboutDrift || (!m.caps.DriftDetection && !m.hideDrift) {
		return targetv2.DriftResult{}, targetv2.Unsupported("DetectDrift", targetv2.CapabilityDriftDetection)
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

func (v *v1mem) Apply(_ context.Context, m v1.Manifest) (v1.Result, error) {
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

func TestAV1TargetConformsThroughTheAdapter(t *testing.T) {
	s := conformancev2.SuiteForV1(
		func() (v1.Target, error) { return &v1mem{}, nil },
		targetv2.Metadata{Kind: "mem", Name: "x/test/v1", Version: "1"},
		desired(),
	)

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
