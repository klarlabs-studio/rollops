package apiv2_test

import (
	"context"
	"testing"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
)

// engineMoved drives a deployment to a status only the engine reaches. Nothing
// in the API does this — applying and verifying are the engine's work — so a
// test that needs a deployment past the queue writes the transition the engine
// would have written, through the state machine rather than around it.
func (s scene) engineMoved(
	t *testing.T, d apiv2.Deployment, to deployment.Status,
) apiv2.Deployment {
	t.Helper()
	ctx := context.Background()
	id, err := identity.ParseDeploymentID(d.ID)
	if err != nil {
		t.Fatalf("deployment id: %v", err)
	}
	stored, err := s.store.Deployments().Get(ctx, id)
	if err != nil {
		t.Fatalf("read deployment: %v", err)
	}
	moved, err := stored.TransitionTo(to, s.clock.Now())
	if err != nil {
		t.Fatalf("transition to %s: %v", to, err)
	}
	if _, err := s.store.Deployments().Update(ctx, moved); err != nil {
		t.Fatalf("store %s: %v", to, err)
	}
	d.Status = string(to)
	return d
}

// serving is a deployment the engine has applied and whose checks are running,
// which is where promotion is asked for.
func (s scene) serving(t *testing.T) apiv2.Deployment {
	t.Helper()
	d := s.engineMoved(t, s.queued(t), deployment.StatusApplying)
	return s.engineMoved(t, d, deployment.StatusVerifying)
}

// halted is a deployment the engine paused, which is where an override and a
// rollback are both asked for.
func (s scene) halted(t *testing.T) apiv2.Deployment {
	t.Helper()
	return s.engineMoved(t, s.serving(t), deployment.StatusPaused)
}

func (s scene) promoting(d apiv2.Deployment, reason, key string) apiv2.PromoteDeploymentRequest {
	return apiv2.PromoteDeploymentRequest{
		DeploymentID:   d.ID,
		Reason:         reason,
		Actor:          planner,
		IdempotencyKey: key,
	}
}

func (s scene) rollingBack(d apiv2.Deployment, key string) apiv2.RollbackDeploymentRequest {
	return apiv2.RollbackDeploymentRequest{
		DeploymentID:   d.ID,
		Reason:         "the error rate did not come down",
		Actor:          planner,
		IdempotencyKey: key,
	}
}

func TestPromotingADeploymentHandsItToTheEngine(t *testing.T) {
	s := setup(t).scene(t)
	d := s.serving(t)

	got, err := s.svc.PromoteDeployment(context.Background(), s.promoting(d, "", ""))
	if err != nil {
		t.Fatalf("PromoteDeployment: %v", err)
	}
	if got.ID != d.ID {
		t.Errorf("deployment = %q, want the %q that was promoted", got.ID, d.ID)
	}
	if got.Status != string(deployment.StatusPromoting) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusPromoting)
	}
}

// Promoting past a pause disregards whatever paused it, so it has to say who
// decided that. A request that does not is the caller's to fix.
func TestPromotingPastAPauseWithoutSayingWhyIsTheCallersMistake(t *testing.T) {
	s := setup(t).scene(t)

	_, err := s.svc.PromoteDeployment(context.Background(), s.promoting(s.halted(t), "", ""))

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAnExplainedOverridePromotesAPausedDeployment(t *testing.T) {
	s := setup(t).scene(t)

	got, err := s.svc.PromoteDeployment(context.Background(),
		s.promoting(s.halted(t), "the probe was pointed at the old ingress", ""))
	if err != nil {
		t.Fatalf("PromoteDeployment: %v", err)
	}
	if got.Status != string(deployment.StatusPromoting) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusPromoting)
	}
}

