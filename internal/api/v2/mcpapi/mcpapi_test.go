package mcpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	mcpserver "go.klarlabs.de/mcp"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/mcpapi"
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

func anyone() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

func everyone() mcpapi.IdentifierFunc {
	return func(context.Context) (identity.Principal, error) { return anyone(), nil }
}

// nobody stands in for a connection that never proved who it was.
func nobody() mcpapi.IdentifierFunc {
	return func(context.Context) (identity.Principal, error) {
		return identity.Principal{}, errors.New("no credential")
	}
}

// refusingDeployer carries nothing out and records what it was asked to. The
// write side is exercised in full by the api/v2 tests; what is in question here
// is whether a call that arrived as a tool reached the right method carrying
// the right arguments, and that is answered by the command whether or not the
// engine behind it would have succeeded.
type refusingDeployer struct {
	verdict *deploy.Verification

	plan     deploy.PlanCommand
	apply    deploy.ApplyCommand
	cancel   deploy.CancelCommand
	promote  deploy.PromoteCommand
	rollback deploy.RollbackCommand
	verify   deploy.VerifyCommand
}

var errNotWired = errors.New("the deployer was not meant to carry this out")

func (d *refusingDeployer) Plan(_ context.Context, cmd deploy.PlanCommand) (plan.DeploymentPlan, error) {
	d.plan = cmd
	return plan.DeploymentPlan{}, errNotWired
}

func (d *refusingDeployer) Apply(_ context.Context, cmd deploy.ApplyCommand) (deployment.Deployment, error) {
	d.apply = cmd
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Approve(context.Context, deploy.ApproveCommand) (deployment.Deployment, error) {
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Cancel(_ context.Context, cmd deploy.CancelCommand) (deployment.Deployment, error) {
	d.cancel = cmd
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Verify(
	_ context.Context, cmd deploy.VerifyCommand,
) (deployment.Deployment, deploy.Verification, error) {
	d.verify = cmd
	if d.verdict == nil {
		return deployment.Deployment{}, deploy.Verification{}, errNotWired
	}
	return deployment.Deployment{}, *d.verdict, nil
}

func (d *refusingDeployer) Promote(_ context.Context, cmd deploy.PromoteCommand) (deployment.Deployment, error) {
	d.promote = cmd
	return deployment.Deployment{}, errNotWired
}

func (d *refusingDeployer) Rollback(_ context.Context, cmd deploy.RollbackCommand) (deployment.Deployment, error) {
	d.rollback = cmd
	return deployment.Deployment{}, errNotWired
}

type estate struct {
	srv      *mcpserver.Server
	svc      *apiv2.Service
	store    *memory.Store
	deployer *refusingDeployer
}

// served registers the tools against the real service over an in-memory store.
// Everything behind a tool is the real thing: a fake service would let the tool
// surface agree with a projection nobody serves.
func served(t *testing.T, who mcpapi.Identifier) *estate {
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

	deployer := &refusingDeployer{}
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
	srv, err := mcpapi.New(mcpapi.Config{Service: svc, Identity: who})
	if err != nil {
		t.Fatalf("mcpapi.New: %v", err)
	}
	return &estate{srv: srv, svc: svc, store: store, deployer: deployer}
}

// raw invokes a tool the way a client does — by name, with JSON — and returns
// the result marshalled back to JSON. Going through Execute rather than calling
// the handler means the generated input schema validates the arguments and the
// json tags decide the field names, both of which are what an agent actually
// sees.
func (e *estate) raw(t *testing.T, name string, in any) (json.RawMessage, error) {
	t.Helper()
	tool, ok := e.srv.GetTool(name)
	if !ok {
		t.Fatalf("no tool named %q", name)
	}
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal %s input: %v", name, err)
	}
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal %s output: %v", name, err)
	}
	return body, nil
}

// call invokes a tool and decodes what it answered, failing the test if it did
// not answer at all.
func call[T any](t *testing.T, e *estate, name string, in any) T {
	t.Helper()
	body, err := e.raw(t, name, in)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var got T
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s output: %v", name, err)
	}
	return got
}

