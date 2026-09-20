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
	"go.klarlabs.de/rollops/internal/domain/plan"
)

// wayBack is what a planner proposes when the change it planned can be undone.
// The harness's planner proposes none by default, because a plan need not
// declare one (§4) and the tests that care should say so out loud.
func wayBack(h *harness) plan.RollbackPlan {
	return plan.RollbackPlan{
		FromRelease: h.release.ID,
		ToRelease:   "rel_previous",
		Operations: []plan.PlannedOperation{{
			ID:         "op_r1",
			Target:     "primary",
			Kind:       plan.OperationRollback,
			Summary:    "Restore the previous image digest",
			Reversible: true,
		}},
	}
}

// reversible is a deployment that has been applied, verified, and whose plan
// declares somewhere to return to.
func (h *harness) reversible(t *testing.T) deployment.Deployment {
	t.Helper()
	h.planner.proposal.Rollback = wayBack(h)
	return h.verified(t)
}

func (h *harness) rollback(
	t *testing.T, d deployment.Deployment, reason string,
) deployment.Deployment {
	t.Helper()
	out, err := h.service.Rollback(context.Background(), deploy.RollbackCommand{
		DeploymentID: d.ID,
		Reason:       reason,
		Actor:        actor(),
	})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	return out
}

// Rolling back records an intent and stops, for the reason promoting does:
// running the plan's rollback operations against the substrate is the engine's
// work, and doing it here would break the line this package holds.
func TestRollingBackHandsTheReversalToTheEngineRatherThanPerformingIt(t *testing.T) {
	h := newHarness(t)
	d := h.reversible(t)

	out := h.rollback(t, d, "the error rate did not come down")

	if out.Status != deployment.StatusRollingBack {
		t.Errorf("status = %q, want rolling_back", out.Status)
	}
	if out.FinishedAt != nil {
		t.Error("rolling back finished the deployment; reaching rolled_back does that")
	}
}

// A plan need not declare a way back — "claiming one that does not exist is
// worse than admitting none". Refusing here tells an operator to deploy the
// previous release forward instead, which is the honest answer; moving to
// rolling_back would hand the engine an empty instruction and leave the
// deployment stuck in a status nothing can advance.
func TestADeploymentWhosePlanDeclaresNoWayBackIsNotRolledBack(t *testing.T) {
	h := newHarness(t)
	d := h.verified(t)

	_, err := h.service.Rollback(context.Background(), deploy.RollbackCommand{
		DeploymentID: d.ID,
		Reason:       "the error rate did not come down",
		Actor:        actor(),
	})

	if !errors.Is(err, deploy.ErrNoWayBack) {
		t.Fatalf("err = %v, want ErrNoWayBack", err)
	}
	stored, err := h.store.Deployments().Get(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != deployment.StatusVerifying {
		t.Errorf("status = %q, want verifying", stored.Status)
	}
}

// Where a rollback is going is the whole point of recording that it started: an
// operator watching one wants to know which release is coming back, and the
// plan that says so expires.
func TestTheTimelineSaysWhichReleaseIsComingBack(t *testing.T) {
	h := newHarness(t)
	d := h.reversible(t)

	h.rollback(t, d, "the error rate did not come down")

	e := find(t, h.timeline(t), event.RollbackStarted)
	if e.AggregateID != string(d.ID) {
		t.Errorf("filed against %q, want %q", e.AggregateID, d.ID)
	}
	payload := decode(t, e)
	if payload["to_release"] != "rel_previous" {
		t.Errorf("to_release = %v, want rel_previous", payload["to_release"])
	}
	if payload["from"] != string(deployment.StatusVerifying) {
		t.Errorf("from = %v, want verifying", payload["from"])
	}
	if payload["reason"] != "the error rate did not come down" {
		t.Errorf("reason = %v, want what the operator said", payload["reason"])
	}
}

// Unlike promoting past a pause, this needs no justification. Rolling back
// heeds a signal rather than disregarding one, and onFailure: rollback fires
// without a person present to write prose — requiring it would make the
// configured branch of §11.4 impossible to take.
func TestRollingBackNeedNotSayWhy(t *testing.T) {
	h := newHarness(t)

	out := h.rollback(t, h.reversible(t), "")

	if out.Status != deployment.StatusRollingBack {
		t.Errorf("status = %q, want rolling_back", out.Status)
	}
}

func TestAPausedDeploymentCanBeRolledBack(t *testing.T) {
	h := newHarness(t)
	h.planner.proposal.Rollback = wayBack(h)

	out := h.rollback(t, h.halted(t), "")

	if out.Status != deployment.StatusRollingBack {
		t.Errorf("status = %q, want rolling_back", out.Status)
	}
}

// The refusal is the state machine's: what may be rolled back is whatever has
// an edge to `rolling_back` (§4.8). A queued deployment has changed nothing, so
// there is nothing to put back.
func TestADeploymentThatHasChangedNothingCannotBeRolledBack(t *testing.T) {
	h := newHarness(t)
	h.planner.proposal.Rollback = wayBack(h)
	queued := h.apply(t, h.plan(t))

	_, err := h.service.Rollback(context.Background(), deploy.RollbackCommand{
		DeploymentID: queued.ID,
		Actor:        actor(),
	})

	if !errors.Is(err, deploy.ErrNotReversible) {
		t.Fatalf("err = %v, want ErrNotReversible", err)
	}
}

func TestRollingBackADeploymentNobodyCreatedIsNotFound(t *testing.T) {
	h := newHarness(t)
	absent, err := identity.NewDeploymentID(h.gen)
	if err != nil {
		t.Fatalf("NewDeploymentID: %v", err)
	}

	_, err = h.service.Rollback(context.Background(), deploy.RollbackCommand{
		DeploymentID: absent,
		Actor:        actor(),
	})

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
