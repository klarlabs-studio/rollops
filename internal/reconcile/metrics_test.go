package reconcile

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
)

type fakeSink struct {
	results  map[string]int
	drift    int
	outcomes map[string]int
	tick     time.Duration
	oldest   float64
}

func newFakeSink() *fakeSink {
	return &fakeSink{results: map[string]int{}, outcomes: map[string]int{}}
}
func (f *fakeSink) ObserveTick(d time.Duration)       { f.tick = d }
func (f *fakeSink) CountReconcile(result string)      { f.results[result]++ }
func (f *fakeSink) IncDrift()                         { f.drift++ }
func (f *fakeSink) IncOutcome(outcome string)         { f.outcomes[outcome]++ }
func (f *fakeSink) SetOldestInFlight(seconds float64) { f.oldest = seconds }

// The postmortem's action item 4 assumed instrumentation was complete; in fact
// nothing ever recorded a metric, so a scrape returned zeros. Each tick must
// count what happened.
func TestRecordTick_CountsEveryOutcome(t *testing.T) {
	sink := newFakeSink()
	promoted := &rollout.Rollout{Phase: rollout.PhasePromoted}
	rolledBack := &rollout.Rollout{Phase: rollout.PhaseRolledBack}
	recordTick(sink, 3*time.Second, []RepoOutcome{
		{Repo: "a"},
		{Repo: "b", Outcome: Outcome{Drift: true, Reconciled: true, Rollout: promoted}},
		{Repo: "c", Outcome: Outcome{Drift: true}},
		{Repo: "d", Err: errors.New("apply failed")},
		{Repo: "e", Outcome: Outcome{Reconciled: true, Rollout: rolledBack}},
		{Repo: "f", Err: ErrNotLeader},
	})
	want := map[string]int{"insync": 1, "reconciled": 2, "drift": 1, "error": 1}
	for k, v := range want {
		if sink.results[k] != v {
			t.Errorf("results[%q] = %d, want %d (all: %v)", k, sink.results[k], v, sink.results)
		}
	}
	if len(sink.results) != len(want) {
		t.Errorf("unexpected result labels %v; a follower's not-leader tick counts as nothing", sink.results)
	}
	if sink.drift != 2 {
		t.Errorf("drift = %d, want 2", sink.drift)
	}
	if sink.outcomes["promoted"] != 1 || sink.outcomes["rolled-back"] != 1 {
		t.Errorf("outcomes = %v, want one promoted and one rolled-back", sink.outcomes)
	}
	if sink.tick != 3*time.Second {
		t.Errorf("tick = %s, want 3s", sink.tick)
	}
}

func TestRecordTick_NilSinkIsANoop(t *testing.T) {
	recordTick(nil, time.Second, []RepoOutcome{{Repo: "a"}})
}

// Item 5's second alert: a rollout held in deploying/verifying is the
// 2026-09-19 wedge. The gauge reports the oldest such rollout, per target's
// latest rollout only; paused and awaiting-approval are waits a human chose.
func TestOldestInFlight(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	got := oldestInFlight(now, []rollout.Rollout{
		{TargetRef: "a", Phase: rollout.PhaseDeploying, UpdatedAt: at(90)},
		{TargetRef: "a", Phase: rollout.PhaseDeploying, UpdatedAt: at(500)}, // older row, superseded
		{TargetRef: "b", Phase: rollout.PhaseVerifying, UpdatedAt: at(10)},
		{TargetRef: "c", Phase: rollout.PhasePaused, UpdatedAt: at(900)},
		{TargetRef: "d", Phase: rollout.PhaseAwaitingApproval, UpdatedAt: at(900)},
		{TargetRef: "e", Phase: rollout.PhasePromoted, UpdatedAt: at(900)},
	})
	if got != (90 * time.Minute).Seconds() {
		t.Fatalf("oldestInFlight = %v s, want %v s", got, (90 * time.Minute).Seconds())
	}
	if oldestInFlight(now, nil) != 0 {
		t.Fatal("no rollouts in flight must report 0")
	}
}
