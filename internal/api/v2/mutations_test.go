package apiv2_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// planning is the request every mutation test starts from, so that a test
// interested in one field says which by changing it.
func (s scene) planning(key string) apiv2.CreatePlanRequest {
	return apiv2.CreatePlanRequest{
		EnvironmentID:  string(s.environment.ID),
		ReleaseID:      string(s.release.ID),
		Strategy:       string(deployment.StrategyRolling),
		Actor:          planner,
		IdempotencyKey: key,
	}
}

// proposes is what the target would answer when asked to plan.
func (s scene) proposes(changes ...plan.Change) {
	s.proposals.proposal = deploy.Proposal{Operations: oneApply(changes...)}
}

func TestPlanningSaysWhatDeployingTheReleaseWouldChange(t *testing.T) {
	s := setup(t).scene(t)
	s.proposes(plan.Change{Path: "spec.replicas", From: "2", To: "4"})

	got, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no plan id; the caller cannot apply or even re-read what they just asked for")
	}
	if got.EnvironmentID != string(s.environment.ID) || got.ReleaseID != string(s.release.ID) {
		t.Errorf("plan = release %q into %q", got.ReleaseID, got.EnvironmentID)
	}
	if got.Strategy != string(deployment.StrategyRolling) {
		t.Errorf("strategy = %q", got.Strategy)
	}
	if len(got.Operations) != 1 || len(got.Operations[0].Changes) != 1 {
		t.Fatalf("operations = %+v", got.Operations)
	}
	if got.Operations[0].Changes[0].To != "4" {
		t.Errorf("change = %+v", got.Operations[0].Changes[0])
	}
	if got.Hash == "" {
		t.Error("no hash; an approval would have nothing to bind to")
	}
}

// The plan is stored, not just computed and handed back: applying it is a
// separate call that reads it by id, so a plan that never landed would be a
// plan nobody could apply.
func TestAPlanThatWasJustCreatedReadsBackByItsID(t *testing.T) {
	s := setup(t).scene(t)

	created, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	got, err := s.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: created.ID})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Hash != created.Hash {
		t.Errorf("hash = %q, want the %q that was returned", got.Hash, created.Hash)
	}
}

// Redaction belongs to the write path as much as to the read path. A caller
// who plans and renders the answer never calls GetPlan, so a value withheld
// only there would leave by this door instead (INV-012).
func TestAPlanReturnedFromCreatingItHidesWhatReadingItHides(t *testing.T) {
	s := setup(t).scene(t)
	s.proposes(
		plan.Change{Path: "spec.replicas", From: "2", To: "4"},
		plan.Change{Path: "env.DATABASE_PASSWORD", From: "hunter2", To: "correct-horse", Sensitive: true},
	)

	got, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	var secret apiv2.Change
	for _, c := range got.Operations[0].Changes {
		if c.Path == "env.DATABASE_PASSWORD" {
			secret = c
		}
	}
	if !secret.Sensitive {
		t.Fatal("the sensitive change is not marked")
	}
	if secret.From != "" || secret.To != "" {
		t.Errorf("sensitive change = %q -> %q, want both blank", secret.From, secret.To)
	}
}

func TestAMalformedPlanRequestIsTheCallersMistake(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*apiv2.CreatePlanRequest)
	}{
		{"environment id", func(r *apiv2.CreatePlanRequest) { r.EnvironmentID = "nonsense" }},
		{"release id of the wrong kind", func(r *apiv2.CreatePlanRequest) {
			r.ReleaseID = "env_0199a0dd-0000-7000-8000-000000000000"
		}},
		// Empty is refused rather than defaulted: the strategy is what the
		// operations are built for, and guessing it would have an approver
		// review a rollout nobody asked for.
		{"no strategy", func(r *apiv2.CreatePlanRequest) { r.Strategy = "" }},
		{"unknown strategy", func(r *apiv2.CreatePlanRequest) { r.Strategy = "yolo" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t).scene(t)
			req := s.planning("")
			tc.spoil(&req)

			_, err := s.svc.CreatePlan(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
			if s.proposals.calls != 0 {
				t.Error("the target was asked to plan for a request that never parsed")
			}
		})
	}
}

