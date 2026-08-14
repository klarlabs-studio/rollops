package progressive

import (
	"testing"
	"time"

	"go.klarlabs.de/statekit"
)

func twoStep() Plan {
	return Plan{Strategy: "canary", Steps: []Step{
		{Weight: 25, Pause: 2 * time.Second},
		{Weight: 100, Pause: 2 * time.Second},
	}}
}

func newStepper(t *testing.T, plan Plan, h StepHealth, clk *statekit.FakeClock) *Stepper {
	t.Helper()
	m, err := BuildStepMachine(plan, h)
	if err != nil {
		t.Fatal(err)
	}
	if clk != nil {
		return NewStepper(m, statekit.WithClock[StepContext](clk))
	}
	return NewStepper(m)
}

func TestStepper_TimedAdvance(t *testing.T) {
	clk := statekit.NewFakeClock(time.Unix(0, 0))
	s := newStepper(t, twoStep(), nil, clk)
	if s.State() != "step0" || s.Context().Weight != 25 {
		t.Fatalf("start = %s w=%d", s.State(), s.Context().Weight)
	}
	clk.Advance(2 * time.Second)
	if s.State() != "step1" || s.Context().Weight != 100 {
		t.Fatalf("after pause0 = %s w=%d, want step1 w=100", s.State(), s.Context().Weight)
	}
	clk.Advance(2 * time.Second)
	if !s.Complete() {
		t.Fatalf("after pause1 = %s, want complete", s.State())
	}
}

func TestStepper_PauseHoldsTimers(t *testing.T) {
	clk := statekit.NewFakeClock(time.Unix(0, 0))
	s := newStepper(t, twoStep(), nil, clk)
	s.Pause()
	if !s.Paused() {
		t.Fatalf("not paused: %s", s.State())
	}
	clk.Advance(10 * time.Second) // exiting the step cancels its After timer
	if !s.Paused() {
		t.Fatalf("advanced while paused: %s", s.State())
	}
	s.Resume()
	if s.State() != "step0" {
		t.Fatalf("resume = %s, want step0", s.State())
	}
	clk.Advance(2 * time.Second)
	if s.State() != "step1" {
		t.Fatalf("post-resume advance = %s, want step1", s.State())
	}
}

func TestStepper_Promote(t *testing.T) {
	s := newStepper(t, twoStep(), nil, nil)
	s.Promote()
	if !s.Complete() {
		t.Fatalf("promote = %s, want complete", s.State())
	}
}

func TestStepper_ZeroPauseAdvancesOnNext(t *testing.T) {
	plan := Plan{Strategy: "canary", Steps: []Step{{Weight: 10}, {Weight: 100}}}
	s := newStepper(t, plan, nil, nil)
	if s.State() != "step0" || s.Context().Weight != 10 {
		t.Fatalf("start = %s w=%d", s.State(), s.Context().Weight)
	}
	s.Next()
	if s.State() != "step1" || s.Context().Weight != 100 {
		t.Fatalf("next = %s w=%d, want step1 w=100", s.State(), s.Context().Weight)
	}
	s.Next()
	if !s.Complete() {
		t.Fatalf("next last = %s, want complete", s.State())
	}
}

func TestStepper_HealthAbort(t *testing.T) {
	s := newStepper(t, twoStep(), func(int, int) bool { return false }, nil)
	if !s.Aborted() {
		t.Fatalf("unhealthy entry = %s, want aborted", s.State())
	}
}

func TestStepper_RestoreClearedTimersAdvanceOnFakeClock(t *testing.T) {
	clk := statekit.NewFakeClock(time.Unix(0, 0))
	s := newStepper(t, twoStep(), nil, clk)
	snap := s.Snapshot()
	s.Stop()
	snap.PendingTimers = nil // re-arm on the new FakeClock
	clk2 := statekit.NewFakeClock(time.Unix(0, 0))
	m, err := BuildStepMachine(twoStep(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := RestoreStepper(m, snap, statekit.WithClock[StepContext](clk2))
	if err != nil {
		t.Fatalf("RestoreStepper: %v", err)
	}
	if s2.State() != "step0" {
		t.Fatalf("restored = %s, want step0", s2.State())
	}
	clk2.Advance(2 * time.Second)
	if s2.State() != "step1" || s2.Context().Weight != 100 {
		t.Fatalf("after restore+advance = %s w=%d, want step1 w=100", s2.State(), s2.Context().Weight)
	}
}
