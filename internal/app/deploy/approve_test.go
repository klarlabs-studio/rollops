package deploy_test

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// needsApproval is the decision that sends a deployment to the gate: allowed,
// but with a condition nobody has met yet.
func needsApproval(count int) policy.Decision {
	d := allowed()
	d.Requirements = []policy.Requirement{{Type: policy.RequireApproval, Count: count}}
	return d
}

func approver(id string) identity.Principal {
	return identity.Principal{ID: id, Type: identity.PrincipalHuman, DisplayName: id}
}

// gated plans and applies with an approval requirement, returning the plan and
// the deployment waiting on it.
func (h *harness) gated(t *testing.T, count int) (plan.DeploymentPlan, deployment.Deployment) {
	t.Helper()
	h.policy.decision = needsApproval(count)
	p := h.plan(t)
	d := h.apply(t, p)
	if d.Status != deployment.StatusAwaitingApproval {
		t.Fatalf("status = %q, want awaiting_approval", d.Status)
	}
	return p, d
}

func (h *harness) approve(t *testing.T, cmd deploy.ApproveCommand) (deployment.Deployment, error) {
	t.Helper()
	return h.service.Approve(context.Background(), cmd)
}

// TestApprovingThePlanQueuesTheDeployment is the path that was missing: a
// deployment could enter the gate and nothing could take it out again.
func TestApprovingThePlanQueuesTheDeployment(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	out, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID,
		Revision:     p.Hash,
		Decision:     policy.ApprovalGranted,
		Actor:        approver("ana"),
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if out.Status != deployment.StatusQueued {
		t.Fatalf("status = %q, want queued", out.Status)
	}

	stored, err := h.store.Deployments().Get(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != deployment.StatusQueued {
		t.Errorf("persisted status = %q, want queued", stored.Status)
	}
}

// TestApprovingAPlanTheApproverDidNotReadIsRefused is §13's MUST at the
// boundary. The approver states the revision they read; if the stored plan is
// not that one, their approval is about something else.
func TestApprovingAPlanTheApproverDidNotReadIsRefused(t *testing.T) {
	h := newHarness(t)
	_, d := h.gated(t, 1)

	_, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID,
		Revision:     digest.Of([]byte("a plan the approver never read")),
		Decision:     policy.ApprovalGranted,
		Actor:        approver("ana"),
	})
	if !errors.Is(err, plan.ErrPlanStale) {
		t.Fatalf("err = %v, want ErrPlanStale", err)
	}

	stored, _ := h.store.Deployments().Get(context.Background(), d.ID)
	if stored.Status != deployment.StatusAwaitingApproval {
		t.Errorf("status = %q, want the deployment still at the gate", stored.Status)
	}
	approvals, _ := h.store.Approvals().ListForSubject(context.Background(), "plan", string(stored.PlanID))
	if len(approvals) != 0 {
		t.Errorf("a refused approval was recorded anyway: %+v", approvals)
	}
}

// TestApprovalMustStateARevision: an approval that names no revision is not
// bound to anything, and accepting one would make the binding optional.
func TestApprovalMustStateARevision(t *testing.T) {
	h := newHarness(t)
	_, d := h.gated(t, 1)

	_, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID,
		Decision:     policy.ApprovalGranted,
		Actor:        approver("ana"),
	})
	if err == nil {
		t.Fatal("an approval stating no revision was accepted")
	}
}

// TestOneApprovalDoesNotSatisfyTwo keeps the deployment at the gate until the
// requirement is actually met, and records the first approval so the second
// approver can see it.
func TestOneApprovalDoesNotSatisfyTwo(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 2)

	out, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalGranted, Actor: approver("ana"),
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if out.Status != deployment.StatusAwaitingApproval {
		t.Fatalf("status = %q, want the deployment still waiting", out.Status)
	}

	out, err = h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalGranted, Actor: approver("ben"),
	})
	if err != nil {
		t.Fatalf("second Approve: %v", err)
	}
	if out.Status != deployment.StatusQueued {
		t.Errorf("status after two approvals = %q, want queued", out.Status)
	}
}

