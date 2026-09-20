package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/httpapi"
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
// here is whether a request that arrived over HTTP reached the right call
// carrying the right values. Recording the command answers that directly,
// where fabricating a plan and a deployment to hand back would mostly assert
// that the fixtures were built as written.
type recordingDeployer struct {
	plan     deploy.PlanCommand
	apply    deploy.ApplyCommand
	cancel   deploy.CancelCommand
	promote  deploy.PromoteCommand
	rollback deploy.RollbackCommand
	approve  deploy.ApproveCommand
	verify   deploy.VerifyCommand

	// verdict is the run a verification is to be answered with, and admitted
	// the deployment an apply is to be answered with. Both are nil by default
	// — most of what this fake is asked is whether the command arrived — and
	// set when the response shape is what is under test.
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

type estate struct {
	h        http.Handler
	store    *memory.Store
	deployer *recordingDeployer
}

// served wires the real service over an in-memory store. Everything behind the
// resource calls is the real thing: a fake service would let the transport
// agree with a projection nobody serves.
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
	h, err := httpapi.New(httpapi.Config{Service: svc, Identity: everyone()})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	return &estate{h: h, store: store, deployer: deployer}
}

// ok sends a request and insists it succeeded, decoding the body into v. A
// test that meant to exercise a route and silently got a 404 would otherwise
// go on to assert things about an error envelope.
func (e *estate) ok(t *testing.T, method, target, body string, v any) {
	t.Helper()
	w := send(t, e.h, method, target, body)
	if w.Code < 200 || w.Code > 299 {
		t.Fatalf("%s %s = %d: %s", method, target, w.Code, w.Body.String())
	}
	if v == nil {
		return
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("%s %s: body is not the shape we asked for: %v (%q)", method, target, err, w.Body.String())
	}
}

type idOnly struct {
	ID string `json:"id"`
}

// minted returns an id of the right shape for something this test never
// creates. The service parses every id before it calls anything, so a
// made-up "dpl_1" would be refused as a malformed argument and the command
// under test would never be sent.
func minted[T ~string](t *testing.T, mint func(identity.Generator) (T, error)) string {
	t.Helper()
	id, err := mint(identity.NewGenerator())
	if err != nil {
		t.Fatalf("minting an id: %v", err)
	}
	return string(id)
}

func (e *estate) project(t *testing.T) string {
	t.Helper()
	var p idOnly
	e.ok(t, "POST", "/v2/projects",
		`{"name":"checkout","description":"the checkout service"}`, &p)
	if p.ID == "" {
		t.Fatal("the created project came back without an id")
	}
	return p.ID
}

// The secret an environment is declared with. It is a literal in the test so
// that a body carrying it through can be found by searching for it.
const secretName = "prod/kubeconfig"

func (e *estate) environment(t *testing.T, projectID string) string {
	t.Helper()
	var env idOnly
	e.ok(t, "POST", "/v2/projects/"+projectID+"/environments", `{
		"name": "production",
		"kind": "production",
		"targets": [{
			"name": "api",
			"driver": "kubernetes",
			"config": {"namespace": {"literal": "payments"},
			           "kubeconfig": {"secret": "`+secretName+`"}}
		}],
		"policies": [{"name": "change-window", "ref": "policy://change-window", "mode": "enforce"}],
		"variables": {"DATABASE_URL": {"secret": "prod/db"}},
		"lifecycle": {"ttl": "72h"}
	}`, &env)
	return env.ID
}

func TestAResourceIsReadableAtTheIDItWasCreatedWith(t *testing.T) {
	e := served(t)
	projectID := e.project(t)

	var got struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Labels      map[string]string `json:"labels"`
		CreatedAt   time.Time         `json:"created_at"`
		Revision    uint64            `json:"revision"`
	}
	e.ok(t, "GET", "/v2/projects/"+projectID, "", &got)

	if got.ID != projectID {
		t.Errorf("id = %q, want %q", got.ID, projectID)
	}
	if got.Name != "checkout" {
		t.Errorf("name = %q, want checkout", got.Name)
	}
	if got.Description != "the checkout service" {
		t.Errorf("description = %q", got.Description)
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("created at = %s, want %s", got.CreatedAt, at)
	}
	// Without it a caller cannot tell that the project moved under them.
	if got.Revision == 0 {
		t.Error("revision is zero")
	}
}