// A deployment still in the queue has changed nothing there is anything to
// widen. The request is well formed and the world will not take it.
func TestPromotingADeploymentThatIsStillQueuedIsAConflict(t *testing.T) {
	s := setup(t).scene(t)

	_, err := s.svc.PromoteDeployment(context.Background(), s.promoting(s.queued(t), "", ""))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestAMalformedPromoteRequestIsTheCallersMistake(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*apiv2.PromoteDeploymentRequest)
	}{
		{"not an id at all", func(r *apiv2.PromoteDeploymentRequest) { r.DeploymentID = "nonsense" }},
		{"an id of the wrong kind", func(r *apiv2.PromoteDeploymentRequest) {
			r.DeploymentID = "pln_0199a0dd-0000-7000-8000-000000000000"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t).scene(t)
			req := s.promoting(s.serving(t), "", "")
			tc.spoil(&req)

			_, err := s.svc.PromoteDeployment(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

func TestTwoPromoteCallsWithOneKeyPromoteOnce(t *testing.T) {
	s := setup(t).scene(t)
	d := s.serving(t)

	first, err := s.svc.PromoteDeployment(context.Background(), s.promoting(d, "", "k-1"))
	if err != nil {
		t.Fatalf("first PromoteDeployment: %v", err)
	}
	second, err := s.svc.PromoteDeployment(context.Background(), s.promoting(d, "", "k-1"))
	if err != nil {
		t.Fatalf("second PromoteDeployment: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("the retry answered about %q, want %q", second.ID, first.ID)
	}
}

// The reason is part of what the key was used for: a retry that changes it is
// a different override, and answering it with the first one's record would
// attribute the decision to prose nobody wrote.
func TestAPromoteKeyReusedWithADifferentReasonIsRefused(t *testing.T) {
	s := setup(t).scene(t)
	d := s.halted(t)
	if _, err := s.svc.PromoteDeployment(context.Background(),
		s.promoting(d, "the probe was wrong", "k-1")); err != nil {
		t.Fatalf("first PromoteDeployment: %v", err)
	}

	_, err := s.svc.PromoteDeployment(context.Background(),
		s.promoting(d, "we are shipping regardless", "k-1"))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// reversible gives the planner a way back, so that the plans made after this
// declare one. It has to be set before the plan is built.
func (s scene) reversible() {
	s.proposals.proposal.Rollback = plan.RollbackPlan{
		FromRelease: s.release.ID,
		ToRelease:   "rel_previous",
		Operations: []plan.PlannedOperation{{
			ID:         "op_r1",
			Target:     "api",
			Kind:       plan.OperationRollback,
			Summary:    "Restore the previous image digest",
			Reversible: true,
		}},
	}
}

func TestRollingBackADeploymentHandsTheReversalToTheEngine(t *testing.T) {
	s := setup(t).scene(t)
	s.reversible()
	d := s.serving(t)

	got, err := s.svc.RollbackDeployment(context.Background(), s.rollingBack(d, ""))
	if err != nil {
		t.Fatalf("RollbackDeployment: %v", err)
	}
	if got.ID != d.ID {
		t.Errorf("deployment = %q, want the %q that was reversed", got.ID, d.ID)
	}
	if got.Status != string(deployment.StatusRollingBack) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusRollingBack)
	}
}

// A plan that declares no way back cannot be rolled back. The request is well
// formed and what is wrong is the plan it names, which is a conflict rather
// than something the caller can fix by editing the request.
func TestRollingBackWithNoWayBackIsAConflict(t *testing.T) {
	s := setup(t).scene(t)

	_, err := s.svc.RollbackDeployment(context.Background(), s.rollingBack(s.serving(t), ""))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// Unlike promoting past a pause, this needs no justification: onFailure:
// rollback fires with nobody present to write prose.
func TestRollingBackNeedNotSayWhy(t *testing.T) {
	s := setup(t).scene(t)
	s.reversible()
	req := s.rollingBack(s.serving(t), "")
	req.Reason = ""

	if _, err := s.svc.RollbackDeployment(context.Background(), req); err != nil {
		t.Fatalf("RollbackDeployment: %v", err)
	}
}

func TestAMalformedRollbackRequestIsTheCallersMistake(t *testing.T) {
	s := setup(t).scene(t)
	req := s.rollingBack(s.serving(t), "")
	req.DeploymentID = "nonsense"

	_, err := s.svc.RollbackDeployment(context.Background(), req)

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestRollingBackADeploymentNobodyStartedIsNotFound(t *testing.T) {
	absent := setup(t).scene(t).queued(t)
	s := setup(t).scene(t)

	_, err := s.svc.RollbackDeployment(context.Background(), s.rollingBack(absent, ""))

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestTwoRollbackCallsWithOneKeyReverseOnce(t *testing.T) {
	s := setup(t).scene(t)
	s.reversible()
	d := s.serving(t)

	first, err := s.svc.RollbackDeployment(context.Background(), s.rollingBack(d, "k-1"))
	if err != nil {
		t.Fatalf("first RollbackDeployment: %v", err)
	}
	second, err := s.svc.RollbackDeployment(context.Background(), s.rollingBack(d, "k-1"))
	if err != nil {
		t.Fatalf("second RollbackDeployment: %v", err)
	}

	if first.ID != second.ID {
		t.Errorf("the retry answered about %q, want %q", second.ID, first.ID)
	}
}