func TestAServiceAndAnIdentifierAreBothRequired(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  mcpapi.Config
	}{
		{"no service", mcpapi.Config{Identity: everyone()}},
		{"no identifier", mcpapi.Config{Service: &apiv2.Service{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := mcpapi.New(tc.cfg); err == nil {
				t.Fatal("New accepted a config it cannot serve")
			}
		})
	}
}

// canonTools is the tool list §25.1 names, exactly. It is written out rather
// than read off the server so that a tool quietly added, renamed or dropped is
// a failing test and a conversation about the canon rather than a silent change
// to a contract agents are pointed at.
var canonTools = []string{
	"rollops.inspect_environment",
	"rollops.list_releases",
	"rollops.create_release",
	"rollops.plan_deployment",
	"rollops.explain_plan",
	"rollops.apply_plan",
	"rollops.get_deployment",
	"rollops.verify_deployment",
	"rollops.promote_deployment",
	"rollops.rollback_deployment",
	"rollops.cancel_deployment",
	"rollops.get_timeline",
}

func TestTheToolSetIsTheOneTheCanonNames(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())

	var got []string
	for _, info := range e.srv.Tools() {
		got = append(got, info.Name)
	}
	slices.Sort(got)
	want := slices.Clone(canonTools)
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
}

// Nothing here approves. An agent that could grant the approval its own plan is
// waiting for would be holding both sides of the gate, so requesting one is an
// action it may name and giving one is not a tool it has.
func TestNoToolApproves(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())

	for _, info := range e.srv.Tools() {
		if strings.Contains(info.Name, "approve") {
			t.Errorf("the tool surface offers %q", info.Name)
		}
	}
}

// Every tool refuses a caller that has not said who it is, reads included:
// answering a read with NOT_FOUND would tell an unidentified agent which ids
// exist. The table is the whole surface, because the one read that forgets to
// ask is the one worth finding.
func TestEveryToolRefusesAnUnidentifiedCaller(t *testing.T) {
	t.Parallel()
	e := served(t, nobody())

	for _, name := range canonTools {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Arguments that would satisfy any tool's schema. None of them are
			// read: the refusal happens first, which is the point.
			_, err := e.raw(t, name, map[string]any{
				"environment_id": "env_1",
				"project_id":     "prj_1",
				"release_id":     "rel_1",
				"plan_id":        "pln_1",
				"deployment_id":  "dpl_1",
				"version":        "1.0.0",
				"strategy":       "rolling",
				"reason":         "because",
			})
			if err == nil {
				t.Fatal("the tool answered a caller it could not identify")
			}
			var te *mcpapi.ToolError
			if !errors.As(err, &te) {
				t.Fatalf("error = %v, want a *ToolError", err)
			}
			if te.Code != apierr.Unauthorized {
				t.Errorf("code = %s, want UNAUTHORIZED", te.Code)
			}
		})
	}
}

// The secret an environment is declared with. Both are literals here so that a
// result carrying one through can be found by searching the bytes for it.
//
// The value is deliberately not shaped like the credential it stands for. What
// the assertion needs is a string that could only have come from the fixture,
// and a real-looking connection string in a test file is a secret scanner's
// finding for as long as the file exists.
const (
	secretName  = "prod/kubeconfig"
	secretValue = "THE-VALUE-NO-AGENT-MAY-SEE"
)

// seeded is a project, an environment, a release, a plan, the deployment
// admitted for it, one verification run and one timeline entry — written
// through the service and the store rather than through the tools.
//
// The tools deliberately cannot reach most of this: there is no create_project
// or create_environment on the agent surface, and the engine that would produce
// a plan and a deployment is behind the refusing deployer. What is in question
// here is the projection, so the fixture puts the shapes in place and lets the
// reads describe them.
type seeded struct {
	projectID string
	envID     string
	release   identity.ReleaseID
	plan      plan.DeploymentPlan
	deploy    deployment.Deployment
	run       verification.Run
}