// A refusal from the engine keeps the code it was refused with. What is
// asserted is the wiring — that CreatePlan wraps rather than reclassifies —
// because a refusal that arrives as INTERNAL is one the caller retries.
func TestAnEnvironmentWithNothingToDeployToCannotBePlannedFor(t *testing.T) {
	s := setup(t).scene(t)
	stored, err := s.store.Environments().Get(context.Background(), s.environment.ID)
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	stored.Targets = nil
	if _, err := s.store.Environments().Update(context.Background(), stored); err != nil {
		t.Fatalf("Environments.Update: %v", err)
	}

	_, err = s.svc.CreatePlan(context.Background(), s.planning(""))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestPlanningAReleaseNobodyCutIsNotFound(t *testing.T) {
	s := setup(t).scene(t)
	req := s.planning("")
	req.ReleaseID = "rel_0199a0dd-0000-7000-8000-000000000009"

	_, err := s.svc.CreatePlan(context.Background(), req)

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestPlanningAReleaseFromAnotherProjectIsTheCallersMistake(t *testing.T) {
	s := setup(t).scene(t)
	other := s.project(t, "billing")
	theirs := s.world.release(t, other.ID, "1.0.0", map[string]identity.ArtifactID{
		"app": s.artifact(t, other.ID, "billing").ID,
	})
	req := s.planning("")
	req.ReleaseID = string(theirs.ID)

	_, err := s.svc.CreatePlan(context.Background(), req)

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

// This is the whole point of the key. A client whose first request timed out
// retries, and gets the plan the first call made rather than a second one
// built against a world that has since moved.
func TestTwoCallsWithOneKeyPlanOnce(t *testing.T) {
	s := setup(t).scene(t)

	first, err := s.svc.CreatePlan(context.Background(), s.planning("k-1"))
	if err != nil {
		t.Fatalf("first CreatePlan: %v", err)
	}
	second, err := s.svc.CreatePlan(context.Background(), s.planning("k-1"))
	if err != nil {
		t.Fatalf("second CreatePlan: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("plan = %q on the retry and %q on the first call", second.ID, first.ID)
	}
	if s.proposals.calls != 1 {
		t.Errorf("the target was asked to plan %d times, want 1", s.proposals.calls)
	}
}

// §18.3 requires a key to be supported, not supplied. The protection is for a
// caller that retries, and a caller that sent no key has not asked for it.
func TestWithoutAKeyEveryCallPlansAgain(t *testing.T) {
	s := setup(t).scene(t)

	first, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("first CreatePlan: %v", err)
	}
	second, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("second CreatePlan: %v", err)
	}

	if second.ID == first.ID {
		t.Error("two unkeyed calls returned one plan")
	}
	if s.proposals.calls != 2 {
		t.Errorf("the target was asked to plan %d times, want 2", s.proposals.calls)
	}
}

// A key reused for a different request is not a retry. Replaying the first
// answer would tell the caller their second request succeeded, and they would
// go on to apply a plan for a rollout they did not ask for.
func TestAKeyReusedForADifferentRequestIsRefused(t *testing.T) {
	s := setup(t).scene(t)
	if _, err := s.svc.CreatePlan(context.Background(), s.planning("k-1")); err != nil {
		t.Fatalf("first CreatePlan: %v", err)
	}
	changed := s.planning("k-1")
	changed.Strategy = string(deployment.StrategyCanary)

	_, err := s.svc.CreatePlan(context.Background(), changed)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
	if s.proposals.calls != 1 {
		t.Errorf("the target planned %d times; the second request ran under the first one's key", s.proposals.calls)
	}
}

// The actor is part of the request a key stands for. Two principals that
// happen to generate the same key must not replay each other's answers —
// policy ruled on one of them, not the other.
func TestOneKeyMeansDifferentThingsToDifferentActors(t *testing.T) {
	s := setup(t).scene(t)
	if _, err := s.svc.CreatePlan(context.Background(), s.planning("k-1")); err != nil {
		t.Fatalf("first CreatePlan: %v", err)
	}
	somebodyElse := s.planning("k-1")
	somebodyElse.Actor = identity.Principal{ID: "bob@example.com", Type: identity.PrincipalHuman}

	_, err := s.svc.CreatePlan(context.Background(), somebodyElse)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// A key past its window is refused rather than honoured. The record is kept so
// the refusal can be given at all: forgetting it would let a very late retry
// plan a second time against a world that has moved on.
func TestAKeyPastItsWindowIsRefusedRatherThanReplayed(t *testing.T) {
	s := setup(t).scene(t)
	if _, err := s.svc.CreatePlan(context.Background(), s.planning("k-1")); err != nil {
		t.Fatalf("first CreatePlan: %v", err)
	}
	s.clock.now = at.Add(apiv2.DefaultKeyLifetime + time.Second)

	_, err := s.svc.CreatePlan(context.Background(), s.planning("k-1"))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// The commonest retry is the one after a failure. A claim held past a call
// that answered nothing would turn every transient refusal into a permanent
// conflict, and the caller would have to invent a new key to make progress.
func TestAKeyWhoseCallFailedCanBeUsedAgain(t *testing.T) {
	s := setup(t).scene(t)
	s.proposals.err = errLeak

	if _, err := s.svc.CreatePlan(context.Background(), s.planning("k-1")); err == nil {
		t.Fatal("the planner failed and CreatePlan did not")
	}
	s.proposals.err = nil

	got, err := s.svc.CreatePlan(context.Background(), s.planning("k-1"))
	if err != nil {
		t.Fatalf("CreatePlan after a failure: %v", err)
	}
	if got.ID == "" {
		t.Error("no plan; the key was still held by the call that answered nothing")
	}
}

// racingKeys claims the key itself just before the service's own claim lands,
// which is the race two concurrent retries are in. The loser is told the key
// is taken and has to answer from what the winner recorded.
type racingKeys struct {
	port.IdempotencyRepository

	// winner is what the rival recorded, or empty to leave its claim in
	// flight — a rival whose own call has not returned yet.
	winner string
	raced  bool
}

func (r *racingKeys) Create(ctx context.Context, rec port.IdempotencyRecord) error {
	if r.raced {
		return r.IdempotencyRepository.Create(ctx, rec)
	}
	r.raced = true
	if err := r.IdempotencyRepository.Create(ctx, rec); err != nil {
		return err
	}
	if r.winner != "" {
		if err := r.Complete(ctx, rec.Operation, rec.Key, r.winner); err != nil {
			return err
		}
	}
	return fmt.Errorf("idempotency key %s/%s: %w", rec.Operation, rec.Key, port.ErrAlreadyExists)
}

func TestARetryThatLosesTheRaceAnswersFromTheWinnersPlan(t *testing.T) {
	w := setup(t)
	s := w.scene(t)
	won, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	svc := serviceWithKeys(t, w, &racingKeys{IdempotencyRepository: w.store.Idempotency(), winner: won.ID})

	got, err := svc.CreatePlan(context.Background(), s.planning("k-1"))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if got.ID != won.ID {
		t.Errorf("plan = %q, want the winner's %q", got.ID, won.ID)
	}
	if s.proposals.calls != 1 {
		t.Errorf("the target planned %d times, want the winner's 1", s.proposals.calls)
	}
}

// A claim with no result yet is a call still in flight. §9.4 allows a typed
// conflict where replay cannot be guaranteed, and there is nothing to replay
// until the first call returns.
func TestARetryThatArrivesBeforeTheFirstCallReturnsIsRefused(t *testing.T) {
	w := setup(t)
	s := w.scene(t)
	svc := serviceWithKeys(t, w, &racingKeys{IdempotencyRepository: w.store.Idempotency()})

	_, err := svc.CreatePlan(context.Background(), s.planning("k-1"))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// brokenKeys fails one call of the idempotency repository, so the branches
// that report a storage failure are reached without breaking a real one.
type brokenKeys struct {
	port.IdempotencyRepository
	get, create, complete error
}

func (b brokenKeys) Get(ctx context.Context, op, key string) (port.IdempotencyRecord, error) {
	if b.get != nil {
		return port.IdempotencyRecord{}, b.get
	}
	return b.IdempotencyRepository.Get(ctx, op, key)
}

func (b brokenKeys) Create(ctx context.Context, rec port.IdempotencyRecord) error {
	if b.create != nil {
		return b.create
	}
	return b.IdempotencyRepository.Create(ctx, rec)
}

func (b brokenKeys) Complete(ctx context.Context, op, key, result string) error {
	if b.complete != nil {
		return b.complete
	}
	return b.IdempotencyRepository.Complete(ctx, op, key, result)
}

// A key store that cannot answer is our failure, not the caller's. It arrives
// as INTERNAL rather than as CONFLICT so that a client does not read "your
// request disagrees with the world" and set about rewriting a request that was
// fine.
func TestAKeyStoreThatCannotAnswerIsOurFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys func(port.IdempotencyRepository) port.IdempotencyRepository
	}{
		{"reading the key", func(r port.IdempotencyRepository) port.IdempotencyRepository {
			return brokenKeys{IdempotencyRepository: r, get: errLeak}
		}},
		{"claiming the key", func(r port.IdempotencyRepository) port.IdempotencyRepository {
			return brokenKeys{IdempotencyRepository: r, create: errLeak}
		}},
		{"recording the answer", func(r port.IdempotencyRepository) port.IdempotencyRepository {
			return brokenKeys{IdempotencyRepository: r, complete: errLeak}
		}},
		// The claim was refused as taken but nothing was written, so the read
		// that should find the winner's record finds nothing instead. The
		// caller cannot act on that either.
		{"reading a claim that is not there", func(r port.IdempotencyRepository) port.IdempotencyRepository {
			return brokenKeys{IdempotencyRepository: r, create: port.ErrAlreadyExists}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := setup(t)
			s := w.scene(t)
			svc := serviceWithKeys(t, w, tc.keys(w.store.Idempotency()))

			_, err := svc.CreatePlan(context.Background(), s.planning("k-1"))

			if got := codeOf(t, err); got != apierr.Internal {
				t.Fatalf("code = %s, want %s", got, apierr.Internal)
			}
			var e *apierr.Error
			if errors.As(err, &e) && containsWord(e.Message, "10.0.0.7") {
				t.Errorf("message %q hands the caller the estate's network layout", e.Message)
			}
		})
	}
}

// A lifetime nobody configured is the default rather than zero. Zero would
// expire every key the instant it was written, which is the same as having no
// keys at all while looking like one working.
func TestAKeyLifetimeNobodyConfiguredIsTheDefault(t *testing.T) {
	w := setup(t)
	s := w.scene(t)

	if _, err := s.svc.CreatePlan(context.Background(), s.planning("k-1")); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	rec, err := w.store.Idempotency().Get(context.Background(), "CreatePlan", "k-1")
	if err != nil {
		t.Fatalf("Idempotency.Get: %v", err)
	}
	if want := at.Add(apiv2.DefaultKeyLifetime); !rec.ExpiresAt.Equal(want) {
		t.Errorf("expires at = %s, want %s", rec.ExpiresAt, want)
	}
}

func TestAConfiguredKeyLifetimeIsTheOneRecorded(t *testing.T) {
	w := setup(t)
	s := w.scene(t)
	cfg := config(w.store, w.clock, w.deployer)
	cfg.KeyLifetime = time.Hour
	svc, err := apiv2.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := svc.CreatePlan(context.Background(), s.planning("k-1")); err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	rec, err := w.store.Idempotency().Get(context.Background(), "CreatePlan", "k-1")
	if err != nil {
		t.Fatalf("Idempotency.Get: %v", err)
	}
	if want := at.Add(time.Hour); !rec.ExpiresAt.Equal(want) {
		t.Errorf("expires at = %s, want %s", rec.ExpiresAt, want)
	}
}

// applying is the request every apply test starts from.
func (s scene) applying(planID, key string) apiv2.ApplyPlanRequest {
	return apiv2.ApplyPlanRequest{
		PlanID:         planID,
		Detail:         "CHG-4471",
		Actor:          planner,
		IdempotencyKey: key,
	}
}

// planned creates a plan through the service and returns its id, so that an
// apply test starts from a plan the API itself would hand a caller.
func (s scene) planned(t *testing.T) string {
	t.Helper()
	p, err := s.svc.CreatePlan(context.Background(), s.planning(""))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	return p.ID
}

// drain cancels the deployment so the environment is free again. Tests about
// the idempotency key use it so that a second apply is refused by the key
// rather than by an environment that is merely busy — two different refusals
// that both arrive as CONFLICT.
func (s scene) drain(t *testing.T, id string) {
	t.Helper()
	parsed, err := identity.ParseDeploymentID(id)
	if err != nil {
		t.Fatalf("ParseDeploymentID: %v", err)
	}
	d, err := s.store.Deployments().Get(context.Background(), parsed)
	if err != nil {
		t.Fatalf("Deployments.Get: %v", err)
	}
	moved, err := d.TransitionTo(deployment.StatusCancelled, s.clock.now)
	if err != nil {
		t.Fatalf("TransitionTo(cancelled): %v", err)
	}
	if _, err := s.store.Deployments().Update(context.Background(), moved); err != nil {
		t.Fatalf("Deployments.Update: %v", err)
	}
}

func (s scene) deployments(t *testing.T) []apiv2.Deployment {
	t.Helper()
	got, err := s.svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{
		EnvironmentID: string(s.environment.ID),
	})
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	return got.Deployments
}

func TestApplyingAPlanAdmitsADeploymentForIt(t *testing.T) {
	s := setup(t).scene(t)
	planID := s.planned(t)

	got, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, ""))
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no deployment id; the caller cannot follow what they just started")
	}
	if got.PlanID != planID {
		t.Errorf("plan id = %q, want %q", got.PlanID, planID)
	}
	if got.ReleaseID != string(s.release.ID) || got.EnvironmentID != string(s.environment.ID) {
		t.Errorf("deployment = release %q into %q", got.ReleaseID, got.EnvironmentID)
	}
	if got.Status != string(deployment.StatusQueued) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusQueued)
	}
	// The strategy comes from the plan rather than from the request, so what
	// runs is what was reviewed.
	if got.Strategy != string(deployment.StrategyRolling) {
		t.Errorf("strategy = %q", got.Strategy)
	}
}

