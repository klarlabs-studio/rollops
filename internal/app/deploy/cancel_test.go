package deploy_test

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

func (h *harness) cancel(t *testing.T, cmd deploy.CancelCommand) (deployment.Deployment, error) {
	t.Helper()
	return h.service.Cancel(context.Background(), cmd)
}

// A deployment admitted and not yet picked up is the commonest thing to want
// stopped: somebody has noticed the release is wrong while it is still sitting
// in the queue.
func TestCancellingAQueuedDeploymentStopsIt(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))

	out, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: d.ID,
		Reason:       "the release was cut from the wrong branch",
		Actor:        actor(),
	})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if out.Status != deployment.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", out.Status)
	}
	if out.FinishedAt == nil {
		t.Error("a cancelled deployment has no finish time; nothing can say how long it was open")
	}

	stored, err := h.store.Deployments().Get(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != deployment.StatusCancelled {
		t.Errorf("persisted status = %q, want cancelled", stored.Status)
	}
}

// Cancelling frees the environment. Without this the refusal that protects one
// deployment at a time would outlive the deployment it was protecting, and the
// environment would be unusable until something else moved it.
func TestAnEnvironmentIsFreeAgainOnceItsDeploymentIsCancelled(t *testing.T) {
	h := newHarness(t)
	first := h.apply(t, h.plan(t))
	if _, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: first.ID,
		Reason:       "wrong branch",
		Actor:        actor(),
	}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	second, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID:  h.plan(t).ID,
		Trigger: deployment.Trigger{Type: deployment.TriggerManual, Detail: "the right one this time"},
		Actor:   actor(),
	})
	if err != nil {
		t.Fatalf("Apply after a cancellation: %v", err)
	}
	if second.Status != deployment.StatusQueued {
		t.Errorf("status = %q, want queued", second.Status)
	}
}

// A deployment at the gate is cancellable too. An operator who knows the
// approval will never come should not have to find an approver to deny it.
func TestADeploymentWaitingAtTheGateCanBeCancelled(t *testing.T) {
	h := newHarness(t)
	_, d := h.gated(t, 1)

	out, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: d.ID,
		Reason:       "superseded by 2.1.0",
		Actor:        actor(),
	})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if out.Status != deployment.StatusCancelled {
		t.Errorf("status = %q, want cancelled", out.Status)
	}
}

// Past the end there is nothing to stop. Reporting success would tell an
// operator they had prevented something that had already happened.
func TestADeploymentThatHasFinishedCannotBeCancelled(t *testing.T) {
	h := newHarness(t)
	done := h.finish(t, h.apply(t, h.plan(t)), deployment.StatusSucceeded)

	_, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: done.ID,
		Reason:       "changed my mind",
		Actor:        actor(),
	})

	if !errors.Is(err, deploy.ErrNotCancellable) {
		t.Fatalf("err = %v, want ErrNotCancellable", err)
	}
	stored, _ := h.store.Deployments().Get(context.Background(), done.ID)
	if stored.Status != deployment.StatusSucceeded {
		t.Errorf("status = %q; a finished deployment was moved", stored.Status)
	}
}

// Cancelling twice is refused rather than being quietly accepted. The second
// caller is not repeating themselves — they are acting on a view of the world
// in which the deployment was still running.
func TestADeploymentAlreadyCancelledCannotBeCancelledAgain(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))
	cmd := deploy.CancelCommand{DeploymentID: d.ID, Reason: "wrong branch", Actor: actor()}
	if _, err := h.cancel(t, cmd); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}

	_, err := h.cancel(t, cmd)

	if !errors.Is(err, deploy.ErrNotCancellable) {
		t.Errorf("err = %v, want ErrNotCancellable", err)
	}
}

// Whoever finds the deployment stopped has to be able to find out why, which is
// the same rule a denied approval is held to.
func TestACancellationMustSayWhy(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))

	_, err := h.cancel(t, deploy.CancelCommand{DeploymentID: d.ID, Actor: actor()})

	if !errors.Is(err, deploy.ErrUnexplainedCancellation) {
		t.Fatalf("err = %v, want ErrUnexplainedCancellation", err)
	}
	stored, _ := h.store.Deployments().Get(context.Background(), d.ID)
	if stored.Status != deployment.StatusQueued {
		t.Errorf("status = %q; the deployment was stopped by a command that was refused", stored.Status)
	}
}

func TestCancellingADeploymentNobodyStartedIsNotFound(t *testing.T) {
	h := newHarness(t)

	_, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: identity.DeploymentID("dep_0199a0dd-0000-7000-8000-000000000009"),
		Reason:       "wrong branch",
		Actor:        actor(),
	})

	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// The timeline is the only place the reason survives, and without it a
// deployment in the cancelled status is indistinguishable from one that died.
func TestACancellationSaysWhoStoppedItAndWhy(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))
	by := approver("ana")

	if _, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: d.ID,
		Reason:       "the release was cut from the wrong branch",
		Actor:        by,
	}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	e := find(t, h.timeline(t), event.DeploymentCancelled)
	if e.AggregateID != string(d.ID) {
		t.Errorf("aggregate = %q, want the deployment %q", e.AggregateID, d.ID)
	}
	if e.Principal.ID != by.ID {
		t.Errorf("actor = %q, want %q", e.Principal.ID, by.ID)
	}
	if got := decode(t, e)["reason"]; got != "the release was cut from the wrong branch" {
		t.Errorf("reason = %v", got)
	}
	if got := decode(t, e)["from"]; got != string(deployment.StatusQueued) {
		t.Errorf("from = %v, want the status it was stopped out of", got)
	}
}

// The cancellation hangs off the same intent as the plan and the apply, so an
// operator reading one correlation id sees the whole story rather than a stop
// with no beginning.
func TestACancellationJoinsTheIntentItStopped(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))
	if _, err := h.cancel(t, deploy.CancelCommand{
		DeploymentID: d.ID,
		Reason:       "wrong branch",
		Actor:        actor(),
	}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	es := h.timeline(t)
	root := find(t, es, event.DeploymentPlanCreated)
	stopped := find(t, es, event.DeploymentCancelled)
	if stopped.CorrelationID != root.CorrelationID {
		t.Errorf("correlation = %q, want the plan's %q", stopped.CorrelationID, root.CorrelationID)
	}
}

// A deployment cancelled by a denied approval already carries the rejection.
// Recording a cancellation beside it would put two causes on the timeline for
// one act, and an operator counting stops would count this one twice.
func TestADenialCancelsWithoutRecordingASecondCause(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	out, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID,
		Revision:     p.Hash,
		Decision:     policy.ApprovalDenied,
		Reason:       "the migration has not been rehearsed",
		Actor:        approver("ana"),
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if out.Status != deployment.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", out.Status)
	}

	for _, e := range h.timeline(t) {
		if e.Type == event.DeploymentCancelled {
			t.Errorf("a denial recorded %s beside its rejection", e.Type)
		}
	}
}