func (e *estate) seed(t *testing.T) seeded {
	t.Helper()
	ctx := context.Background()
	ids := identity.NewGenerator()

	prj, err := e.svc.CreateProject(ctx, apiv2.CreateProjectRequest{
		Name: "checkout", Description: "the checkout service", Actor: anyone(),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	env, err := e.svc.CreateEnvironment(ctx, apiv2.CreateEnvironmentRequest{
		ProjectID: prj.ID,
		Name:      "production",
		Kind:      "production",
		Targets: []apiv2.TargetSpec{{
			Name:   "api",
			Driver: "kubernetes",
			Config: map[string]apiv2.Value{
				"namespace":  {Literal: "payments"},
				"kubeconfig": {Secret: secretName},
			},
		}},
		Policies:  []apiv2.PolicyBinding{{Name: "change-window", Ref: "policy://change-window", Mode: "enforce"}},
		Variables: map[string]apiv2.Value{"DATABASE_URL": {Secret: secretName}},
		Actor:     anyone(),
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	// The revision a plan pins itself to is the stored one. What the create
	// call returned describes the environment it made, not the row a
	// concurrent write would have to beat.
	stored, err := e.store.Environments().Get(ctx, identity.EnvironmentID(env.ID))
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}

	digest := "sha256:" + strings.Repeat("c", 64)
	art, err := e.svc.RegisterArtifact(ctx, apiv2.RegisterArtifactRequest{
		ProjectID: prj.ID,
		Kind:      "oci-image",
		Digest:    digest,
		Locator:   "registry.example.com/checkout@" + digest,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Actor:     anyone(),
	})
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	rel := call[map[string]any](t, e, "rollops.create_release", map[string]any{
		"project_id":        prj.ID,
		"version":           "2.0.0",
		"artifacts":         []map[string]string{{"role": "app", "artifact_id": art.ID}},
		"source_provider":   "git",
		"source_repository": "example/checkout",
		"source_revision":   strings.Repeat("d", 40),
		"source_ref":        "refs/heads/main",
	})
	releaseID, _ := rel["release_id"].(string)
	if releaseID == "" {
		t.Fatalf("create_release returned no id: %v", rel)
	}

	p, err := plan.New(ids, fixedClock{}, anyone(), time.Hour, plan.DeploymentPlan{
		ProjectID:     identity.ProjectID(prj.ID),
		EnvironmentID: identity.EnvironmentID(env.ID),
		ReleaseID:     identity.ReleaseID(releaseID),
		BaseRevision:  stored.Revision,
		Strategy:      deployment.StrategyRolling,
		Operations: []plan.PlannedOperation{{
			ID:      "op-1",
			Kind:    plan.OperationApply,
			Target:  "api",
			Summary: "roll the api deployment forward",
			Diff: plan.Diff{Changes: []plan.Change{
				{
					Path: "spec.template.spec.containers[0].image",
					From: "registry.example.com/checkout:1.4.0",
					To:   "registry.example.com/checkout:2.0.0",
				},
				// The one row an agent must be told changed and must not be
				// told the value of.
				{Path: "env.DATABASE_URL", From: secretValue, To: secretValue, Sensitive: true},
			}},
		}},
		Policy: policy.Decision{
			Allowed: false,
			Reasons: []policy.Reason{{Code: "needs_approval", Message: "production is gated"}},
			Requirements: []policy.Requirement{{
				Type:   policy.RequireApproval,
				Role:   "release-manager",
				Count:  1,
				Detail: "production changes need a release manager",
			}},
			Risk: policy.RiskAssessment{
				Level:   policy.RiskHigh,
				Score:   0.8,
				Factors: []policy.RiskFactor{{Code: "blast_radius", Message: "every checkout request"}},
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
		Trigger:       deployment.Trigger{Type: deployment.TriggerAPI, Detail: "rollops.apply_plan"},
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
		_, err := e.store.Events().Append(ctx, ev)
		return err
	})
	if err != nil {
		t.Fatalf("Events.Append: %v", err)
	}

	return seeded{
		projectID: prj.ID,
		envID:     env.ID,
		release:   identity.ReleaseID(releaseID),
		plan:      p,
		deploy:    d,
		run:       run,
	}
}

// §25.2 asks every result an agent reads to carry the ids to act on, the state
// the thing is in, what may be done next, what policy said, how risky it is and
// whether an approval is outstanding. This walks the reads an agent deciding
// about a deployment would make and asserts each of those arrives.
func TestAPlanAndItsDeploymentCarryWhatAnAgentNeedsToDecide(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())
	s := e.seed(t)

	p := call[mcpapi.PlanOut](t, e, "rollops.explain_plan",
		map[string]any{"plan_id": string(s.plan.ID)})

	if p.PlanID != string(s.plan.ID) {
		t.Errorf("plan id = %q, want %q", p.PlanID, s.plan.ID)
	}
	if p.EnvironmentID != s.envID || p.ReleaseID != string(s.release) {
		t.Errorf("plan names %q/%q, want %q/%q", p.EnvironmentID, p.ReleaseID, s.envID, s.release)
	}
	// An agent holding a plan has to be able to tell for itself whether it
	// still describes the world, and to say which plan it asked a human about.
	if p.BaseRevision == 0 {
		t.Error("the plan did not say which revision it was computed against")
	}
	if p.Hash == "" {
		t.Error("the plan did not say what an approval would bind to")
	}
	if p.Policy.Allowed {
		t.Error("policy allowed a plan the fixture gated")
	}
	if p.Policy.Risk.Level != string(policy.RiskHigh) {
		t.Errorf("risk = %q, want high", p.Policy.Risk.Level)
	}
	if len(p.Policy.Reasons) == 0 {
		t.Error("policy refused without saying why")
	}
	// Waiting on an approval and being refused outright are different things
	// to do next, so the flag and the action have to agree.
	if !p.ApprovalRequired {
		t.Error("a plan requiring an approval did not say so")
	}
	if !slices.Equal(p.NextActions, []string{apiv2.ActionRequestApproval}) {
		t.Errorf("next actions = %v, want [request_approval]", p.NextActions)
	}

	d := call[mcpapi.DeploymentOut](t, e, "rollops.get_deployment",
		map[string]any{"deployment_id": string(s.deploy.ID)})

	if d.DeploymentID != string(s.deploy.ID) {
		t.Errorf("deployment id = %q, want %q", d.DeploymentID, s.deploy.ID)
	}
	if d.PlanID != string(s.plan.ID) {
		t.Errorf("deployment plan id = %q, want %q", d.PlanID, s.plan.ID)
	}
	if d.Status != string(deployment.StatusPlanned) {
		t.Errorf("status = %q, want planned", d.Status)
	}
	if d.Terminal {
		t.Error("a planned deployment was reported terminal")
	}
	if !slices.Contains(d.NextActions, apiv2.ActionCancel) {
		t.Errorf("next actions = %v, want cancel among them", d.NextActions)
	}
	if d.Actor.ID != anyone().ID {
		t.Errorf("actor = %q, want the caller", d.Actor.ID)
	}

	tl := call[mcpapi.GetTimelineOut](t, e, "rollops.get_timeline",
		map[string]any{"deployment_id": string(s.deploy.ID)})
	if len(tl.Events) != 1 {
		t.Fatalf("timeline has %d events, want 1", len(tl.Events))
	}
	if tl.Events[0].Type != string(event.DeploymentQueued) {
		t.Errorf("event type = %q, want %q", tl.Events[0].Type, event.DeploymentQueued)
	}
	if string(tl.Events[0].Payload) != `{"strategy":"rolling"}` {
		t.Errorf("payload = %s, want it passed through", tl.Events[0].Payload)
	}

	// Verifying is the one write the refusing deployer is taught to answer,
	// because the verdict is what decides the status and an agent reading a
	// run with no checks in it cannot tell a pass from an empty answer.
	e.deployer.verdict = &deploy.Verification{ID: s.run.ID}
	v := call[mcpapi.VerifyDeploymentOut](t, e, "rollops.verify_deployment",
		map[string]any{"deployment_id": string(s.deploy.ID)})
	if v.Run.RunID != string(s.run.ID) {
		t.Errorf("run id = %q, want %q", v.Run.RunID, s.run.ID)
	}
	if v.Run.Verdict != string(verifyv1.VerdictPass) {
		t.Errorf("verdict = %q, want pass", v.Run.Verdict)
	}
	if len(v.Run.Checks) != 1 || v.Run.Checks[0].Name != "error-rate" {
		t.Errorf("checks = %+v, want the one the fixture recorded", v.Run.Checks)
	}
	if v.Deployment.DeploymentID != string(s.deploy.ID) {
		t.Errorf("verify answered about %q, want %q", v.Deployment.DeploymentID, s.deploy.ID)
	}
}

// A model only ever sees an error's text, so the stable code has to be in it.
// An agent branching on APPROVAL_REQUIRED against POLICY_DENIED is reading this
// string, and the two differ by what it should do next rather than by how badly
// the call went.
func TestAFailureReachesTheAgentAsACodeInFrontOfAMessage(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())

	// A well-formed id for a plan nobody made. It has to be generated rather
	// than written out, because a literal that stopped parsing would quietly
	// turn this case into the one above it.
	missing, err := identity.NewPlanID(identity.NewGenerator())
	if err != nil {
		t.Fatalf("NewPlanID: %v", err)
	}

	for _, tc := range []struct {
		name string
		tool string
		in   any
		want apierr.Code
	}{
		{
			"a malformed id is the caller's mistake",
			"rollops.get_deployment",
			map[string]any{"deployment_id": "not-a-deployment"},
			apierr.InvalidArgument,
		},
		{
			"a well-formed id for nothing is not found",
			"rollops.explain_plan",
			map[string]any{"plan_id": string(missing)},
			apierr.NotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.raw(t, tc.tool, tc.in)
			if err == nil {
				t.Fatal("the tool answered a call it could not serve")
			}
			var te *mcpapi.ToolError
			if !errors.As(err, &te) {
				t.Fatalf("error = %v, want a *ToolError", err)
			}
			if te.Code != tc.want {
				t.Errorf("code = %s, want %s", te.Code, tc.want)
			}
			// The text is what reaches the model. A code that is only in a
			// field the transport drops is a code nothing can branch on.
			if !strings.HasPrefix(err.Error(), string(tc.want)+": ") {
				t.Errorf("error text = %q, want it to lead with %s", err.Error(), tc.want)
			}
			if te.Message == "" {
				t.Error("the failure carried a code and nothing to read")
			}
			// The typed error stays reachable underneath. A transport wrapping
			// this surface should not have to re-parse the string it was built
			// from to find out what it already knows.
			var typed *apierr.Error
			if !errors.As(err, &typed) {
				t.Fatalf("the *apierr.Error behind %v was not reachable", err)
			}
			if typed.Code != tc.want {
				t.Errorf("wrapped code = %s, want %s", typed.Code, tc.want)
			}
		})
	}
}

// §25.2: using MCP does not entitle an agent to a secret. Nothing in this
// package strips one — the views it projects already carry keys without values
// — so what this asserts is that no field added here reaches past them.
func TestNoSecretReachesAnAgent(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())
	s := e.seed(t)

	env, err := e.raw(t, "rollops.inspect_environment", map[string]any{"environment_id": s.envID})
	if err != nil {
		t.Fatalf("inspect_environment: %v", err)
	}
	var got mcpapi.InspectEnvironmentOut
	if err := json.Unmarshal(env, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The names are the point: they answer whether the environment configures
	// the thing the agent expected, without answering what it is.
	if len(got.Environment.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(got.Environment.Targets))
	}
	for _, want := range []string{"kubeconfig", "namespace"} {
		if !slices.Contains(got.Environment.Targets[0].ConfigKeys, want) {
			t.Errorf("config keys = %v, want %q among them", got.Environment.Targets[0].ConfigKeys, want)
		}
	}
	if !slices.Contains(got.Environment.VariableNames, "DATABASE_URL") {
		t.Errorf("variable names = %v, want DATABASE_URL", got.Environment.VariableNames)
	}

	plan, err := e.raw(t, "rollops.explain_plan", map[string]any{"plan_id": string(s.plan.ID)})
	if err != nil {
		t.Fatalf("explain_plan: %v", err)
	}

	// A sensitive change is kept with its values blank rather than dropped, so
	// that a model cannot mistake a withheld value for a field that did not
	// change — and does not go looking for it somewhere it might be found.
	var p mcpapi.PlanOut
	if err := json.Unmarshal(plan, &p); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	var sensitive *mcpapi.ChangeOut
	for i, c := range p.Operations[0].Changes {
		if c.Path == "env.DATABASE_URL" {
			sensitive = &p.Operations[0].Changes[i]
		}
	}
	if sensitive == nil {
		t.Fatalf("the sensitive change was dropped: %+v", p.Operations[0].Changes)
	}
	if !sensitive.Sensitive {
		t.Error("the sensitive change did not say it was withheld")
	}
	if sensitive.From != "" || sensitive.To != "" {
		t.Errorf("the sensitive change carried %q -> %q", sensitive.From, sensitive.To)
	}

	// Searching the bytes rather than the fields, because a secret that leaks
	// does so through whichever field nobody thought to check.
	for name, body := range map[string]json.RawMessage{
		"inspect_environment": env,
		"explain_plan":        plan,
	} {
		if strings.Contains(string(body), secretValue) {
			t.Errorf("%s carried the secret's value", name)
		}
	}
	if strings.Contains(string(env), secretName) {
		t.Errorf("inspect_environment named the secret it resolves from: %s", env)
	}
}

// Each write tool reaches the method its name promises, carrying the caller as
// the actor.
//
// The actor is the point. There is no actor field on any tool input, so a model
// cannot name somebody else — an act on the timeline with the wrong principal
// attached is worse than one with none (INV-005). The deployer refuses
// everything, so these calls are sent for what they record rather than for what
// they return.
func TestEachWriteToolReachesItsOwnMethodAsTheCallerWhoSentIt(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())
	s := e.seed(t)
	id := string(s.deploy.ID)

	for _, c := range []struct {
		tool string
		in   any
	}{
		{"rollops.cancel_deployment", map[string]any{"deployment_id": id, "reason": "the wrong build"}},
		{"rollops.promote_deployment", map[string]any{"deployment_id": id, "reason": "checks are green"}},
		{"rollops.rollback_deployment", map[string]any{"deployment_id": id}},
		{"rollops.verify_deployment", map[string]any{"deployment_id": id}},
		{"rollops.apply_plan", map[string]any{"plan_id": string(s.plan.ID), "detail": "CHG-41"}},
		{"rollops.plan_deployment", map[string]any{
			"environment_id": s.envID, "release_id": string(s.release), "strategy": "rolling",
		}},
	} {
		if _, err := e.raw(t, c.tool, c.in); err == nil {
			t.Errorf("%s: the refusing deployer answered", c.tool)
		}
	}

	d := e.deployer
	if d.cancel.Reason != "the wrong build" {
		t.Errorf("cancel reason = %q", d.cancel.Reason)
	}
	if d.promote.Reason != "checks are green" {
		t.Errorf("promote reason = %q", d.promote.Reason)
	}
	// A rollback heeds a signal rather than overriding one, so no reason is a
	// request rather than a mistake.
	if string(d.rollback.DeploymentID) != id {
		t.Errorf("rollback deployment = %q, want %q", d.rollback.DeploymentID, id)
	}
	if string(d.verify.DeploymentID) != id {
		t.Errorf("verify deployment = %q, want %q", d.verify.DeploymentID, id)
	}
	if string(d.apply.PlanID) != string(s.plan.ID) {
		t.Errorf("apply plan = %q, want %q", d.apply.PlanID, s.plan.ID)
	}
	// The trigger says an API call did this and the detail says which. The
	// type is not the caller's to choose; the detail is.
	if d.apply.Trigger.Type != deployment.TriggerAPI || d.apply.Trigger.Detail != "CHG-41" {
		t.Errorf("apply trigger = %+v, want api/CHG-41", d.apply.Trigger)
	}
	if string(d.plan.EnvironmentID) != s.envID {
		t.Errorf("plan environment = %q, want %q", d.plan.EnvironmentID, s.envID)
	}

	for name, actor := range map[string]identity.Principal{
		"cancel":   d.cancel.Actor,
		"promote":  d.promote.Actor,
		"rollback": d.rollback.Actor,
		"verify":   d.verify.Actor,
		"apply":    d.apply.Actor,
		"plan":     d.plan.Actor,
	} {
		if actor.ID != anyone().ID {
			t.Errorf("%s actor = %q, want the caller", name, actor.ID)
		}
	}
}