// The trigger type is not the caller's to choose. Everything arriving here came
// through the API, and a request that could claim "git" or "schedule" would
// write a cause into the record that nothing else corroborates.
func TestADeploymentAdmittedThroughTheAPISaysSoAndCarriesTheCallersNote(t *testing.T) {
	s := setup(t).scene(t)

	got, err := s.svc.ApplyPlan(context.Background(), s.applying(s.planned(t), ""))
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if got.Trigger.Type != string(deployment.TriggerAPI) {
		t.Errorf("trigger type = %q, want %q", got.Trigger.Type, deployment.TriggerAPI)
	}
	if got.Trigger.Detail != "CHG-4471" {
		t.Errorf("trigger detail = %q, want the caller's change reference", got.Trigger.Detail)
	}
}

func TestAMalformedApplyRequestIsTheCallersMistake(t *testing.T) {
	for _, tc := range []struct {
		name   string
		planID string
	}{
		{"not an id at all", "nonsense"},
		{"an id of the wrong kind", "dep_0199a0dd-0000-7000-8000-000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t).scene(t)

			_, err := s.svc.ApplyPlan(context.Background(), s.applying(tc.planID, ""))

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

func TestApplyingAPlanNobodyComputedIsNotFound(t *testing.T) {
	absent := setup(t).scene(t).plan(t, oneApply(), allowed())
	s := setup(t).scene(t)

	_, err := s.svc.ApplyPlan(context.Background(), s.applying(string(absent.ID), ""))

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

// One environment runs one deployment at a time. The second caller is told the
// world disagrees with them rather than being queued behind the first, because
// what they would be queued for is a plan built against a world the first
// deployment is in the middle of changing.
func TestAnEnvironmentAlreadyDeployingWillNotTakeAnother(t *testing.T) {
	s := setup(t).scene(t)
	planID := s.planned(t)
	if _, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, "")); err != nil {
		t.Fatalf("first ApplyPlan: %v", err)
	}

	_, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, ""))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// A refusal is not a gate. POLICY_DENIED tells the caller to stop, where
// APPROVAL_REQUIRED would send them looking for someone to sign it off.
func TestAPlanPolicyRefusedCannotBeApplied(t *testing.T) {
	s := setup(t).scene(t)
	refused := s.plan(t, oneApply(), policy.Decision{
		Allowed: false,
		Reasons: []policy.Reason{{Code: "change_freeze", Message: "a change freeze is in force"}},
		Risk: policy.RiskAssessment{
			Level:   policy.RiskHigh,
			Score:   0.9,
			Factors: []policy.RiskFactor{{Code: "change_freeze", Message: "a change freeze is in force"}},
		},
	})

	_, err := s.svc.ApplyPlan(context.Background(), s.applying(string(refused.ID), ""))

	if got := codeOf(t, err); got != apierr.PolicyDenied {
		t.Errorf("code = %s, want %s", got, apierr.PolicyDenied)
	}
}

// Planning separately from applying is only safe because the plan goes stale.
// PLAN_STALE rather than CONFLICT because the remedy is a specific one: plan
// again, and read what it says this time.
func TestAPlanPastItsWindowCannotBeApplied(t *testing.T) {
	s := setup(t).scene(t)
	planID := s.planned(t)
	s.clock.now = at.Add(time.Hour + time.Second)

	_, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, ""))

	if got := codeOf(t, err); got != apierr.PlanStale {
		t.Errorf("code = %s, want %s", got, apierr.PlanStale)
	}
}

