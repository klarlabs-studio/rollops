package mcpapi

import (
	"context"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/page"
)

// Pagination is the cursor and size a listing tool takes. It is embedded rather
// than repeated so that every list reads the same way to a model.
type Pagination struct {
	PageToken string `json:"page_token,omitempty" jsonschema:"opaque cursor from a previous call; omit for the first page"`
	PageSize  int    `json:"page_size,omitempty" jsonschema:"most rows wanted; clamped to the server's maximum"`
}

func (p Pagination) request() page.Request {
	return page.Request{Cursor: p.PageToken, Size: p.PageSize}
}

// InspectEnvironmentIn names the environment to read.
type InspectEnvironmentIn struct {
	EnvironmentID string `json:"environment_id" jsonschema:"the environment to inspect"`
}

// InspectEnvironmentOut is the environment and where it has got to.
//
// The recent deployments are here because an agent asking about an environment
// is almost always asking what is running in it, and a tool that answered only
// with configuration would be followed by a second call every time.
type InspectEnvironmentOut struct {
	Environment       EnvironmentOut  `json:"environment"`
	RecentDeployments []DeploymentOut `json:"recent_deployments,omitempty"`
}

// InspectEnvironment implements rollops.inspect_environment.
func (t *tools) InspectEnvironment(ctx context.Context, in InspectEnvironmentIn) (InspectEnvironmentOut, error) {
	if err := t.reader(ctx); err != nil {
		return InspectEnvironmentOut{}, err
	}
	env, err := t.svc.GetEnvironment(ctx, apiv2.GetEnvironmentRequest{ID: in.EnvironmentID})
	if err != nil {
		return InspectEnvironmentOut{}, fail(err)
	}
	recent, err := t.svc.ListDeployments(ctx, apiv2.ListDeploymentsRequest{EnvironmentID: env.ID})
	if err != nil {
		return InspectEnvironmentOut{}, fail(err)
	}
	return InspectEnvironmentOut{
		Environment:       environment(env),
		RecentDeployments: mapped(recent.Deployments, deploymentOf),
	}, nil
}

// ListReleasesIn names the project whose releases to list.
type ListReleasesIn struct {
	ProjectID string `json:"project_id" jsonschema:"the project whose releases to list"`
	Pagination
}

// ListReleasesOut is a page of releases.
type ListReleasesOut struct {
	Releases      []ReleaseOut `json:"releases"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

// ListReleases implements rollops.list_releases.
func (t *tools) ListReleases(ctx context.Context, in ListReleasesIn) (ListReleasesOut, error) {
	if err := t.reader(ctx); err != nil {
		return ListReleasesOut{}, err
	}
	got, err := t.svc.ListReleases(ctx, apiv2.ListReleasesRequest{
		ProjectID: in.ProjectID,
		Page:      in.request(),
	})
	if err != nil {
		return ListReleasesOut{}, fail(err)
	}
	return ListReleasesOut{Releases: mapped(got.Releases, release), NextPageToken: got.Next}, nil
}

// CreateReleaseIn fixes a set of registered artifacts under a version.
//
// There is no actor field, for the reason grpcapi gives: who is calling is
// established by the transport, and a field for it would be a field an agent
// could fill in with somebody else's name.
type CreateReleaseIn struct {
	ProjectID string `json:"project_id" jsonschema:"the project the release belongs to"`
	Version   string `json:"version" jsonschema:"how the release is asked for; unique within the project"`

	Artifacts []ReleaseArtifactIn `json:"artifacts" jsonschema:"the already-registered artifacts this release is made of"`

	SourceProvider   string `json:"source_provider,omitempty" jsonschema:"where the code came from, e.g. github"`
	SourceRepository string `json:"source_repository,omitempty"`
	SourceRevision   string `json:"source_revision,omitempty" jsonschema:"the commit the release was built from"`
	SourceRef        string `json:"source_ref,omitempty" jsonschema:"the branch or tag the commit was on"`

	Labels map[string]string `json:"labels,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"repeat on a retry to get the release the first call created rather than a conflict"`
}

// ReleaseArtifactIn binds one registered artifact to the role it plays.
type ReleaseArtifactIn struct {
	Role       string `json:"role" jsonschema:"what this artifact is to the release, e.g. image"`
	ArtifactID string `json:"artifact_id"`
}

