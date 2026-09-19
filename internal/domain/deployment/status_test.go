package deployment_test

import (
	"testing"

	"go.klarlabs.de/rollops/internal/domain/deployment"
)

// every status the machine knows about. The list is written out rather than
// exported from the package so that adding a status without deciding how it
// connects to the rest shows up here as a gap rather than passing silently.
var all = []deployment.Status{
	deployment.StatusPlanned,
	deployment.StatusAwaitingApproval,
	deployment.StatusQueued,
	deployment.StatusApplying,
	deployment.StatusVerifying,
	deployment.StatusPaused,
	deployment.StatusPromoting,
	deployment.StatusSucceeded,
	deployment.StatusFailed,
	deployment.StatusRollingBack,
	deployment.StatusRolledBack,
	deployment.StatusCancelled,
}

func TestEveryStatusIsRecognised(t *testing.T) {
	for _, s := range all {
		if !s.Valid() {
			t.Errorf("%s is not recognised", s)
		}
	}
	if deployment.Status("deploying").Valid() {
		t.Error("an unknown status was recognised")
	}
}

func TestTheTerminalStatusesAreTheOnesThatEndADeployment(t *testing.T) {
	terminal := map[deployment.Status]bool{
		deployment.StatusSucceeded:  true,
		deployment.StatusFailed:     true,
		deployment.StatusRolledBack: true,
		deployment.StatusCancelled:  true,
	}
	for _, s := range all {
		if got := s.IsTerminal(); got != terminal[s] {
			t.Errorf("%s.IsTerminal() = %v, want %v", s, got, terminal[s])
		}
	}
}

// A terminal status is the end of the record. Allowing anything out of one
// would mean a deployment that reported success could later report failure,
// and every consumer of the timeline would have to guess which was true.
func TestNothingLeavesATerminalStatus(t *testing.T) {
	for _, from := range all {
		if !from.IsTerminal() {
			continue
		}
		for _, to := range all {
			if from.CanTransitionTo(to) {
				t.Errorf("%s -> %s is allowed but %s is terminal", from, to, from)
			}
		}
	}
}

// A self-transition is a no-op that reads as progress. Refusing it means an
// event that did not move the deployment is reported rather than swallowed.
func TestAStatusDoesNotTransitionToItself(t *testing.T) {
	for _, s := range all {
		if s.CanTransitionTo(s) {
			t.Errorf("%s transitions to itself", s)
		}
	}
}

// A deployment that cannot reach a terminal status is stuck forever: it holds
// the environment, never releases its concurrency slot, and nothing in the
// timeline ever explains why. This is the property that makes the table safe to
// extend, so it is asserted rather than eyeballed.
func TestEveryStatusCanReachATerminalOne(t *testing.T) {
	for _, start := range all {
		if start.IsTerminal() {
			continue
		}
		seen := map[deployment.Status]bool{start: true}
		queue := []deployment.Status{start}
		reached := false
		for len(queue) > 0 && !reached {
			cur := queue[0]
			queue = queue[1:]
			for _, next := range all {
				if !cur.CanTransitionTo(next) || seen[next] {
					continue
				}
				if next.IsTerminal() {
					reached = true
					break
				}
				seen[next] = true
				queue = append(queue, next)
			}
		}
		if !reached {
			t.Errorf("%s cannot reach a terminal status", start)
		}
	}
}

// An unknown status is not a state the machine can be in, so it neither leaves
// nor is entered. A typo in stored data fails closed.
func TestAnUnknownStatusIsIsolated(t *testing.T) {
	unknown := deployment.Status("deploying")
	for _, s := range all {
		if unknown.CanTransitionTo(s) {
			t.Errorf("unknown -> %s is allowed", s)
		}
		if s.CanTransitionTo(unknown) {
			t.Errorf("%s -> unknown is allowed", s)
		}
	}
}

// Cancellation does not imply rollback (spec 32). The two are different
// outcomes and a cancelled deployment must not be recorded as reverted, because
// whether anything was partially applied is exactly what an operator needs to
// know afterwards.
func TestCancellingIsNotRollingBack(t *testing.T) {
	if !deployment.StatusApplying.CanTransitionTo(deployment.StatusCancelled) {
		t.Error("an applying deployment cannot be cancelled")
	}
	if deployment.StatusCancelled.CanTransitionTo(deployment.StatusRolledBack) {
		t.Error("cancelling led to rolled back")
	}
}

// A rollback either completes or fails; it never reports success, because what
// succeeded was the undoing rather than the deployment.
func TestARollbackDoesNotSucceed(t *testing.T) {
	if deployment.StatusRollingBack.CanTransitionTo(deployment.StatusSucceeded) {
		t.Error("rolling back led to succeeded")
	}
	if !deployment.StatusRollingBack.CanTransitionTo(deployment.StatusRolledBack) {
		t.Error("rolling back cannot complete")
	}
	if !deployment.StatusRollingBack.CanTransitionTo(deployment.StatusFailed) {
		t.Error("a rollback that fails has nowhere to go")
	}
}

// An approval gate is only a gate if nothing can go round it. A queued
// deployment is one step from applying, so the gate has to sit before it.
func TestApprovalIsNotReachableAfterTheGate(t *testing.T) {
	for _, s := range []deployment.Status{
		deployment.StatusQueued,
		deployment.StatusApplying,
		deployment.StatusVerifying,
		deployment.StatusPromoting,
	} {
		if s.CanTransitionTo(deployment.StatusAwaitingApproval) {
			t.Errorf("%s can return to awaiting approval", s)
		}
	}
}