// The environment is drained first so that the retry could have succeeded. A
// busy environment would refuse the second call with a CONFLICT of its own, and
// the test would pass without the key doing anything.
func TestTwoApplyCallsWithOneKeyAdmitOneDeployment(t *testing.T) {
	s := setup(t).scene(t)
	planID := s.planned(t)
	first, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, "k-1"))
	if err != nil {
		t.Fatalf("first ApplyPlan: %v", err)
	}
	s.drain(t, first.ID)

	second, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, "k-1"))
	if err != nil {
		t.Fatalf("second ApplyPlan: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("deployment = %q on the retry and %q on the first call", second.ID, first.ID)
	}
	if got := s.deployments(t); len(got) != 1 {
		t.Errorf("got %d deployments, want the 1 the retry should have replayed", len(got))
	}
}

// The note the caller wrote is part of the request the key stands for. Replaying
// the first answer would record one change reference against a deployment
// somebody started for another.
func TestAnApplyKeyReusedForADifferentNoteIsRefused(t *testing.T) {
	s := setup(t).scene(t)
	planID := s.planned(t)
	first, err := s.svc.ApplyPlan(context.Background(), s.applying(planID, "k-1"))
	if err != nil {
		t.Fatalf("first ApplyPlan: %v", err)
	}
	s.drain(t, first.ID)
	changed := s.applying(planID, "k-1")
	changed.Detail = "CHG-9999"

	_, err = s.svc.ApplyPlan(context.Background(), changed)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
	if got := s.deployments(t); len(got) != 1 {
		t.Errorf("got %d deployments; the second request ran under the first one's key", len(got))
	}
}

