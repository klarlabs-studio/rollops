package progressive

import (
	"fmt"

	"go.klarlabs.de/statekit"
)

// The canary step machine is a statekit statechart, one per in-flight rollout,
// built from the resolved Plan. It owns the *timing* and *control* of progressive
// delivery — the pauses between weight steps are statekit `After` delayed
// transitions (interpreter-driven, not goroutine sleeps), so the whole thing is
// deterministically testable with a FakeClock and snapshot-recoverable across a
// restart. The engine deploys once up front, then drives this machine and reacts
// to its state to health-gate and persist phase.
//
//	step0 ──After(p0)──▶ step1 ──After(p1)──▶ … ──▶ complete (final)
//	  │PAUSE                                          ▲ PROMOTE (from any step/paused)
//	  ▼                                               │
//	paused0 ──RESUME──▶ step0          ABORT ──▶ aborted (final)
//
// Health is gated by an OnEntry action that flips ctx.Failed; an eventless
// (Always) guarded transition then routes a failed step to `aborted`.

// Step/control event names for the canary machine.
const (
	EvPause   = "PAUSE"
	EvResume  = "RESUME"
	EvPromote = "PROMOTE"
	EvAbort   = "ABORT"
)

// Machine state ids that the engine observes.
const (
	StateComplete = "complete"
	StateAborted  = "aborted"
)

// StepContext is the canary machine's context: the live cursor the engine reads
// to persist progress and that snapshots/restores for crash recovery.
type StepContext struct {
	Index  int  // current step index (0-based)
	Weight int  // traffic weight at the current step
	Failed bool // health gate failed at the current step
}

// StepHealth reports whether the workload is healthy at the given step weight.
// It runs synchronously on step entry; false routes the rollout to aborted.
type StepHealth func(stepIndex, weight int) bool

func stepID(i int) string   { return fmt.Sprintf("step%d", i) }
func pausedID(i int) string { return fmt.Sprintf("paused%d", i) }

// BuildStepMachine compiles a canary plan into a statekit machine. health is
// invoked on entry to each step; a nil gate is treated as always-healthy.
func BuildStepMachine(plan Plan, health StepHealth) (*statekit.MachineConfig[StepContext], error) {
	if health == nil {
		health = func(int, int) bool { return true }
	}
	n := len(plan.Steps)
	if n == 0 {
		return nil, fmt.Errorf("progressive: empty plan")
	}

	mb := statekit.NewMachine[StepContext]("canary").
		WithInitial(statekit.StateID(stepID(0))).
		WithGuard("failed", func(c StepContext, _ statekit.Event) bool { return c.Failed }).
		WithGuard("ok", func(c StepContext, _ statekit.Event) bool { return !c.Failed })

	// Per-step entry action: set cursor + run the health gate.
	for i := range plan.Steps {
		i, weight := i, plan.Steps[i].Weight
		mb = mb.WithAction(statekit.ActionType(fmt.Sprintf("enter%d", i)), func(ctx *StepContext, _ statekit.Event) {
			ctx.Index = i
			ctx.Weight = weight
			ctx.Failed = !health(i, weight)
		})
	}

	for i := range plan.Steps {
		next := statekit.StateID(StateComplete)
		if i < n-1 {
			next = statekit.StateID(stepID(i + 1))
		}
		sb := mb.State(statekit.StateID(stepID(i))).
			OnEntry(statekit.ActionType(fmt.Sprintf("enter%d", i))).
			// failed health → aborted (eventless, fires right after entry).
			Always().Target(statekit.StateID(StateAborted)).Guard("failed").
			// healthy → advance after the configured pause.
			After(plan.Steps[i].Pause).Target(next).Guard("ok").
			On(EvPause).Target(statekit.StateID(pausedID(i))).
			On(EvPromote).Target(statekit.StateID(StateComplete)).
			On(EvAbort).Target(statekit.StateID(StateAborted)).
			Done()
		// paused sibling for this step; resume re-enters the same step.
		sb = sb.State(statekit.StateID(pausedID(i))).
			On(EvResume).Target(statekit.StateID(stepID(i))).
			On(EvPromote).Target(statekit.StateID(StateComplete)).
			On(EvAbort).Target(statekit.StateID(StateAborted)).
			Done()
		mb = sb
	}

	mb = mb.State(statekit.StateID(StateComplete)).Final().Done().
		State(statekit.StateID(StateAborted)).Final().Done()

	m, err := mb.Build()
	if err != nil {
		return nil, fmt.Errorf("progressive: build step machine: %w", err)
	}
	return m, nil
}

// Stepper wraps a running canary machine with the control surface the engine and
// UI use. Timing is driven by the injected clock (FakeClock in tests).
type Stepper struct {
	interp *statekit.Interpreter[StepContext]
}

// NewStepper starts a canary machine. Pass statekit.WithClock(fake) in tests for
// deterministic stepping.
func NewStepper(m *statekit.MachineConfig[StepContext], opts ...statekit.Option[StepContext]) *Stepper {
	interp := statekit.NewInterpreter(m, opts...)
	interp.Start()
	return &Stepper{interp: interp}
}

func (s *Stepper) send(ev string) { s.interp.Send(statekit.Event{Type: statekit.EventType(ev)}) }

// Pause holds the canary at the current step. Resume continues, Promote skips to
// completion, Abort terminates to aborted.
func (s *Stepper) Pause()   { s.send(EvPause) }
func (s *Stepper) Resume()  { s.send(EvResume) }
func (s *Stepper) Promote() { s.send(EvPromote) }
func (s *Stepper) Abort()   { s.send(EvAbort) }

// State returns the current state id (e.g. "step1", "paused0", "complete").
func (s *Stepper) State() string { return string(s.interp.State().Value) }

// Context returns the live cursor.
func (s *Stepper) Context() StepContext { return s.interp.Snapshot().Context }

// Paused reports whether the canary is operator-held.
func (s *Stepper) Paused() bool { return len(s.State()) >= 6 && s.State()[:6] == "paused" }

// Done reports terminal completion (promoted-eligible) or abort.
func (s *Stepper) Complete() bool { return s.State() == StateComplete }
func (s *Stepper) Aborted() bool  { return s.State() == StateAborted }

// Snapshot/Restore persist the machine (including pending timers) for crash
// recovery — a restarted daemon resumes a mid-step or paused canary.
func (s *Stepper) Snapshot() statekit.Snapshot[StepContext] { return s.interp.Snapshot() }
func (s *Stepper) Restore(snap statekit.Snapshot[StepContext]) error {
	return s.interp.Restore(snap)
}