// The write shape and the read shape of an environment differ on purpose. This
// is the one that would hurt if it regressed: a read that carried a secret
// name — let alone a resolved value — would put the estate's credential layout
// on the wire (INV-012).
func TestAnEnvironmentIsReadBackAsKeysAndNeverAsValues(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)

	w := send(t, e.h, "GET", "/v2/environments/"+envID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secretName) {
		t.Errorf("the environment read back naming a secret: %s", w.Body.String())
	}

	var got struct {
		Variables []string `json:"variables"`
		Targets   []struct {
			Name   string   `json:"name"`
			Config []string `json:"config"`
		} `json:"targets"`
		Policies []struct {
			Name string `json:"name"`
			Ref  string `json:"ref"`
			Mode string `json:"mode"`
		} `json:"policies"`
		Lifecycle struct {
			TTL string `json:"ttl"`
		} `json:"lifecycle"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("the environment is not the shape we publish: %v", err)
	}
	// A policy binding names a policy rather than carrying one, so it is a
	// reference on both sides and reads back as it was written.
	if len(got.Policies) != 1 || got.Policies[0].Mode != "enforce" ||
		got.Policies[0].Ref != "policy://change-window" {
		t.Errorf("policies = %v", got.Policies)
	}
	if len(got.Variables) != 1 || got.Variables[0] != "DATABASE_URL" {
		t.Errorf("variables = %v, want the names only", got.Variables)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("targets = %v", got.Targets)
	}
	if want := []string{"kubeconfig", "namespace"}; !equal(got.Targets[0].Config, want) {
		t.Errorf("target config = %v, want %v", got.Targets[0].Config, want)
	}
	// A duration a caller wrote as "72h" comes back readable rather than as the
	// nanosecond count Go would marshal a time.Duration to.
	if got.Lifecycle.TTL != "72h0m0s" {
		t.Errorf("ttl = %q, want a duration string", got.Lifecycle.TTL)
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAnArtifactAndItsReleaseTravelTheNestedPaths(t *testing.T) {
	e := served(t)
	projectID := e.project(t)

	var a idOnly
	e.ok(t, "POST", "/v2/projects/"+projectID+"/artifacts", `{
		"kind": "oci-image",
		"digest": "sha256:`+strings.Repeat("a", 64)+`",
		"locator": "registry.example.com/checkout@sha256:`+strings.Repeat("a", 64)+`",
		"media_type": "application/vnd.oci.image.manifest.v1+json"
	}`, &a)

	var r idOnly
	e.ok(t, "POST", "/v2/projects/"+projectID+"/releases", `{
		"version": "1.4.0",
		"artifacts": [{"role": "app", "artifact_id": "`+a.ID+`"}],
		"source": {
			"provider": "git",
			"repository": "example/checkout",
			"revision": "`+strings.Repeat("b", 40)+`",
			"ref": "refs/heads/main"
		}
	}`, &r)

	var got struct {
		Version   string `json:"version"`
		Artifacts []struct {
			Role       string `json:"role"`
			ArtifactID string `json:"artifact_id"`
		} `json:"artifacts"`
	}
	e.ok(t, "GET", "/v2/releases/"+r.ID, "", &got)
	if got.Version != "1.4.0" {
		t.Errorf("version = %q, want 1.4.0", got.Version)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].ArtifactID != a.ID {
		t.Errorf("artifacts = %v, want the one registered", got.Artifacts)
	}

	var list struct {
		Artifacts []idOnly `json:"artifacts"`
	}
	e.ok(t, "GET", "/v2/projects/"+projectID+"/artifacts", "", &list)
	if len(list.Artifacts) != 1 || list.Artifacts[0].ID != a.ID {
		t.Errorf("the project's artifacts = %v", list.Artifacts)
	}
}

// A retry says it is a retry with a header, so that the same sentence reads on
// every command — including the ones whose bodies are empty.
func TestAKeyedRetryIsAnsweredWithTheFirstCallsResource(t *testing.T) {
	e := served(t)

	first := httptest.NewRequest("POST", "/v2/projects", strings.NewReader(`{"name":"checkout"}`))
	first.Header.Set("Idempotency-Key", "k1")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, first)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var one idOnly
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil {
		t.Fatalf("body: %v", err)
	}

	second := httptest.NewRequest("POST", "/v2/projects", strings.NewReader(`{"name":"checkout"}`))
	second.Header.Set("Idempotency-Key", "k1")
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, second)
	if w.Code != http.StatusOK {
		t.Fatalf("the retry was refused: %d %s", w.Code, w.Body.String())
	}
	var two idOnly
	if err := json.Unmarshal(w.Body.Bytes(), &two); err != nil {
		t.Fatalf("body: %v", err)
	}
	if one.ID != two.ID {
		t.Errorf("the retry made a second project: %q then %q", one.ID, two.ID)
	}
}

// What the transport owes each command: the id from the path, the prose from
// the body, and the caller from the credential rather than from either.
func TestACommandReachesTheDeployerCarryingWhatTheRequestSaid(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)

	deploymentID := minted(t, identity.NewDeploymentID)
	releaseID := minted(t, identity.NewReleaseID)

	// The deployer refuses everything, so these are sent for what they record.
	send(t, e.h, "POST", "/v2/deployments/"+deploymentID+":cancel", `{"reason":"the wrong build"}`)
	send(t, e.h, "POST", "/v2/deployments/"+deploymentID+":promote", `{"reason":"checks are green"}`)
	send(t, e.h, "POST", "/v2/deployments/"+deploymentID+":rollback", "")
	send(t, e.h, "POST", "/v2/deployments/"+deploymentID+":verify", "")
	send(t, e.h, "POST", "/v2/environments/"+envID+"/deployments:plan",
		`{"release_id":"`+releaseID+`","strategy":"rolling"}`)

	d := e.deployer
	if d.cancel.Reason != "the wrong build" {
		t.Errorf("cancel reason = %q", d.cancel.Reason)
	}
	if d.promote.Reason != "checks are green" {
		t.Errorf("promote reason = %q", d.promote.Reason)
	}
	// A rollback heeds a signal rather than overriding one, so an empty body is
	// a request rather than a mistake.
	if d.rollback.DeploymentID == "" {
		t.Error("rollback never reached the deployer")
	}
	if d.verify.DeploymentID == "" {
		t.Error("verify never reached the deployer")
	}
	if string(d.plan.EnvironmentID) != envID {
		t.Errorf("plan environment = %q, want %q", d.plan.EnvironmentID, envID)
	}
	// The actor is the credential's, not the body's. Every command carries it,
	// and one that did not would leave an act on the timeline with nobody
	// attached to it (INV-005).
	for name, actor := range map[string]identity.Principal{
		"cancel":   d.cancel.Actor,
		"promote":  d.promote.Actor,
		"rollback": d.rollback.Actor,
		"verify":   d.verify.Actor,
		"plan":     d.plan.Actor,
	} {
		if actor.ID != anyone().ID {
			t.Errorf("%s actor = %q, want the caller", name, actor.ID)
		}
	}
}

// landed is a plan, the deployment admitted for it, one verification run and
// one event — written straight into the store.
//
// The engine that would produce these is behind the recording deployer, and
// what is in question here is the read projection: a deployment, a plan, a
// verdict and a timeline entry all have wire shapes that no create call
// reaches. Driving them through a real apply would test the engine twice and
// this transport once.
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
		Trigger:       deployment.Trigger{Type: deployment.TriggerAPI, Detail: "POST /v2/deployment-plans/:apply"},
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

	return landed{plan: p, deployment: d, run: run}
}

// release registers an artifact and a release for it, returning the release id.
func (e *estate) release(t *testing.T, projectID string) identity.ReleaseID {
	t.Helper()
	sha := "sha256:" + strings.Repeat("c", 64)
	var a idOnly
	e.ok(t, "POST", "/v2/projects/"+projectID+"/artifacts", `{
		"kind": "oci-image",
		"digest": "`+sha+`",
		"locator": "registry.example.com/checkout@`+sha+`",
		"media_type": "application/vnd.oci.image.manifest.v1+json"
	}`, &a)

	var r idOnly
	e.ok(t, "POST", "/v2/projects/"+projectID+"/releases", `{
		"version": "2.0.0",
		"artifacts": [{"role": "app", "artifact_id": "`+a.ID+`"}],
		"source": {
			"provider": "git",
			"repository": "example/checkout",
			"revision": "`+strings.Repeat("d", 40)+`",
			"ref": "refs/heads/main"
		}
	}`, &r)
	return identity.ReleaseID(r.ID)
}

// The read side of a deployment: the deployment, the plan it came from, the
// verdict the checks gave and the timeline. Each has a wire shape of its own,
// and none of them is reachable from a create call.
func TestTheReadSideOfADeploymentIsServedAtItsOwnPaths(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)

	var d struct {
		ID            string `json:"id"`
		EnvironmentID string `json:"environment_id"`
		PlanID        string `json:"plan_id"`
		Status        string `json:"status"`
		Strategy      string `json:"strategy"`
		Trigger       struct {
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"trigger"`
	}
	e.ok(t, "GET", "/v2/deployments/"+string(l.deployment.ID), "", &d)
	if d.ID != string(l.deployment.ID) {
		t.Errorf("id = %q, want %q", d.ID, l.deployment.ID)
	}
	if d.PlanID != string(l.plan.ID) {
		t.Errorf("plan id = %q, want %q", d.PlanID, l.plan.ID)
	}
	if d.Trigger.Type != string(deployment.TriggerAPI) {
		t.Errorf("trigger = %q, want api", d.Trigger.Type)
	}

	var p struct {
		ID         string `json:"id"`
		Hash       string `json:"hash"`
		Strategy   string `json:"strategy"`
		Operations []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Changes []struct {
				Path string `json:"path"`
				To   string `json:"to"`
			} `json:"changes"`
		} `json:"operations"`
		Policy struct {
			Allowed      bool `json:"allowed"`
			Requirements []struct {
				Type string `json:"type"`
				Role string `json:"role"`
			} `json:"requirements"`
			Risk struct {
				Level   string `json:"level"`
				Factors []struct {
					Code string `json:"code"`
				} `json:"factors"`
			} `json:"risk"`
		} `json:"policy"`
	}
	e.ok(t, "GET", "/v2/deployment-plans/"+string(l.plan.ID), "", &p)
	if p.ID != string(l.plan.ID) {
		t.Errorf("plan id = %q, want %q", p.ID, l.plan.ID)
	}
	if p.Hash == "" {
		t.Error("the plan came back without the hash an approval is bound to")
	}
	if len(p.Operations) != 1 || len(p.Operations[0].Changes) != 1 {
		t.Fatalf("operations = %v", p.Operations)
	}
	if p.Operations[0].Changes[0].Path != "spec.template.spec.containers[0].image" {
		t.Errorf("change path = %q", p.Operations[0].Changes[0].Path)
	}
	if !p.Policy.Allowed || p.Policy.Risk.Level != string(policy.RiskLow) {
		t.Errorf("policy = %+v", p.Policy)
	}
	// An outstanding requirement is a gate somebody has to answer, so it has
	// to survive the wire: a plan that read as unconditionally allowed would
	// have an approver looking for a question that is not there.
	if len(p.Policy.Requirements) != 1 || p.Policy.Requirements[0].Role != "release-manager" {
		t.Errorf("requirements = %v", p.Policy.Requirements)
	}
	if len(p.Policy.Risk.Factors) != 1 || p.Policy.Risk.Factors[0].Code != "blast_radius" {
		t.Errorf("risk factors = %v", p.Policy.Risk.Factors)
	}

	var timeline struct {
		Events []struct {
			Type        string          `json:"type"`
			AggregateID string          `json:"aggregate_id"`
			Payload     json.RawMessage `json:"payload"`
		} `json:"events"`
	}
	e.ok(t, "GET", "/v2/deployments/"+string(l.deployment.ID)+"/events?page_size=10", "", &timeline)
	if len(timeline.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(timeline.Events))
	}
	if timeline.Events[0].Type != string(event.DeploymentQueued) {
		t.Errorf("event = %q, want %q", timeline.Events[0].Type, event.DeploymentQueued)
	}
}