// gated applies a plan whose policy leaves a requirement outstanding, so an
// approval test starts from a deployment actually waiting at the gate.
func (s scene) gated(t *testing.T) (plan.DeploymentPlan, apiv2.Deployment) {
	t.Helper()
	p := s.plan(t, oneApply(), needsApproval())
	d, err := s.svc.ApplyPlan(context.Background(), s.applying(string(p.ID), ""))
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if d.Status != string(deployment.StatusAwaitingApproval) {
		t.Fatalf("status = %q, want %q", d.Status, deployment.StatusAwaitingApproval)
	}
	return p, d
}

func (s scene) approving(d apiv2.Deployment, hash, key string) apiv2.ApproveDeploymentRequest {
	return apiv2.ApproveDeploymentRequest{
		DeploymentID:   d.ID,
		PlanHash:       hash,
		Granted:        true,
		Reason:         "read the diff, the replica bump is expected",
		Actor:          approver,
		IdempotencyKey: key,
	}
}

// A gate is something to pass, not an error. Applying a plan with an unmet
// requirement records the deployment and returns it waiting, which is how an
// approver finds out there is anything to answer.
func TestAPlanWithAnUnmetRequirementIsAdmittedToTheGateRatherThanRefused(t *testing.T) {
	s := setup(t).scene(t)

	_, d := s.gated(t)

	if d.ID == "" {
		t.Error("no deployment id; nobody could be sent a link to approve")
	}
}