// A release created through a tool is found by the listing tool, so the two
// halves of the surface describe the same project rather than two views of it.
func TestAReleaseCreatedByAToolIsListedByOne(t *testing.T) {
	t.Parallel()
	e := served(t, everyone())
	s := e.seed(t)

	got := call[mcpapi.ListReleasesOut](t, e, "rollops.list_releases",
		map[string]any{"project_id": s.projectID, "page_size": 10})

	if len(got.Releases) != 1 {
		t.Fatalf("releases = %d, want 1", len(got.Releases))
	}
	r := got.Releases[0]
	if r.ReleaseID != string(s.release) {
		t.Errorf("release id = %q, want %q", r.ReleaseID, s.release)
	}
	if r.Version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", r.Version)
	}
	if r.Source.Repository != "example/checkout" {
		t.Errorf("source repository = %q", r.Source.Repository)
	}
	if r.CreatedBy.ID != anyone().ID {
		t.Errorf("created by = %q, want the caller", r.CreatedBy.ID)
	}
}

// An Identifier that has already worked out what kind of refusal this is keeps
// its answer.
//
// Telling a caller whose credential is fine but whose permissions are not to go
// and authenticate again will not help, and an agent branching on the code
// would retry a call that can only ever be refused.
func TestAnIdentifierThatAlreadyChoseACodeKeepsIt(t *testing.T) {
	t.Parallel()
	e := served(t, mcpapi.IdentifierFunc(func(context.Context) (identity.Principal, error) {
		return identity.Principal{}, &apierr.Error{
			Code:    apierr.Forbidden,
			Message: "this token may read but not deploy",
		}
	}))

	_, err := e.raw(t, "rollops.cancel_deployment",
		map[string]any{"deployment_id": "dpl_1", "reason": "because"})
	if err == nil {
		t.Fatal("the tool answered a caller it was told to refuse")
	}
	var te *mcpapi.ToolError
	if !errors.As(err, &te) {
		t.Fatalf("error = %v, want a *ToolError", err)
	}
	if te.Code != apierr.Forbidden {
		t.Errorf("code = %s, want FORBIDDEN", te.Code)
	}
	if te.Message != "this token may read but not deploy" {
		t.Errorf("message = %q, want the identifier's own", te.Message)
	}
}
