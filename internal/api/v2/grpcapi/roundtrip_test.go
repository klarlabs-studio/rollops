package grpcapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/types/known/durationpb"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/grpcapi"
	rollopsv2 "go.klarlabs.de/rollops/internal/api/v2/grpcapi/rollopsv2"
	"go.klarlabs.de/rollops/internal/app/deploy"
	appenv "go.klarlabs.de/rollops/internal/app/environment"
	appproject "go.klarlabs.de/rollops/internal/app/project"
	apprelease "go.klarlabs.de/rollops/internal/app/release"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/verification"
	"go.klarlabs.de/rollops/internal/store/memory"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return at }

// recordingDeployer keeps the last command it was handed and refuses to do
// anything with it.
//
// The write side is exercised in full by the api/v2 tests; what is in question
// here is whether a call that arrived over gRPC reached the right method
// carrying the right values.
type recordingDeployer struct {
	plan     deploy.PlanCommand
	apply    deploy.ApplyCommand
	cancel   deploy.CancelCommand
	promote  deploy.PromoteCommand
	rollback deploy.RollbackCommand
	approve  deploy.ApproveCommand
	verify   deploy.VerifyCommand

	verdict  *deploy.Verification
	admitted *deployment.Deployment
}

var errNotWired = errors.New("the deployer was not meant to carry this out")

func (d *recordingDeployer) Plan(_ context.Context, c deploy.PlanCommand) (plan.DeploymentPlan, error) {
	d.plan = c
	return plan.DeploymentPlan{}, errNotWired
}

func (d *recordingDeployer) Apply(_ context.Context, c deploy.ApplyCommand) (deployment.Deployment, error) {
	d.apply = c
	if d.admitted == nil {
		return deployment.Deployment{}, errNotWired
	}
	return *d.admitted, nil
}

func (d *recordingDeployer) Approve(_ context.Context, c deploy.ApproveCommand) (deployment.Deployment, error) {
	d.approve = c
	return deployment.Deployment{}, errNotWired
}

func (d *recordingDeployer) Cancel(_ context.Context, c deploy.CancelCommand) (deployment.Deployment, error) {
	d.cancel = c
	return deployment.Deployment{}, errNotWired
}

func (d *recordingDeployer) Verify(
	_ context.Context, c deploy.VerifyCommand,
) (deployment.Deployment, deploy.Verification, error) {
	d.verify = c
	if d.verdict == nil {
		return deployment.Deployment{}, deploy.Verification{}, errNotWired
	}
	return deployment.Deployment{}, *d.verdict, nil
}

func (d *recordingDeployer) Promote(_ context.Context, c deploy.PromoteCommand) (deployment.Deployment, error) {
	d.promote = c
	return deployment.Deployment{}, errNotWired
}

func (d *recordingDeployer) Rollback(_ context.Context, c deploy.RollbackCommand) (deployment.Deployment, error) {
	d.rollback = c
	return deployment.Deployment{}, errNotWired
}

// dial serves one gRPC server over an in-process listener and returns a client
// for it. A real server and a real connection, because the marshalling is part
// of what is under test: a field the generated code cannot carry would survive
// a direct method call and fail on the wire.
func dial(t *testing.T, svc *apiv2.Service, who grpcapi.Identifier) rollopsv2.RollOpsClient {
	t.Helper()

	server, err := grpcapi.New(grpcapi.Config{Service: svc, Identity: who})
	if err != nil {
		t.Fatalf("grpcapi.New: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	server.Register(gs)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
	})
	return rollopsv2.NewRollOpsClient(conn)
}

type estate struct {
	client   rollopsv2.RollOpsClient
	store    *memory.Store
	deployer *recordingDeployer
}