func TestGrantingTheLastApprovalQueuesTheDeployment(t *testing.T) {
	s := setup(t).scene(t)
	p, d := s.gated(t)

	got, err := s.svc.ApproveDeployment(context.Background(), s.approving(d, p.Hash.String(), ""))
	if err != nil {
		t.Fatalf("ApproveDeployment: %v", err)
	}
	if got.ID != d.ID {
		t.Errorf("deployment = %q, want the %q that was approved", got.ID, d.ID)
	}
	if got.Status != string(deployment.StatusQueued) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusQueued)
	}
}

// A refusal is an answer, not a pause. Leaving the deployment at the gate would
// wait for somebody to overrule the person who said no.
func TestDenyingCancelsTheDeploymentRatherThanLeavingItWaiting(t *testing.T) {
	s := setup(t).scene(t)
	p, d := s.gated(t)
	req := s.approving(d, p.Hash.String(), "")
	req.Granted = false
	req.Reason = "the migration has not been rehearsed"

	got, err := s.svc.ApproveDeployment(context.Background(), req)
	if err != nil {
		t.Fatalf("ApproveDeployment: %v", err)
	}
	if got.Status != string(deployment.StatusCancelled) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusCancelled)
	}
}

// §13 binds consent to a revision. An approval of a hash that is not the stored
// plan's is consent for something else, and honouring it would carry a review
// of one rollout over to another.
func TestApprovingAgainstAHashThatIsNotThePlansIsStale(t *testing.T) {
	s := setup(t).scene(t)
	_, d := s.gated(t)
	other := s.plan(t, oneApply(plan.Change{Path: "spec.replicas", From: "2", To: "9"}), needsApproval())

	_, err := s.svc.ApproveDeployment(context.Background(), s.approving(d, other.Hash.String(), ""))

	if got := codeOf(t, err); got != apierr.PlanStale {
		t.Errorf("code = %s, want %s", got, apierr.PlanStale)
	}
}