// Applying a plan answers 202 rather than 200: the deployment is recorded and
// nothing has been applied yet, which is precisely what Accepted means.
func TestApplyingAPlanIsAcceptedRatherThanCompleted(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)
	e.deployer.admitted = &l.deployment

	w := send(t, e.h, "POST", "/v2/deployment-plans/"+string(l.plan.ID)+":apply",
		`{"detail":"released by hand"}`)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body.String())
	}
	var got idOnly
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body: %v", err)
	}
	if got.ID != string(l.deployment.ID) {
		t.Errorf("deployment = %q, want %q", got.ID, l.deployment.ID)
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

// Verifying is the one command that does not answer with a deployment alone:
// the verdict and where it left the deployment are both wanted, so both are in
// the body.
func TestVerifyingAnswersWithTheVerdictBesideTheDeployment(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)
	e.deployer.verdict = &deploy.Verification{ID: l.run.ID}

	var got struct {
		Deployment struct {
			ID string `json:"id"`
		} `json:"deployment"`
		Run struct {
			ID           string `json:"id"`
			DeploymentID string `json:"deployment_id"`
			Verdict      string `json:"verdict"`
			Checks       []struct {
				Verifier struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"verifier"`
				Verdict      string `json:"verdict"`
				Reason       string `json:"reason"`
				Measurements []struct {
					Name  string  `json:"name"`
					Value float64 `json:"value"`
				} `json:"measurements"`
				Evidence []struct {
					Kind string `json:"kind"`
					URI  string `json:"uri"`
				} `json:"evidence"`
			} `json:"checks"`
		} `json:"run"`
	}
	e.ok(t, "POST", "/v2/deployments/"+string(l.deployment.ID)+":verify", "", &got)

	if got.Deployment.ID != string(l.deployment.ID) {
		t.Errorf("deployment = %q, want %q", got.Deployment.ID, l.deployment.ID)
	}
	if got.Run.ID != string(l.run.ID) {
		t.Errorf("run = %q, want %q", got.Run.ID, l.run.ID)
	}
	// The verdict is derived from the checks rather than stored beside them
	// (§11.5), so the one on the wire has to agree with the one they imply.
	if got.Run.Verdict != string(verifyv1.VerdictPass) {
		t.Errorf("verdict = %q, want pass", got.Run.Verdict)
	}
	if len(got.Run.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(got.Run.Checks))
	}
	c := got.Run.Checks[0]
	if c.Verifier.Name != "error-rate" || c.Reason == "" {
		t.Errorf("check = %+v", c)
	}
	if len(c.Measurements) != 1 || c.Measurements[0].Value != 0.004 {
		t.Errorf("measurements = %v", c.Measurements)
	}
	if len(c.Evidence) != 1 || c.Evidence[0].URI == "" {
		t.Errorf("evidence = %v", c.Evidence)
	}
}

// A verification run outlives the call that produced it, so it is readable on
// its own rather than only in the answer to :verify — a caller that lost that
// response, or that never made the call, has no other way back to the verdict.
// INV-006 is what makes this worth a test of its own: the service has always
// answered GetVerificationRun and gRPC has always served it, so a run reachable
// over one transport and not the other is the gap the invariant forbids.
func TestAVerificationRunIsReadableAtItsOwnPath(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	l := e.landed(t, projectID, envID)

	var got struct {
		ID           string `json:"id"`
		DeploymentID string `json:"deployment_id"`
		Verdict      string `json:"verdict"`
		Checks       []struct {
			Verifier struct {
				Name string `json:"name"`
			} `json:"verifier"`
			Verdict string `json:"verdict"`
		} `json:"checks"`
	}
	e.ok(t, "GET", "/v2/verification-runs/"+string(l.run.ID), "", &got)

	if got.ID != string(l.run.ID) {
		t.Errorf("id = %q, want %q", got.ID, l.run.ID)
	}
	if got.DeploymentID != string(l.deployment.ID) {
		t.Errorf("deployment = %q, want %q", got.DeploymentID, l.deployment.ID)
	}
	if got.Verdict != string(verifyv1.VerdictPass) {
		t.Errorf("verdict = %q, want pass", got.Verdict)
	}
	if len(got.Checks) != 1 || got.Checks[0].Verifier.Name != "error-rate" {
		t.Errorf("checks = %+v", got.Checks)
	}
}

// Every collection this API serves answers a read, and a page size it cannot
// read is the caller's mistake rather than a silent fall back to the default.
func TestEachCollectionIsListedAtItsOwnPath(t *testing.T) {
	e := served(t)
	projectID := e.project(t)
	envID := e.environment(t, projectID)
	e.landed(t, projectID, envID)

	for _, tc := range []struct {
		name, target, key string
	}{
		{"projects", "/v2/projects", "projects"},
		{"environments", "/v2/projects/" + projectID + "/environments", "environments"},
		{"artifacts", "/v2/projects/" + projectID + "/artifacts", "artifacts"},
		{"releases", "/v2/projects/" + projectID + "/releases", "releases"},
		{"deployments", "/v2/environments/" + envID + "/deployments", "deployments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string][]idOnly
			e.ok(t, "GET", tc.target, "", &got)
			if len(got[tc.key]) == 0 {
				t.Fatalf("%s came back empty: %v", tc.key, got)
			}
			if got[tc.key][0].ID == "" {
				t.Errorf("a listed resource came back without an id")
			}

			w := send(t, e.h, "GET", tc.target+"?page_size=fifty", "")
			if w.Code != http.StatusBadRequest {
				t.Errorf("an unreadable page size = %d, want 400", w.Code)
			}
		})
	}
}

// An artifact is readable at its own path as well as under the project that
// registered it, because INV-002 makes it a thing in its own right.
func TestAnArtifactIsReadableAtItsOwnPath(t *testing.T) {
	e := served(t)
	projectID := e.project(t)

	sha := "sha256:" + strings.Repeat("e", 64)
	var a idOnly
	e.ok(t, "POST", "/v2/projects/"+projectID+"/artifacts", `{
		"kind": "oci-image",
		"digest": "`+sha+`",
		"locator": "registry.example.com/checkout@`+sha+`"
	}`, &a)

	var got struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Digest  string `json:"digest"`
		Locator string `json:"locator"`
	}
	e.ok(t, "GET", "/v2/artifacts/"+a.ID, "", &got)
	if got.ID != a.ID || got.Digest != sha {
		t.Errorf("artifact = %+v, want %q at %q", got, sha, a.ID)
	}
}

// Only the commands this API serves reach the deployer. An unknown verb on a
// path that does exist must not be read as an id, and a GET must not be read as
// a command.
func TestOnlyTheServedCommandsReachTheDeployer(t *testing.T) {
	for _, tc := range []struct{ name, method, target string }{
		{"an unknown verb", "POST", "/v2/deployments/dpl_1:detonate"},
		{"a command sent as a read", "GET", "/v2/deployments/dpl_1:cancel"},
		{"a plan verb on the wrong collection", "POST", "/v2/environments/env_1/releases:plan"},
		{"an apply verb on a deployment", "POST", "/v2/deployments/dpl_1:apply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := served(t)

			w := send(t, e.h, tc.method, tc.target, "")

			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", w.Code)
			}
			if e.deployer.cancel.DeploymentID != "" || e.deployer.plan.EnvironmentID != "" ||
				e.deployer.apply.PlanID != "" {
				t.Error("the deployer was reached by a route this API does not serve")
			}
		})
	}
}