// served wires the real service over an in-memory store. Everything behind the
// RPCs is the real thing: a fake service would let the transport agree with a
// projection nobody serves.
func served(t *testing.T) *estate {
	t.Helper()
	store := memory.New()
	clock := fixedClock{}

	registrar, err := apprelease.New(apprelease.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Artifacts:  store.Artifacts(),
		Releases:   store.Releases(),
		Events:     store.Events(),
		Clock:      clock,
		IDs:        identity.NewGenerator(),
	})
	if err != nil {
		t.Fatalf("apprelease.New: %v", err)
	}
	founder, err := appproject.New(appproject.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Events:     store.Events(),
		Clock:      clock,
		IDs:        identity.NewGenerator(),
	})
	if err != nil {
		t.Fatalf("appproject.New: %v", err)
	}
	binder, err := appenv.New(appenv.Config{
		Transactor:   store,
		Projects:     store.Projects(),
		Environments: store.Environments(),
		Events:       store.Events(),
		Clock:        clock,
		IDs:          identity.NewGenerator(),
	})
	if err != nil {
		t.Fatalf("appenv.New: %v", err)
	}

	deployer := &recordingDeployer{}
	svc, err := apiv2.New(apiv2.Config{
		Projects:         store.Projects(),
		Environments:     store.Environments(),
		Releases:         store.Releases(),
		Artifacts:        store.Artifacts(),
		Deployments:      store.Deployments(),
		Plans:            store.Plans(),
		VerificationRuns: store.VerificationRuns(),
		Events:           store.Events(),
		Deployer:         deployer,
		Registrar:        registrar,
		Founder:          founder,
		Binder:           binder,
		Keys:             store.Idempotency(),
		Clock:            clock,
	})
	if err != nil {
		t.Fatalf("apiv2.New: %v", err)
	}
	return &estate{client: dial(t, svc, everyone()), store: store, deployer: deployer}
}