// TestTheSamePersonCannotApproveTwice: two approvals are two people, and the
// service must not let one approver clear a four-eyes gate by answering again.
func TestTheSamePersonCannotApproveTwice(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 2)

	for range 2 {
		out, err := h.approve(t, deploy.ApproveCommand{
			DeploymentID: d.ID, Revision: p.Hash,
			Decision: policy.ApprovalGranted, Actor: approver("ana"),
		})
		if err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if out.Status != deployment.StatusAwaitingApproval {
			t.Fatalf("status = %q, want the gate to hold", out.Status)
		}
	}
}

// TestARejectionCancelsTheDeployment: a refusal is an answer, not a pause.
func TestARejectionCancelsTheDeployment(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	out, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalDenied, Reason: "migration not rehearsed",
		Actor: approver("ana"),
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if out.Status != deployment.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", out.Status)
	}
}

// TestARejectionMustSayWhy: whoever is stopped has to be told what stopped
// them, and after the fact the record has to still say it.
func TestARejectionMustSayWhy(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	_, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalDenied, Actor: approver("ana"),
	})
	if err == nil {
		t.Fatal("a rejection with no reason was accepted")
	}
}

// TestApprovingAnUngatedDeploymentIsRefused: a deployment already queued or
// running has passed the point where an approval means anything, and accepting
// one would record consent for work already done.
func TestApprovingAnUngatedDeploymentIsRefused(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)
	d := h.apply(t, p)
	if d.Status != deployment.StatusQueued {
		t.Fatalf("status = %q, want queued", d.Status)
	}

	_, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalGranted, Actor: approver("ana"),
	})
	if !errors.Is(err, deploy.ErrNotAwaitingApproval) {
		t.Fatalf("err = %v, want ErrNotAwaitingApproval", err)
	}
}

// TestAnApprovalIsRecordedAgainstThePlan: the approval has to outlive the call,
// bound to what it approved, or nothing downstream can audit why this ran.
func TestAnApprovalIsRecordedAgainstThePlan(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	if _, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalGranted, Actor: approver("ana"),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := h.store.Approvals().ListForSubject(context.Background(), "plan", string(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("recorded %d approvals, want 1", len(got))
	}
	if got[0].Subject.Revision != p.Hash.String() {
		t.Errorf("approval bound to %q, want the plan hash %q", got[0].Subject.Revision, p.Hash)
	}
	if got[0].Principal.ID != "ana" {
		t.Errorf("approver = %q", got[0].Principal.ID)
	}
}

// TestAnApprovalIsOnTheTimeline: an operator asking why this deployment ran
// has to find the answer in the same log as everything else that happened.
func TestAnApprovalIsOnTheTimeline(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	if _, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalGranted, Actor: approver("ana"),
	}); err != nil {
		t.Fatal(err)
	}

	types := deploymentEventTypes(t, h, d.ID)
	if !contains(types, "deployment.approved") {
		t.Errorf("timeline = %v, want an approval on it", types)
	}
	if !contains(types, "deployment.queued") {
		t.Errorf("timeline = %v, want the deployment queued on it", types)
	}
}

// TestARejectionIsOnTheTimeline: a deployment that was refused has to say so,
// not merely appear cancelled.
func TestARejectionIsOnTheTimeline(t *testing.T) {
	h := newHarness(t)
	p, d := h.gated(t, 1)

	if _, err := h.approve(t, deploy.ApproveCommand{
		DeploymentID: d.ID, Revision: p.Hash,
		Decision: policy.ApprovalDenied, Reason: "not this week",
		Actor: approver("ana"),
	}); err != nil {
		t.Fatal(err)
	}

	types := deploymentEventTypes(t, h, d.ID)
	if !contains(types, "deployment.approval.rejected") {
		t.Errorf("timeline = %v, want the rejection on it", types)
	}
}

