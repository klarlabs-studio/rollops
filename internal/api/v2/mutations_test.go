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