func (e *estate) project(t *testing.T) string {
	t.Helper()
	p, err := e.client.CreateProject(context.Background(), &rollopsv2.CreateProjectRequest{
		Name:        "checkout",
		Description: "the checkout service",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.GetId() == "" {
		t.Fatal("the created project came back without an id")
	}
	return p.GetId()
}

// The secret an environment is declared with. It is a literal in the test so
// that a message carrying it through can be found by searching for it.
const secretName = "prod/kubeconfig"

func (e *estate) environment(t *testing.T, projectID string) string {
	t.Helper()
	env, err := e.client.CreateEnvironment(context.Background(), &rollopsv2.CreateEnvironmentRequest{
		ProjectId: projectID,
		Name:      "production",
		Kind:      rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_PRODUCTION,
		Targets: []*rollopsv2.TargetSpec{{
			Name:   "api",
			Driver: "kubernetes",
			Config: map[string]*rollopsv2.Value{
				"namespace":  {Value: &rollopsv2.Value_Literal{Literal: "payments"}},
				"kubeconfig": {Value: &rollopsv2.Value_Secret{Secret: secretName}},
			},
		}},
		Policies: []*rollopsv2.PolicyBinding{{
			Name: "change-window",
			Ref:  "policy://change-window",
			Mode: rollopsv2.PolicyMode_POLICY_MODE_ENFORCE,
		}},
		Variables: map[string]*rollopsv2.Value{
			"DATABASE_URL": {Value: &rollopsv2.Value_Secret{Secret: "prod/db"}},
		},
		Lifecycle: &rollopsv2.Lifecycle{Ttl: durationpb.New(72 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	return env.GetId()
}

// release registers an artifact and a release for it, returning the release id.
func (e *estate) release(t *testing.T, projectID string) identity.ReleaseID {
	t.Helper()
	ctx := context.Background()
	sha := "sha256:" + strings.Repeat("c", 64)

	a, err := e.client.RegisterArtifact(ctx, &rollopsv2.RegisterArtifactRequest{
		ProjectId: projectID,
		Kind:      rollopsv2.ArtifactKind_ARTIFACT_KIND_OCI_IMAGE,
		Digest:    sha,
		Locator:   "registry.example.com/checkout@" + sha,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
	})
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	r, err := e.client.CreateRelease(ctx, &rollopsv2.CreateReleaseRequest{
		ProjectId: projectID,
		Version:   "2.0.0",
		Artifacts: []*rollopsv2.ReleaseArtifact{{Role: "app", ArtifactId: a.GetId()}},
		Source: &rollopsv2.Source{
			Provider:   "git",
			Repository: "example/checkout",
			Revision:   strings.Repeat("d", 40),
			Ref:        "refs/heads/main",
		},
	})
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}
	return identity.ReleaseID(r.GetId())
}

func TestAResourceIsReadableAtTheIDItWasCreatedWith(t *testing.T) {
	t.Parallel()
	e := served(t)
	projectID := e.project(t)

	got, err := e.client.GetProject(context.Background(), &rollopsv2.GetProjectRequest{Id: projectID})
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}

	if got.GetId() != projectID {
		t.Errorf("id = %q, want %q", got.GetId(), projectID)
	}
	if got.GetName() != "checkout" {
		t.Errorf("name = %q, want checkout", got.GetName())
	}
	if !got.GetCreatedAt().AsTime().Equal(at) {
		t.Errorf("created at = %s, want %s", got.GetCreatedAt().AsTime(), at)
	}
	// Without it a caller cannot tell that the project moved under them.
	if got.GetRevision() == 0 {
		t.Error("revision is zero")
	}
}

// The write shape and the read shape of an environment differ on purpose. This
// is the one that would hurt if it regressed: a read that carried a secret name
// — let alone a resolved value — would put the estate's credential layout on
// the wire (INV-012).
func TestAnEnvironmentIsReadBackAsKeysAndNeverAsValues(t *testing.T) {
	t.Parallel()
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)

	got, err := e.client.GetEnvironment(context.Background(), &rollopsv2.GetEnvironmentRequest{Id: envID})
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}

	// The whole message, because a secret that leaked into a field nobody
	// thought to assert on is exactly the case a field-by-field check misses.
	if rendered := prototext.Format(got); strings.Contains(rendered, secretName) {
		t.Errorf("the environment read back naming a secret: %s", rendered)
	}
	if want := []string{"DATABASE_URL"}; !slices.Equal(got.GetVariableNames(), want) {
		t.Errorf("variable names = %v, want %v", got.GetVariableNames(), want)
	}
	if len(got.GetTargets()) != 1 {
		t.Fatalf("targets = %v", got.GetTargets())
	}
	if want := []string{"kubeconfig", "namespace"}; !slices.Equal(got.GetTargets()[0].GetConfigKeys(), want) {
		t.Errorf("target config keys = %v, want %v", got.GetTargets()[0].GetConfigKeys(), want)
	}
	// A policy binding names a policy rather than carrying one, so it is a
	// reference on both sides and reads back as it was written.
	policies := got.GetPolicies()
	if len(policies) != 1 || policies[0].GetMode() != rollopsv2.PolicyMode_POLICY_MODE_ENFORCE ||
		policies[0].GetRef() != "policy://change-window" {
		t.Errorf("policies = %v", policies)
	}
	if ttl := got.GetLifecycle().GetTtl().AsDuration(); ttl != 72*time.Hour {
		t.Errorf("ttl = %s, want 72h", ttl)
	}
	// An enum that came back UNSPECIFIED would read as "this environment has no
	// kind" rather than as "this build does not know the kind it has".
	if got.GetKind() != rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_PRODUCTION {
		t.Errorf("kind = %s, want PRODUCTION", got.GetKind())
	}
}

// An artifact is a thing in its own right (INV-002), so it is readable at its
// own RPC as well as under the project that registered it — and so is the
// release that names it.
func TestAnArtifactAndItsReleaseAreReadableAtTheirOwnRPCs(t *testing.T) {
	t.Parallel()
	e := served(t)
	ctx := context.Background()
	projectID := e.project(t)
	releaseID := e.release(t, projectID)

	r, err := e.client.GetRelease(ctx, &rollopsv2.GetReleaseRequest{Id: string(releaseID)})
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if r.GetVersion() != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", r.GetVersion())
	}
	if len(r.GetArtifacts()) != 1 {
		t.Fatalf("artifacts = %v, want the one registered", r.GetArtifacts())
	}
	// The source is what makes a release traceable back to a commit, so an
	// absent one would be a release nobody can account for.
	if r.GetSource().GetRepository() != "example/checkout" {
		t.Errorf("source = %v", r.GetSource())
	}

	a, err := e.client.GetArtifact(ctx, &rollopsv2.GetArtifactRequest{
		Id: r.GetArtifacts()[0].GetArtifactId(),
	})
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if want := "sha256:" + strings.Repeat("c", 64); a.GetDigest() != want {
		t.Errorf("digest = %q, want %q", a.GetDigest(), want)
	}
	if a.GetKind() != rollopsv2.ArtifactKind_ARTIFACT_KIND_OCI_IMAGE {
		t.Errorf("kind = %s, want OCI_IMAGE", a.GetKind())
	}
}

// Applying a plan answers with the deployment that was admitted and nothing
// more: §23.3 has the call return as soon as the deployment is recorded, so
// what comes back is a promise rather than an outcome.
func TestApplyingAPlanAnswersWithTheAdmittedDeployment(t *testing.T) {
	t.Parallel()
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)
	e.deployer.admitted = &l.deployment

	got, err := e.client.ApplyPlan(context.Background(), &rollopsv2.ApplyPlanRequest{
		PlanId: string(l.plan.ID),
		Detail: "released by hand",
	})
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}

	if got.GetId() != string(l.deployment.ID) {
		t.Errorf("deployment = %q, want %q", got.GetId(), l.deployment.ID)
	}
	if e.deployer.apply.PlanID != l.plan.ID {
		t.Errorf("plan = %q, want %q", e.deployer.apply.PlanID, l.plan.ID)
	}
	// The trigger type is not the caller's to choose. A request that could
	// claim "git" would write a cause into the record nothing corroborates.
	if e.deployer.apply.Trigger.Type != deployment.TriggerAPI {
		t.Errorf("trigger = %q, want api", e.deployer.apply.Trigger.Type)
	}
	if e.deployer.apply.Trigger.Detail != "released by hand" {
		t.Errorf("detail = %q", e.deployer.apply.Trigger.Detail)
	}
}