// CreateRelease implements rollops.create_release.
func (t *tools) CreateRelease(ctx context.Context, in CreateReleaseIn) (ReleaseOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return ReleaseOut{}, err
	}
	r, err := t.svc.CreateRelease(ctx, apiv2.CreateReleaseRequest{
		ProjectID: in.ProjectID,
		Version:   in.Version,
		Artifacts: mapped(in.Artifacts, func(a ReleaseArtifactIn) apiv2.ReleaseArtifact {
			return apiv2.ReleaseArtifact{Role: a.Role, ArtifactID: a.ArtifactID}
		}),
		Source: apiv2.Source{
			Provider:   in.SourceProvider,
			Repository: in.SourceRepository,
			Revision:   in.SourceRevision,
			Ref:        in.SourceRef,
		},
		Labels:         in.Labels,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return ReleaseOut{}, fail(err)
	}
	return release(r), nil
}

// PlanDeploymentIn asks what deploying a release to an environment would do.
type PlanDeploymentIn struct {
	EnvironmentID string `json:"environment_id"`
	ReleaseID     string `json:"release_id"`

	// Strategy is required. Guessing it would have an approver review a
	// rollout nobody asked for.
	Strategy string `json:"strategy" jsonschema:"how to roll the release out, e.g. rolling, canary, blue_green"`

	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"repeat on a retry to get the plan the first call built rather than a second one against a world that has since moved"`
}

// PlanDeployment implements rollops.plan_deployment.
func (t *tools) PlanDeployment(ctx context.Context, in PlanDeploymentIn) (PlanOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return PlanOut{}, err
	}
	p, err := t.svc.CreatePlan(ctx, apiv2.CreatePlanRequest{
		EnvironmentID:  in.EnvironmentID,
		ReleaseID:      in.ReleaseID,
		Strategy:       in.Strategy,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return PlanOut{}, fail(err)
	}
	return plan(p), nil
}

// ExplainPlanIn names the plan to re-read.
type ExplainPlanIn struct {
	PlanID string `json:"plan_id"`
}

// ExplainPlan implements rollops.explain_plan.
//
// It is a read of the same plan planning returned, not a second computation:
// an explanation derived from a fresh evaluation could describe something other
// than the plan an approver is looking at.
func (t *tools) ExplainPlan(ctx context.Context, in ExplainPlanIn) (PlanOut, error) {
	if err := t.reader(ctx); err != nil {
		return PlanOut{}, err
	}
	p, err := t.svc.GetPlan(ctx, apiv2.GetPlanRequest{ID: in.PlanID})
	if err != nil {
		return PlanOut{}, fail(err)
	}
	return plan(p), nil
}

// ApplyPlanIn admits a deployment for a plan.
type ApplyPlanIn struct {
	PlanID string `json:"plan_id"`

	// Detail is free text for whoever reads the timeline later. The trigger
	// type is not the caller's to choose.
	Detail string `json:"detail,omitempty" jsonschema:"why, for the timeline: a ticket, a change request, the request being fulfilled"`

	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"repeat on a retry to get the deployment the first call admitted rather than a second one"`
}

// ApplyPlan implements rollops.apply_plan.
func (t *tools) ApplyPlan(ctx context.Context, in ApplyPlanIn) (DeploymentOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return DeploymentOut{}, err
	}
	d, err := t.svc.ApplyPlan(ctx, apiv2.ApplyPlanRequest{
		PlanID:         in.PlanID,
		Detail:         in.Detail,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return DeploymentOut{}, fail(err)
	}
	return deploymentOf(d), nil
}

// GetDeploymentIn names the deployment to read.
type GetDeploymentIn struct {
	DeploymentID string `json:"deployment_id"`
}

// GetDeployment implements rollops.get_deployment.
func (t *tools) GetDeployment(ctx context.Context, in GetDeploymentIn) (DeploymentOut, error) {
	if err := t.reader(ctx); err != nil {
		return DeploymentOut{}, err
	}
	d, err := t.svc.GetDeployment(ctx, apiv2.GetDeploymentRequest{ID: in.DeploymentID})
	if err != nil {
		return DeploymentOut{}, fail(err)
	}
	return deploymentOf(d), nil
}

// VerifyDeploymentIn names the deployment to check.
//
// There is no reason field: verifying asks a question rather than overriding
// an answer, so there is nothing to justify.
type VerifyDeploymentIn struct {
	DeploymentID   string `json:"deployment_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"repeat on a retry to get the run the first call recorded rather than checking twice"`
}

