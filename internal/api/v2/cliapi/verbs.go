package cliapi

import (
	"context"
	"io"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
)

// The verbs of spec 26.1. Each is a thin projection of one service method: the
// decision about what may happen is made there, and repeating any part of it
// here would give CLI users a rule the other transports do not have (§23.1).

// plan builds one, or reads back one that already exists.
//
// The read is here rather than under a noun of its own because a plan is not
// one: spec 26.1 names plan only as a verb. It is needed because `deploy --plan`
// takes an id, and an operator handed one has to be able to see what they would
// be applying before they apply it. The word is the one the MCP surface uses
// for the same question (§25.1 explain_plan), so the two transports describe it
// alike.
func (a *App) plan(ctx context.Context, args []string) error {
	fs := flags("plan")
	out := outputFlag(fs)
	explain := fs.String("explain", "", "read a plan that already exists instead of building one")
	releaseID := fs.String("release", "", "the release to plan for")
	// Strategy is required rather than defaulted. Guessing it would have an
	// approver review a rollout nobody asked for.
	strategy := fs.String("strategy", "", "how to roll it out, e.g. rolling, canary, blue_green")
	key := fs.String("idempotency-key", "", "repeat on a retry to get the plan the first call built")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if *explain != "" {
		if err := none(fs.Name(), positional); err != nil {
			return err
		}
		if err := a.reader(ctx); err != nil {
			return err
		}
		p, err := a.svc.GetPlan(ctx, apiv2.GetPlanRequest{ID: *explain})
		if err != nil {
			return err
		}
		return a.render(f, plan(p), func(w io.Writer) { describePlan(w, plan(p)) })
	}
	environmentID, err := one(fs.Name(), positional, "an environment or --explain")
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	p, err := a.svc.CreatePlan(ctx, apiv2.CreatePlanRequest{
		EnvironmentID:  environmentID,
		ReleaseID:      *releaseID,
		Strategy:       *strategy,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	return a.render(f, plan(p), func(w io.Writer) { describePlan(w, plan(p)) })
}

// describePlan leads with the verdict. Spec 26.2 asks for decision-oriented
// output, and what an operator looking at a plan has to decide is whether to
// apply it — the operations are the evidence, not the answer.
func describePlan(w io.Writer, p PlanView) {
	verdict := "allowed"
	if !p.Policy.Allowed {
		verdict = "blocked"
		if p.ApprovalRequired {
			verdict = "needs approval"
		}
	}
	printf(w, "plan %s\t%s\t%s\n", p.PlanID, p.Strategy, verdict)
	printf(w, "  risk %s (%.3f)\t%d operations\n", p.Policy.Risk.Level, p.Policy.Risk.Score, len(p.Operations))
	for _, r := range p.Policy.Requirements {
		printf(w, "  requires %s %s\n", r.Type, r.Detail)
	}
	for _, r := range p.Policy.Reasons {
		printf(w, "  %s: %s\n", r.Code, r.Message)
	}
	for _, o := range p.Operations {
		printf(w, "  %s\t%s\t%s\n", o.Kind, o.Target, o.Summary)
	}
	if len(p.NextActions) > 0 {
		printf(w, "  next: %s\n", list(p.NextActions))
	}
}

// DeployView is a deployment and, when the command planned it, the plan it
// applied. The plan is here because an operator who ran one command should not
// have to run a second to find out what it did.
type DeployView struct {
	Deployment DeploymentView `json:"deployment"`
	Plan       *PlanView      `json:"plan,omitempty"`
}

// deploy applies a plan. Given an environment and a release it plans first,
// which is two calls rather than one — but the plan it applies is reported, so
// what was decided is still visible to whoever reads the output. Given
// --plan it applies the plan that already exists, which is the path to take
// when a human has read one and wants exactly it.
func (a *App) deploy(ctx context.Context, args []string) error {
	fs := flags("deploy")
	out := outputFlag(fs)
	planID := fs.String("plan", "", "a plan to apply; with it, no environment is needed")
	releaseID := fs.String("release", "", "the release to deploy")
	strategy := fs.String("strategy", "", "how to roll it out, e.g. rolling, canary, blue_green")
	detail := fs.String("detail", "", "why, for the timeline: a ticket, a change request")
	key := fs.String("idempotency-key", "", "repeat on a retry to get the deployment the first call admitted")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}

	view := DeployView{}
	applying := *planID
	if applying == "" {
		environmentID, err := one(fs.Name(), positional, "an environment or --plan")
		if err != nil {
			return err
		}
		p, err := a.svc.CreatePlan(ctx, apiv2.CreatePlanRequest{
			EnvironmentID:  environmentID,
			ReleaseID:      *releaseID,
			Strategy:       *strategy,
			Actor:          who,
			IdempotencyKey: *key,
		})
		if err != nil {
			return err
		}
		planned := plan(p)
		view.Plan = &planned
		applying = p.ID
	} else if err := none(fs.Name(), positional); err != nil {
		return err
	}

	d, err := a.svc.ApplyPlan(ctx, apiv2.ApplyPlanRequest{
		PlanID:         applying,
		Detail:         *detail,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	view.Deployment = deploymentOf(d)
	return a.render(f, view, func(w io.Writer) {
		if view.Plan != nil {
			describePlan(w, *view.Plan)
		}
		describeDeployment(w, view.Deployment)
	})
}

func (a *App) status(ctx context.Context, args []string) error {
	fs := flags("status")
	out := outputFlag(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "a deployment")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	d, err := a.svc.GetDeployment(ctx, apiv2.GetDeploymentRequest{ID: id})
	if err != nil {
		return err
	}
	return a.render(f, deploymentOf(d), func(w io.Writer) { describeDeployment(w, deploymentOf(d)) })
}

// describeDeployment says where it got to and what may be done about it. The
// next actions are the service's answer, not a list kept here, so a status this
// CLI offers is one the next command will accept.
func describeDeployment(w io.Writer, d DeploymentView) {
	printf(w, "deployment %s\t%s\t%s\n", d.DeploymentID, d.Status, d.Strategy)
	printf(w, "  environment %s\trelease %s\n", d.EnvironmentID, d.ReleaseID)
	if len(d.NextActions) > 0 {
		printf(w, "  next: %s\n", list(d.NextActions))
	}
}

// VerifyView is the verdict and where it left the deployment. Both are here
// because the verdict decides the status, and a caller given only one of them
// would go and fetch the other.
type VerifyView struct {
	Deployment DeploymentView      `json:"deployment"`
	Run        VerificationRunView `json:"run"`
}

func (a *App) verify(ctx context.Context, args []string) error {
	fs := flags("verify")
	out := outputFlag(fs)
	// There is no --reason. Verifying asks a question rather than overriding an
	// answer, so there is nothing to justify.
	key := fs.String("idempotency-key", "", "repeat on a retry to get the run the first call recorded")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "a deployment")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	got, err := a.svc.VerifyDeployment(ctx, apiv2.VerifyDeploymentRequest{
		DeploymentID:   id,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	view := VerifyView{Deployment: deploymentOf(got.Deployment), Run: run(got.Run)}
	return a.render(f, view, func(w io.Writer) {
		printf(w, "verification %s\t%s\n", view.Run.RunID, view.Run.Verdict)
		for _, c := range view.Run.Checks {
			printf(w, "  %s\t%s\t%s\n", c.Name, c.Verdict, c.Reason)
		}
		describeDeployment(w, view.Deployment)
	})
}

func (a *App) promote(ctx context.Context, args []string) error {
	fs := flags("promote")
	out := outputFlag(fs)
	// Required for a paused deployment, because promoting one disregards
	// whatever paused it and somebody has to be findable for the decision. The
	// service decides when it is required; this only carries it.
	reason := fs.String("reason", "", "why; required when the deployment is paused")
	key := fs.String("idempotency-key", "", "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "a deployment")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	d, err := a.svc.PromoteDeployment(ctx, apiv2.PromoteDeploymentRequest{
		DeploymentID:   id,
		Reason:         *reason,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	return a.render(f, deploymentOf(d), func(w io.Writer) { describeDeployment(w, deploymentOf(d)) })
}

// rollback takes a deployment. Spec 26.1 also draws an environment, which would
// mean putting back whatever is running there now — but choosing which
// deployment that is would be a decision made in a transport, and api/v2 has no
// method that answers it. Left out rather than guessed at here.
func (a *App) rollback(ctx context.Context, args []string) error {
	fs := flags("rollback")
	out := outputFlag(fs)
	// Optional, unlike promoting past a pause: a rollback heeds a signal rather
	// than disregarding one.
	reason := fs.String("reason", "", "why")
	key := fs.String("idempotency-key", "", "")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "a deployment")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	d, err := a.svc.RollbackDeployment(ctx, apiv2.RollbackDeploymentRequest{
		DeploymentID:   id,
		Reason:         *reason,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	return a.render(f, deploymentOf(d), func(w io.Writer) { describeDeployment(w, deploymentOf(d)) })
}

// HistoryView is what an environment has deployed, or what one deployment did.
// Exactly one of the two is populated, decided by whether --deployment was
// given — a document with both would say an environment's history contained a
// timeline, which is not the relationship.
type HistoryView struct {
	Deployments   []DeploymentView `json:"deployments,omitempty"`
	Events        []EventView      `json:"events,omitempty"`
	NextPageToken string           `json:"next_page_token,omitempty"`
}

// history answers two questions with one verb: what an environment has
// deployed, and what one of those deployments did. They are the same question
// at two depths, and spec 26.1 names no second verb for the inner one.
func (a *App) history(ctx context.Context, args []string) error {
	fs := flags("history")
	out := outputFlag(fs)
	pg := pageFlags(fs)
	deploymentID := fs.String("deployment", "", "read one deployment's timeline instead of an environment's deployments")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	if *deploymentID != "" {
		if err := none(fs.Name(), positional); err != nil {
			return err
		}
		got, err := a.svc.DeploymentEvents(ctx, apiv2.DeploymentEventsRequest{
			DeploymentID: *deploymentID,
			Page:         *pg,
		})
		if err != nil {
			return err
		}
		view := HistoryView{Events: mapped(got.Events, event), NextPageToken: got.Next}
		return a.render(f, view, func(w io.Writer) {
			for _, e := range view.Events {
				printf(w, "%d\t%s\t%s\t%s\n", e.Sequence, e.Time.Format(rfc3339), e.Type, e.Principal.ID)
			}
			nextPage(w, view.NextPageToken)
		})
	}
	environmentID, err := one(fs.Name(), positional, "an environment or --deployment")
	if err != nil {
		return err
	}
	got, err := a.svc.ListDeployments(ctx, apiv2.ListDeploymentsRequest{
		EnvironmentID: environmentID,
		Page:          *pg,
	})
	if err != nil {
		return err
	}
	view := HistoryView{Deployments: mapped(got.Deployments, deploymentOf), NextPageToken: got.Next}
	return a.render(f, view, func(w io.Writer) {
		for _, d := range view.Deployments {
			printf(w, "%s\t%s\t%s\t%s\n", d.DeploymentID, d.Status, d.ReleaseID, d.CreatedAt.Format(rfc3339))
		}
		nextPage(w, view.NextPageToken)
	})
}