// A retry says it is a retry with a field rather than with metadata, so the
// same sentence reads on every command — including the ones whose requests
// carry nothing else.
func TestAKeyedRetryIsAnsweredWithTheFirstCallsResource(t *testing.T) {
	t.Parallel()
	e := served(t)
	ctx := context.Background()
	req := &rollopsv2.CreateProjectRequest{Name: "checkout", IdempotencyKey: "k1"}

	one, err := e.client.CreateProject(ctx, req)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	two, err := e.client.CreateProject(ctx, req)
	if err != nil {
		t.Fatalf("the retry was refused: %v", err)
	}

	if one.GetId() != two.GetId() {
		t.Errorf("the retry made a second project: %q then %q", one.GetId(), two.GetId())
	}
}

// What the transport owes each command: the id from the request, the prose from
// the request, and the caller from the credential rather than from either.
func TestACommandReachesTheDeployerCarryingWhatTheRequestSaid(t *testing.T) {
	t.Parallel()
	e := served(t)
	ctx := context.Background()
	projectID := e.project(t)
	envID := e.environment(t, projectID)

	deploymentID := minted(t, identity.NewDeploymentID)
	releaseID := minted(t, identity.NewReleaseID)

	// The deployer refuses everything, so these are sent for what they record.
	_, _ = e.client.CancelDeployment(ctx, &rollopsv2.CancelDeploymentRequest{
		DeploymentId: deploymentID, Reason: "the wrong build",
	})
	_, _ = e.client.PromoteDeployment(ctx, &rollopsv2.PromoteDeploymentRequest{
		DeploymentId: deploymentID, Reason: "checks are green",
	})
	_, _ = e.client.RollbackDeployment(ctx, &rollopsv2.RollbackDeploymentRequest{
		DeploymentId: deploymentID,
	})
	_, _ = e.client.VerifyDeployment(ctx, &rollopsv2.VerifyDeploymentRequest{
		DeploymentId: deploymentID,
	})
	_, _ = e.client.CreatePlan(ctx, &rollopsv2.CreatePlanRequest{
		EnvironmentId: envID,
		ReleaseId:     releaseID,
		Strategy:      rollopsv2.Strategy_STRATEGY_ROLLING,
	})
	_, _ = e.client.ApproveDeployment(ctx, &rollopsv2.ApproveDeploymentRequest{
		DeploymentId: deploymentID,
		PlanHash:     "sha256:" + strings.Repeat("f", 64),
		Granted:      true,
		Reason:       "the window is open",
	})

	d := e.deployer
	if d.cancel.Reason != "the wrong build" {
		t.Errorf("cancel reason = %q", d.cancel.Reason)
	}
	if d.promote.Reason != "checks are green" {
		t.Errorf("promote reason = %q", d.promote.Reason)
	}
	// A rollback heeds a signal rather than overriding one, so an empty reason
	// is a request rather than a mistake.
	if d.rollback.DeploymentID == "" {
		t.Error("rollback never reached the deployer")
	}
	if d.verify.DeploymentID == "" {
		t.Error("verify never reached the deployer")
	}
	if string(d.plan.EnvironmentID) != envID {
		t.Errorf("plan environment = %q, want %q", d.plan.EnvironmentID, envID)
	}
	// The strategy is a proto enum on the wire and a domain value behind it. One
	// that arrived as UNSPECIFIED would be refused before it got this far.
	if d.plan.Strategy != deployment.StrategyRolling {
		t.Errorf("plan strategy = %q, want rolling", d.plan.Strategy)
	}
	// An approval is bound to the revision it was given for, so the hash has to
	// travel: one that arrived empty would approve whatever the plan says now.
	if d.approve.Decision != policy.ApprovalGranted {
		t.Errorf("approval decision = %q, want granted", d.approve.Decision)
	}
	if d.approve.Revision.String() == "" {
		t.Error("the approval reached the deployer unbound to a plan revision")
	}
	// The actor is the credential's, not the request's. No request carries one,
	// and a command that reached the deployer without it would leave an act on
	// the timeline with nobody attached to it (INV-005).
	for name, actor := range map[string]identity.Principal{
		"cancel":   d.cancel.Actor,
		"promote":  d.promote.Actor,
		"rollback": d.rollback.Actor,
		"verify":   d.verify.Actor,
		"plan":     d.plan.Actor,
		"approve":  d.approve.Actor,
	} {
		if actor.ID != anyone().ID {
			t.Errorf("%s actor = %q, want the caller", name, actor.ID)
		}
	}
}