func TestAMalformedApprovalIsTheCallersMistake(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*apiv2.ApproveDeploymentRequest)
	}{
		{"deployment id of the wrong kind", func(r *apiv2.ApproveDeploymentRequest) {
			r.DeploymentID = "pln_0199a0dd-0000-7000-8000-000000000000"
		}},
		{"a hash that is not a digest", func(r *apiv2.ApproveDeploymentRequest) { r.PlanHash = "deadbeef" }},
		// Required, not defaulted: an approval that stated no revision would be
		// bound to whatever happens to be stored when it is spent.
		{"no hash at all", func(r *apiv2.ApproveDeploymentRequest) { r.PlanHash = "" }},
		// The denial half of the same rule: whoever is stopped by a refusal has
		// to be told what stopped them.
		{"a denial with no reason", func(r *apiv2.ApproveDeploymentRequest) {
			r.Granted = false
			r.Reason = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t).scene(t)
			p, d := s.gated(t)
			req := s.approving(d, p.Hash.String(), "")
			tc.spoil(&req)

			_, err := s.svc.ApproveDeployment(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

// Past the gate the work has begun, and recording consent for it after the fact
// would describe a review that never happened.
func TestApprovingADeploymentThatIsNotAtTheGateIsRefused(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	d, err := s.svc.ApplyPlan(context.Background(), s.applying(string(p.ID), ""))
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}

	_, err = s.svc.ApproveDeployment(context.Background(), s.approving(d, p.Hash.String(), ""))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestApprovingADeploymentNobodyStartedIsNotFound(t *testing.T) {
	absent := setup(t).scene(t)
	p, d := absent.gated(t)
	s := setup(t).scene(t)

	_, err := s.svc.ApproveDeployment(context.Background(), s.approving(d, p.Hash.String(), ""))

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

// The retry matters more here than anywhere else: the first call has already
// moved the deployment off the gate, so without the key the retry would be told
// the deployment is not awaiting approval and an approver would think their
// answer was lost.
func TestTwoApprovalsWithOneKeyRecordOneAnswer(t *testing.T) {
	s := setup(t).scene(t)
	p, d := s.gated(t)

	first, err := s.svc.ApproveDeployment(context.Background(), s.approving(d, p.Hash.String(), "k-1"))
	if err != nil {
		t.Fatalf("first ApproveDeployment: %v", err)
	}
	second, err := s.svc.ApproveDeployment(context.Background(), s.approving(d, p.Hash.String(), "k-1"))
	if err != nil {
		t.Fatalf("second ApproveDeployment: %v", err)
	}
	if second.ID != first.ID || second.Status != first.Status {
		t.Errorf("retry = %q/%q, want the first call's %q/%q",
			second.ID, second.Status, first.ID, first.Status)
	}
}

// A key reused to say the opposite is not a retry. Replaying the grant would
// tell somebody their refusal was recorded while the deployment went ahead.
func TestAnApprovalKeyReusedForTheOppositeAnswerIsRefused(t *testing.T) {
	s := setup(t).scene(t)
	p, d := s.gated(t)
	if _, err := s.svc.ApproveDeployment(context.Background(), s.approving(d, p.Hash.String(), "k-1")); err != nil {
		t.Fatalf("first ApproveDeployment: %v", err)
	}
	reversed := s.approving(d, p.Hash.String(), "k-1")
	reversed.Granted = false
	reversed.Reason = "on second thoughts"

	_, err := s.svc.ApproveDeployment(context.Background(), reversed)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
	got, err := s.svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: d.ID})
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Status != string(deployment.StatusQueued) {
		t.Errorf("status = %q; the reversal ran under the grant's key", got.Status)
	}
}

// Each mutation has its own key space. §18.3 has a CLI generate one key per
// invocation, and a caller whose generator repeats must not have an approval
// answered with a plan.
func TestOneKeyMeansDifferentThingsToDifferentOperations(t *testing.T) {
	s := setup(t).scene(t)
	created, err := s.svc.CreatePlan(context.Background(), s.planning("k-1"))
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	got, err := s.svc.ApplyPlan(context.Background(), s.applying(created.ID, "k-1"))
	if err != nil {
		t.Fatalf("ApplyPlan: %v", err)
	}
	if got.ID == "" {
		t.Error("the apply was answered from the plan's record")
	}
}

// serviceWithKeys rebuilds the service against a different key store, keeping
// everything else the scene already arranged.
func serviceWithKeys(t *testing.T, w *world, keys port.IdempotencyRepository) *apiv2.Service {
	t.Helper()
	cfg := config(w.store, w.clock, w.deployer)
	cfg.Keys = keys
	svc, err := apiv2.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}