// VerifyDeploymentOut is the verdict and where it left the deployment.
type VerifyDeploymentOut struct {
	Deployment DeploymentOut      `json:"deployment"`
	Run        VerificationRunOut `json:"run"`
}

// VerifyDeployment implements rollops.verify_deployment.
func (t *tools) VerifyDeployment(ctx context.Context, in VerifyDeploymentIn) (VerifyDeploymentOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return VerifyDeploymentOut{}, err
	}
	got, err := t.svc.VerifyDeployment(ctx, apiv2.VerifyDeploymentRequest{
		DeploymentID:   in.DeploymentID,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return VerifyDeploymentOut{}, fail(err)
	}
	return VerifyDeploymentOut{Deployment: deploymentOf(got.Deployment), Run: run(got.Run)}, nil
}

// PromoteDeploymentIn widens a deployment past its checks.
type PromoteDeploymentIn struct {
	DeploymentID string `json:"deployment_id"`

	// Reason is required for a paused deployment, because promoting one
	// disregards whatever paused it and somebody has to be findable for the
	// decision.
	Reason string `json:"reason,omitempty" jsonschema:"required when the deployment is paused: why its checks are being overridden"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// PromoteDeployment implements rollops.promote_deployment.
func (t *tools) PromoteDeployment(ctx context.Context, in PromoteDeploymentIn) (DeploymentOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return DeploymentOut{}, err
	}
	d, err := t.svc.PromoteDeployment(ctx, apiv2.PromoteDeploymentRequest{
		DeploymentID:   in.DeploymentID,
		Reason:         in.Reason,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return DeploymentOut{}, fail(err)
	}
	return deploymentOf(d), nil
}

// RollbackDeploymentIn returns a deployment to the release it replaced.
type RollbackDeploymentIn struct {
	DeploymentID string `json:"deployment_id"`

	// Reason is optional here, unlike promoting past a pause: a rollback heeds
	// a signal rather than disregarding one.
	Reason string `json:"reason,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// RollbackDeployment implements rollops.rollback_deployment.
func (t *tools) RollbackDeployment(ctx context.Context, in RollbackDeploymentIn) (DeploymentOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return DeploymentOut{}, err
	}
	d, err := t.svc.RollbackDeployment(ctx, apiv2.RollbackDeploymentRequest{
		DeploymentID:   in.DeploymentID,
		Reason:         in.Reason,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return DeploymentOut{}, fail(err)
	}
	return deploymentOf(d), nil
}

// CancelDeploymentIn stops a deployment that has not finished.
type CancelDeploymentIn struct {
	DeploymentID string `json:"deployment_id"`

	// Reason is required. A cancelled deployment with nothing saying who
	// stopped it is indistinguishable from one that died.
	Reason string `json:"reason" jsonschema:"why it is being stopped; required"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// CancelDeployment implements rollops.cancel_deployment.
func (t *tools) CancelDeployment(ctx context.Context, in CancelDeploymentIn) (DeploymentOut, error) {
	who, err := t.caller(ctx)
	if err != nil {
		return DeploymentOut{}, err
	}
	d, err := t.svc.CancelDeployment(ctx, apiv2.CancelDeploymentRequest{
		DeploymentID:   in.DeploymentID,
		Reason:         in.Reason,
		Actor:          who,
		IdempotencyKey: in.IdempotencyKey,
	})
	if err != nil {
		return DeploymentOut{}, fail(err)
	}
	return deploymentOf(d), nil
}

// GetTimelineIn names the deployment whose log to read.
type GetTimelineIn struct {
	DeploymentID string `json:"deployment_id"`
	Pagination
}

// GetTimelineOut is a page of the deployment's events, oldest first.
type GetTimelineOut struct {
	Events        []EventOut `json:"events"`
	NextPageToken string     `json:"next_page_token,omitempty"`
}

// GetTimeline implements rollops.get_timeline.
func (t *tools) GetTimeline(ctx context.Context, in GetTimelineIn) (GetTimelineOut, error) {
	if err := t.reader(ctx); err != nil {
		return GetTimelineOut{}, err
	}
	got, err := t.svc.DeploymentEvents(ctx, apiv2.DeploymentEventsRequest{
		DeploymentID: in.DeploymentID,
		Page:         in.request(),
	})
	if err != nil {
		return GetTimelineOut{}, fail(err)
	}
	return GetTimelineOut{Events: mapped(got.Events, event), NextPageToken: got.Next}, nil
}