// minted returns an id of the right shape for something this test never
// creates. The service parses every id before it calls anything, so a made-up
// "dpl_1" would be refused as a malformed argument and the command under test
// would never be sent.
func minted[T ~string](t *testing.T, mint func(identity.Generator) (T, error)) string {
	t.Helper()
	id, err := mint(identity.NewGenerator())
	if err != nil {
		t.Fatalf("minting an id: %v", err)
	}
	return string(id)
}

// landed is a plan, the deployment admitted for it, one verification run and
// one event — written straight into the store.
//
// The engine that would produce these is behind the recording deployer, and
// what is in question here is the read projection: a deployment, a plan, a
// verdict and a timeline entry all have wire shapes that no create call
// reaches.
type landed struct {
	plan       plan.DeploymentPlan
	deployment deployment.Deployment
	run        verification.Run
}

func (e *estate) landed(t *testing.T, projectID, envID string) landed {
	t.Helper()
	ctx := context.Background()
	ids := identity.NewGenerator()

	env, err := e.store.Environments().Get(ctx, identity.EnvironmentID(envID))
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	rel := e.release(t, projectID)

	p, err := plan.New(ids, fixedClock{}, anyone(), time.Hour, plan.DeploymentPlan{
		ProjectID:     identity.ProjectID(projectID),
		EnvironmentID: env.ID,
		ReleaseID:     rel,
		BaseRevision:  env.Revision,
		Strategy:      deployment.StrategyRolling,
		Operations: []plan.PlannedOperation{{
			ID:      "op-1",
			Kind:    plan.OperationApply,
			Target:  "api",
			Summary: "roll the api deployment forward",
			Diff: plan.Diff{Changes: []plan.Change{{
				Path: "spec.template.spec.containers[0].image",
				From: "registry.example.com/checkout:1.4.0",
				To:   "registry.example.com/checkout:2.0.0",
			}}},
		}},
		Policy: policy.Decision{
			Allowed: true,
			Reasons: []policy.Reason{{Code: "in_window", Message: "the change window is open"}},
			Requirements: []policy.Requirement{{
				Type:   policy.RequireApproval,
				Role:   "release-manager",
				Count:  1,
				Detail: "production changes need a release manager",
			}},
			Risk: policy.RiskAssessment{
				Level:   policy.RiskLow,
				Score:   0.1,
				Factors: []policy.RiskFactor{{Code: "blast_radius", Message: "one target"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("plan.New: %v", err)
	}
	if err := e.store.Plans().Create(ctx, p); err != nil {
		t.Fatalf("Plans.Create: %v", err)
	}

	d, err := deployment.New(ids, fixedClock{}, anyone(), deployment.Deployment{
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		PlanID:        p.ID,
		Strategy:      deployment.StrategyRolling,
		Trigger:       deployment.Trigger{Type: deployment.TriggerAPI, Detail: "ApplyPlan"},
	})
	if err != nil {
		t.Fatalf("deployment.New: %v", err)
	}
	if d.Revision, err = e.store.Deployments().Create(ctx, d); err != nil {
		t.Fatalf("Deployments.Create: %v", err)
	}

	run, err := verification.New(ids, fixedClock{}, anyone(), verification.Run{
		DeploymentID: d.ID,
		PlanID:       p.ID,
		StartedAt:    at.Add(-10 * time.Minute),
		Checks: []verification.Check{{
			Verifier: verifyv1.VerifierMetadata{Kind: "prometheus", Name: "error-rate", Version: "1.2.0"},
			Result: verifyv1.VerificationResult{
				Verdict:      verifyv1.VerdictPass,
				Reason:       "the error rate stayed under the threshold",
				Measurements: []verifyv1.Measurement{{Name: "error_rate", Value: 0.004}},
				Evidence:     []verifyv1.EvidenceRef{{Kind: "url", URI: "https://grafana.example/d/abc"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("verification.New: %v", err)
	}
	if err := e.store.VerificationRuns().Create(ctx, run); err != nil {
		t.Fatalf("VerificationRuns.Create: %v", err)
	}

	ev, err := event.New(ids, fixedClock{}, anyone(), event.Event{
		Type:          event.DeploymentQueued,
		AggregateType: event.AggregateDeployment,
		AggregateID:   string(d.ID),
		Payload:       json.RawMessage(`{"strategy":"rolling"}`),
	})
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	err = e.store.WithinTransaction(ctx, func(ctx context.Context) error {
		_, appendErr := e.store.Events().Append(ctx, ev)
		return appendErr
	})
	if err != nil {
		t.Fatalf("Events.Append: %v", err)
	}

	return landed{plan: p, deployment: d, run: run}
}

// The read side of a deployment: the deployment, the plan it came from, the
// verdict the checks gave and the timeline. Each has a wire shape of its own,
// and none of them is reachable from a create call.
func TestTheReadSideOfADeploymentIsServedAtItsOwnRPCs(t *testing.T) {
	t.Parallel()
	e := served(t)
	ctx := context.Background()
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)

	d, err := e.client.GetDeployment(ctx, &rollopsv2.GetDeploymentRequest{Id: string(l.deployment.ID)})
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if d.GetId() != string(l.deployment.ID) {
		t.Errorf("id = %q, want %q", d.GetId(), l.deployment.ID)
	}
	if d.GetPlanId() != string(l.plan.ID) {
		t.Errorf("plan id = %q, want %q", d.GetPlanId(), l.plan.ID)
	}
	if d.GetTrigger().GetType() != rollopsv2.TriggerType_TRIGGER_TYPE_API {
		t.Errorf("trigger = %s, want API", d.GetTrigger().GetType())
	}
	if d.GetStatus() == rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_UNSPECIFIED {
		t.Error("the deployment came back with no status, which is not a status it can be in")
	}
	// A deployment that has not started says so by omission rather than by
	// claiming the epoch.
	if d.GetStartedAt() != nil {
		t.Errorf("started at = %s, want absent", d.GetStartedAt().AsTime())
	}

	p, err := e.client.GetPlan(ctx, &rollopsv2.GetPlanRequest{Id: string(l.plan.ID)})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if p.GetHash() == "" {
		t.Error("the plan came back without the hash an approval is bound to")
	}
	if len(p.GetOperations()) != 1 || len(p.GetOperations()[0].GetChanges()) != 1 {
		t.Fatalf("operations = %v", p.GetOperations())
	}
	if got := p.GetOperations()[0].GetChanges()[0].GetPath(); got != "spec.template.spec.containers[0].image" {
		t.Errorf("change path = %q", got)
	}
	if !p.GetPolicy().GetAllowed() ||
		p.GetPolicy().GetRisk().GetLevel() != rollopsv2.RiskLevel_RISK_LEVEL_LOW {
		t.Errorf("policy = %v", p.GetPolicy())
	}
	// An outstanding requirement is a gate somebody has to answer, so it has to
	// survive the wire: a plan that read as unconditionally allowed would have
	// an approver looking for a question that is not there.
	reqs := p.GetPolicy().GetRequirements()
	if len(reqs) != 1 || reqs[0].GetRole() != "release-manager" ||
		reqs[0].GetType() != rollopsv2.RequirementType_REQUIREMENT_TYPE_APPROVAL {
		t.Errorf("requirements = %v", reqs)
	}
	if factors := p.GetPolicy().GetRisk().GetFactors(); len(factors) != 1 ||
		factors[0].GetCode() != "blast_radius" {
		t.Errorf("risk factors = %v", p.GetPolicy().GetRisk().GetFactors())
	}

	timeline, err := e.client.ListDeploymentEvents(ctx, &rollopsv2.ListDeploymentEventsRequest{
		DeploymentId: string(l.deployment.ID),
		Page:         &rollopsv2.PageRequest{PageSize: 10},
	})
	if err != nil {
		t.Fatalf("ListDeploymentEvents: %v", err)
	}
	if len(timeline.GetEvents()) != 1 {
		t.Fatalf("got %d events, want 1", len(timeline.GetEvents()))
	}
	// The type stays a string. §16.4 asks a reader that does not recognise a
	// type to tolerate it, which an enum would turn into an UNSPECIFIED that
	// discards the name it could not match.
	if got := timeline.GetEvents()[0].GetType(); got != string(event.DeploymentQueued) {
		t.Errorf("event = %q, want %q", got, event.DeploymentQueued)
	}
	// The payload travels as the bytes that were written. Re-encoding it here
	// would be a chance to change a log that is immutable (INV-013).
	if got := string(timeline.GetEvents()[0].GetPayload()); got != `{"strategy":"rolling"}` {
		t.Errorf("payload = %s", got)
	}
}

// A verification run is readable at its own RPC. This is where a timeline entry
// leads — the completion event carries the run id and nothing else about it.
func TestAVerificationRunIsReadableAtItsOwnRPC(t *testing.T) {
	t.Parallel()
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)

	got, err := e.client.GetVerificationRun(context.Background(),
		&rollopsv2.GetVerificationRunRequest{Id: string(l.run.ID)})
	if err != nil {
		t.Fatalf("GetVerificationRun: %v", err)
	}

	if got.GetId() != string(l.run.ID) {
		t.Errorf("id = %q, want %q", got.GetId(), l.run.ID)
	}
	if got.GetVerdict() != rollopsv2.Verdict_VERDICT_PASS {
		t.Errorf("verdict = %s, want PASS", got.GetVerdict())
	}
}

// Verifying is the one command that does not answer with a deployment alone:
// the verdict and where it left the deployment are both wanted, so both are in
// the response.
func TestVerifyingAnswersWithTheVerdictBesideTheDeployment(t *testing.T) {
	t.Parallel()
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)
	e.deployer.verdict = &deploy.Verification{ID: l.run.ID}

	got, err := e.client.VerifyDeployment(context.Background(), &rollopsv2.VerifyDeploymentRequest{
		DeploymentId: string(l.deployment.ID),
	})
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}

	if got.GetRun().GetId() != string(l.run.ID) {
		t.Errorf("run = %q, want %q", got.GetRun().GetId(), l.run.ID)
	}
	// The verdict is derived from the checks rather than stored beside them
	// (§11.5), so the one on the wire has to agree with the one they imply.
	if got.GetRun().GetVerdict() != rollopsv2.Verdict_VERDICT_PASS {
		t.Errorf("verdict = %s, want PASS", got.GetRun().GetVerdict())
	}
	checks := got.GetRun().GetChecks()
	if len(checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(checks))
	}
	c := checks[0]
	if c.GetVerifier().GetName() != "error-rate" || c.GetReason() == "" {
		t.Errorf("check = %v", c)
	}
	if len(c.GetMeasurements()) != 1 || c.GetMeasurements()[0].GetValue() != 0.004 {
		t.Errorf("measurements = %v", c.GetMeasurements())
	}
	if len(c.GetEvidence()) != 1 || c.GetEvidence()[0].GetUri() == "" {
		t.Errorf("evidence = %v", c.GetEvidence())
	}
}

// Every collection this API serves answers a read, and a page token it cannot
// read is the caller's mistake rather than a silent fall back to the first page.
func TestEachCollectionIsListedAtItsOwnRPC(t *testing.T) {
	t.Parallel()
	e := served(t)
	ctx := context.Background()
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	e.landed(t, projectID, envID)

	for _, tc := range []struct {
		name string
		list func(*rollopsv2.PageRequest) ([]string, error)
	}{
		{"projects", func(p *rollopsv2.PageRequest) ([]string, error) {
			out, err := e.client.ListProjects(ctx, &rollopsv2.ListProjectsRequest{Page: p})
			return ids(out.GetProjects(), (*rollopsv2.Project).GetId), err
		}},
		{"environments", func(p *rollopsv2.PageRequest) ([]string, error) {
			out, err := e.client.ListEnvironments(ctx,
				&rollopsv2.ListEnvironmentsRequest{ProjectId: projectID, Page: p})
			return ids(out.GetEnvironments(), (*rollopsv2.Environment).GetId), err
		}},
		{"artifacts", func(p *rollopsv2.PageRequest) ([]string, error) {
			out, err := e.client.ListArtifacts(ctx,
				&rollopsv2.ListArtifactsRequest{ProjectId: projectID, Page: p})
			return ids(out.GetArtifacts(), (*rollopsv2.Artifact).GetId), err
		}},
		{"releases", func(p *rollopsv2.PageRequest) ([]string, error) {
			out, err := e.client.ListReleases(ctx,
				&rollopsv2.ListReleasesRequest{ProjectId: projectID, Page: p})
			return ids(out.GetReleases(), (*rollopsv2.Release).GetId), err
		}},
		{"deployments", func(p *rollopsv2.PageRequest) ([]string, error) {
			out, err := e.client.ListDeployments(ctx,
				&rollopsv2.ListDeploymentsRequest{EnvironmentId: envID, Page: p})
			return ids(out.GetDeployments(), (*rollopsv2.Deployment).GetId), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.list(nil)
			if err != nil {
				t.Fatalf("listing: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("the collection came back empty")
			}
			if got[0] == "" {
				t.Error("a listed resource came back without an id")
			}

			if _, err := tc.list(&rollopsv2.PageRequest{PageToken: "not-a-cursor"}); err == nil {
				t.Error("an unreadable page token was accepted")
			}
		})
	}
}

func ids[T any](in []T, id func(T) string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, id(v))
	}
	return out
}
