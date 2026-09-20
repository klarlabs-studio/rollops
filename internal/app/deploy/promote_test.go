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
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// verified is a deployment whose checks were happy. It is still `verifying`:
// what finishes it is being promoted, which is what these tests are about.
func (h *harness) verified(t *testing.T) deployment.Deployment {
	t.Helper()
	d, _ := h.verify(t, h.applied(t))
	return d
}

// halted is a deployment paused by a verdict that was not a pass. Promoting out
// of here is the override §11.4 calls "promote anyway".
func (h *harness) halted(t *testing.T) deployment.Deployment {
	t.Helper()
	h.verifier.checks = []deploy.CheckResult{answered(verifyv1.VerdictFail)}
	d, _ := h.verify(t, h.applied(t))
	return d
}

func (h *harness) promote(
	t *testing.T, d deployment.Deployment, reason string,
) deployment.Deployment {
	t.Helper()
	out, err := h.service.Promote(context.Background(), deploy.PromoteCommand{
		DeploymentID: d.ID,
		Reason:       reason,
		Actor:        actor(),
	})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	return out
}

// Promoting records an intent and stops. Rolling the release out to the rest of
// the fleet is the engine's work, and a service that did it here would be the
// one place in this package that mutates a substrate.
func TestPromotingHandsTheDeploymentToTheEngineRatherThanFinishingIt(t *testing.T) {
	h := newHarness(t)
	d := h.verified(t)

	out := h.promote(t, d, "")

	if out.Status != deployment.StatusPromoting {
		t.Errorf("status = %q, want promoting", out.Status)
	}
	if out.FinishedAt != nil {
		t.Error("promoting finished the deployment; only reaching an outcome does that")
	}
}

// A verification that passed is the ordinary road to promotion, and asking an
// operator to justify the expected next step trains them to type anything.
func TestPromotingAVerifiedDeploymentNeedsNoJustification(t *testing.T) {
	h := newHarness(t)

	if _, err := h.service.Promote(context.Background(), deploy.PromoteCommand{
		DeploymentID: h.verified(t).ID,
		Actor:        actor(),
	}); err != nil {
		t.Fatalf("Promote: %v", err)
	}
}

// §11.4 makes "promote anyway" one of the branches onFailure may take, so this
// is allowed — but a deployment is only paused because something said not to
// continue, and whoever finds it promoted has to be able to find out who
// overrode what. It is the rule a cancellation is held to, for the same reason.
func TestPromotingPastAPauseHasToSayWhy(t *testing.T) {
	h := newHarness(t)
	d := h.halted(t)

	_, err := h.service.Promote(context.Background(), deploy.PromoteCommand{
		DeploymentID: d.ID,
		Actor:        actor(),
	})

	if !errors.Is(err, deploy.ErrUnexplainedPromotion) {
		t.Fatalf("err = %v, want ErrUnexplainedPromotion", err)
	}
	stored, err := h.store.Deployments().Get(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != deployment.StatusPaused {
		t.Errorf("status = %q, want paused", stored.Status)
	}
}

func TestAnExplainedOverridePromotesAPausedDeployment(t *testing.T) {
	h := newHarness(t)

	out := h.promote(t, h.halted(t), "the probe was pointed at the old ingress")

	if out.Status != deployment.StatusPromoting {
		t.Errorf("status = %q, want promoting", out.Status)
	}
}

// The status promoted out of is what tells an override from the ordinary road,
// and `promoting` alone does not distinguish them.
func TestTheTimelineSaysWhatThePromotionOverrode(t *testing.T) {
	h := newHarness(t)

	h.promote(t, h.halted(t), "the probe was pointed at the old ingress")

	payload := decode(t, find(t, h.timeline(t), event.DeploymentPromotionStarted))
	if payload["from"] != string(deployment.StatusPaused) {
		t.Errorf("from = %v, want paused", payload["from"])
	}
	if payload["reason"] != "the probe was pointed at the old ingress" {
		t.Errorf("reason = %v, want the override's justification", payload["reason"])
	}
}

func TestPromotingRecordsThatItStartedAgainstTheDeployment(t *testing.T) {
	h := newHarness(t)
	d := h.verified(t)

	h.promote(t, d, "")

	e := find(t, h.timeline(t), event.DeploymentPromotionStarted)
	if e.AggregateID != string(d.ID) {
		t.Errorf("filed against %q, want %q", e.AggregateID, d.ID)
	}
	if decode(t, e)["plan_id"] != string(d.PlanID) {
		t.Errorf("payload does not carry the plan: %s", e.Payload)
	}
}

// The refusal is the state machine's rather than a list of statuses kept here:
// what may be promoted is whatever has an edge to `promoting` (§4.8).
func TestADeploymentThatHasNotBeenVerifiedCannotBePromoted(t *testing.T) {
	h := newHarness(t)
	queued := h.apply(t, h.plan(t))

	_, err := h.service.Promote(context.Background(), deploy.PromoteCommand{
		DeploymentID: queued.ID,
		Reason:       "ship it",
		Actor:        actor(),
	})

	if !errors.Is(err, deploy.ErrNotPromotable) {
		t.Fatalf("err = %v, want ErrNotPromotable", err)
	}
}

func TestPromotingADeploymentNobodyCreatedIsNotFound(t *testing.T) {
	h := newHarness(t)
	absent, err := identity.NewDeploymentID(h.gen)
	if err != nil {
		t.Fatalf("NewDeploymentID: %v", err)
	}

	_, err = h.service.Promote(context.Background(), deploy.PromoteCommand{
		DeploymentID: absent,
		Reason:       "ship it",
		Actor:        actor(),
	})

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