// deploymentEventTypes reads the deployment's timeline in order.
func deploymentEventTypes(t *testing.T, h *harness, id identity.DeploymentID) []string {
	t.Helper()
	es, err := h.store.Events().ForAggregate(
		context.Background(), event.AggregateDeployment, string(id), port.Page{Limit: 50},
	)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, string(e.Type))
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// record stores an answer about the plan without going through the service, so
// that a test can set up a plan that was answered before it was applied.
func (h *harness) record(
	t *testing.T, p plan.DeploymentPlan, d policy.ApprovalDecision, reason string,
) {
	t.Helper()
	id, err := identity.NewApprovalID(identity.NewSequenceGenerator())
	if err != nil {
		t.Fatal(err)
	}
	a := policy.Approval{
		ID:        identity.ApprovalID(string(id) + "_" + string(p.ID)),
		Subject:   policy.SubjectRef{Kind: "plan", ID: string(p.ID), Revision: p.Hash.String()},
		Principal: approver("ana"),
		Decision:  d,
		Reason:    reason,
		CreatedAt: h.clock.Now(),
	}
	if err := h.store.Approvals().Create(context.Background(), a); err != nil {
		t.Fatalf("recording an approval: %v", err)
	}
}

// TestApplyingAnAlreadyApprovedPlanQueuesItStraightAway is §4.9's "required
// approvals exist". Apply asked only whether the plan carried requirements,
// never whether anyone had met them, so a plan approved before it was applied
// was sent back to a gate it had already cleared.
func TestApplyingAnAlreadyApprovedPlanQueuesItStraightAway(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = needsApproval(1)
	p := h.plan(t)
	h.record(t, p, policy.ApprovalGranted, "")

	d := h.apply(t, p)
	if d.Status != deployment.StatusQueued {
		t.Fatalf("status = %q, want queued — the plan was already approved", d.Status)
	}
}

// TestApplyingAPlanWithOnlySomeApprovalsStillGates keeps the previous case from
// passing by ignoring the count.
func TestApplyingAPlanWithOnlySomeApprovalsStillGates(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = needsApproval(2)
	p := h.plan(t)
	h.record(t, p, policy.ApprovalGranted, "")

	d := h.apply(t, p)
	if d.Status != deployment.StatusAwaitingApproval {
		t.Fatalf("status = %q, want the deployment still at the gate", d.Status)
	}
}

// TestApplyingAPlanAnApproverRefusedIsRefused: admitting a deployment that
// would be cancelled on its first approval would put a refused change into the
// queue, however briefly, and record it as having been admitted.
func TestApplyingAPlanAnApproverRefusedIsRefused(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = needsApproval(1)
	p := h.plan(t)
	h.record(t, p, policy.ApprovalDenied, "the migration has not been rehearsed")

	_, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID:  p.ID,
		Trigger: deployment.Trigger{Type: deployment.TriggerManual, Detail: "ship it"},
		Actor:   actor(),
	})
	if !errors.Is(err, policy.ErrApprovalDenied) {
		t.Fatalf("err = %v, want ErrApprovalDenied", err)
	}
	if _, err := h.store.Deployments().FindActive(context.Background(), h.env.ID); !errors.Is(err, port.ErrNotFound) {
		t.Errorf("a refused plan was admitted anyway: %v", err)
	}
}

// TestAnApprovalOfAnEarlierPlanDoesNotAdmitTheNewOne: re-planning has to
// invalidate approvals, and it does so by construction — the approval names
// the hash it was given.
func TestAnApprovalOfAnEarlierPlanDoesNotAdmitTheNewOne(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = needsApproval(1)
	first := h.plan(t)
	h.record(t, first, policy.ApprovalGranted, "")

	second := h.plan(t)
	if second.Hash == first.Hash {
		t.Fatal("the two plans hash the same; the case proves nothing")
	}
	d := h.apply(t, second)
	if d.Status != deployment.StatusAwaitingApproval {
		t.Fatalf("status = %q, want a re-planned change to need approving again", d.Status)
	}
}
