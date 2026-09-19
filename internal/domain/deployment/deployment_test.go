package deployment_test

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// One generator for the package, so two deployments built by the same test get
// different identifiers.
var gen = identity.NewSequenceGenerator()

func newGen() identity.Generator { return gen }

func newClock() identity.Clock { return identity.NewFixedClock(at) }

func author() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

func draft() deployment.Deployment {
	return deployment.Deployment{
		ProjectID:     "prj_1",
		EnvironmentID: "env_1",
		ReleaseID:     "rel_1",
		PlanID:        "pln_1",
		Strategy:      deployment.StrategyRolling,
		Trigger:       deployment.Trigger{Type: deployment.TriggerManual, Detail: "ada ran deploy"},
	}
}

func newDeployment(t *testing.T) deployment.Deployment {
	t.Helper()
	d, err := deployment.New(newGen(), newClock(), author(), draft())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func TestANewDeploymentIsPlannedAndNotYetRunning(t *testing.T) {
	d := newDeployment(t)
	if d.ID == "" {
		t.Error("no id")
	}
	if d.Status != deployment.StatusPlanned {
		t.Errorf("Status = %s, want %s", d.Status, deployment.StatusPlanned)
	}
	if !d.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %s, want %s", d.CreatedAt, at)
	}
	// A deployment that has not applied anything has not started. Stamping the
	// clock at creation would make every queued deployment look like it had
	// been running for as long as it waited.
	if d.StartedAt != nil {
		t.Errorf("StartedAt = %v, want nil", d.StartedAt)
	}
	if d.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil", d.FinishedAt)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// A deployment is persisted and rendered on every surface, so a credential that
// reached it would be impossible to recall (INV-012).
func TestTheActorIsRecordedAndRedacted(t *testing.T) {
	by := author()
	by.Claims = map[string]string{"token": "s3cret"}
	d, err := deployment.New(newGen(), newClock(), by, draft())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.Actor.ID != "u1" {
		t.Errorf("Actor.ID = %q, want %q", d.Actor.ID, "u1")
	}
	if got, ok := d.Actor.Claims["token"]; ok && got == "s3cret" {
		t.Error("the actor's credential survived into the deployment")
	}
}

// A deployment that does not name what it is deploying, where, or under which
// plan cannot be executed or audited.
func TestADeploymentMustNameItsSubject(t *testing.T) {
	for name, blank := range map[string]func(*deployment.Deployment){
		"id":          func(d *deployment.Deployment) { d.ID = "" },
		"project":     func(d *deployment.Deployment) { d.ProjectID = "" },
		"environment": func(d *deployment.Deployment) { d.EnvironmentID = "" },
		"release":     func(d *deployment.Deployment) { d.ReleaseID = "" },
		"plan":        func(d *deployment.Deployment) { d.PlanID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			d := newDeployment(t)
			blank(&d)
			if err := d.Validate(); err == nil {
				t.Errorf("a deployment with no %s was accepted", name)
			}
		})
	}
}

func TestADeploymentMustCarryAKnownStrategyTriggerAndStatus(t *testing.T) {
	for name, mutate := range map[string]func(*deployment.Deployment){
		"strategy": func(d *deployment.Deployment) { d.Strategy = "yolo" },
		"trigger":  func(d *deployment.Deployment) { d.Trigger.Type = "vibes" },
		"status":   func(d *deployment.Deployment) { d.Status = "deploying" },
	} {
		t.Run(name, func(t *testing.T) {
			d := newDeployment(t)
			mutate(&d)
			if err := d.Validate(); err == nil {
				t.Errorf("an unknown %s was accepted", name)
			}
		})
	}
}

func TestADeploymentMustBeAttributable(t *testing.T) {
	d := newDeployment(t)
	d.Actor = identity.Principal{}
	if err := d.Validate(); err == nil {
		t.Error("a deployment with no actor was accepted")
	}
}

// New always stamps the creation time, but a deployment read back from storage
// is a struct someone else filled in, and Validate is what stands between it
// and an apply.
func TestAReconstitutedDeploymentMustCarryItsCreationTime(t *testing.T) {
	d := newDeployment(t)
	d.CreatedAt = time.Time{}
	if err := d.Validate(); err == nil {
		t.Error("a deployment with no creation time was accepted")
	}
}

// A deployment is only ever created by a planner that produced a plan, so New
// refuses a draft that names no plan rather than leaving it to be discovered at
// apply time.
func TestNewRefusesADraftItCannotValidate(t *testing.T) {
	d := draft()
	d.PlanID = ""
	if _, err := deployment.New(newGen(), newClock(), author(), d); err == nil {
		t.Error("a deployment with no plan was created")
	}
}

func TestNewNeedsAWorkingGenerator(t *testing.T) {
	failing := identity.GeneratorFunc(func() (string, error) {
		return "", errors.New("no entropy")
	})
	if _, err := deployment.New(failing, newClock(), author(), draft()); err == nil {
		t.Error("a deployment was created without an identifier")
	}
}

func TestALegalTransitionMovesTheStatus(t *testing.T) {
	d := newDeployment(t)
	got, err := d.TransitionTo(deployment.StatusQueued, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if got.Status != deployment.StatusQueued {
		t.Errorf("Status = %s, want %s", got.Status, deployment.StatusQueued)
	}
}

func TestAnIllegalTransitionIsRefused(t *testing.T) {
	d := newDeployment(t)
	_, err := d.TransitionTo(deployment.StatusSucceeded, at.Add(time.Minute))
	if !errors.Is(err, deployment.ErrIllegalTransition) {
		t.Errorf("got %v, want ErrIllegalTransition", err)
	}
}

// TransitionTo returns a copy. A caller still holding the deployment it was
// called on must not find its status changed underneath it — that is what makes
// a failed write safe to discard.
func TestTransitionDoesNotMutateTheReceiver(t *testing.T) {
	d := newDeployment(t)
	if _, err := d.TransitionTo(deployment.StatusQueued, at); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if d.Status != deployment.StatusPlanned {
		t.Errorf("the receiver moved to %s", d.Status)
	}
}

// Started means "began changing the world", which is the moment it enters
// applying and not before.
func TestStartingToApplyStampsTheStartTime(t *testing.T) {
	d := advance(t, newDeployment(t), deployment.StatusQueued)
	started := at.Add(time.Minute)
	d, err := d.TransitionTo(deployment.StatusApplying, started)
	if err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if d.StartedAt == nil || !d.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %s", d.StartedAt, started)
	}
}

// A paused canary that resumes has not started again. Re-stamping would reset
// the elapsed time an operator is watching, and would make the duration of a
// deployment depend on how often it was paused.
func TestResumingDoesNotRestampTheStartTime(t *testing.T) {
	started := at.Add(time.Minute)
	d := advance(t, newDeployment(t), deployment.StatusQueued)
	d = step(t, d, deployment.StatusApplying, started)
	d = step(t, d, deployment.StatusPaused, started.Add(time.Minute))
	d = step(t, d, deployment.StatusApplying, started.Add(2*time.Minute))

	if d.StartedAt == nil || !d.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %s", d.StartedAt, started)
	}
}

// Whatever the outcome, the record says when it was reached. A terminal status
// with no finish time would leave every duration in the timeline open-ended.
func TestReachingATerminalStatusStampsTheFinishTime(t *testing.T) {
	// Each terminal status is reached by the shortest path that legitimately
	// ends there, because the paths differ: a deployment is cancelled before it
	// has a verdict and rolled back only after one.
	paths := map[deployment.Status][]deployment.Status{
		deployment.StatusSucceeded: {
			deployment.StatusQueued, deployment.StatusApplying,
			deployment.StatusVerifying, deployment.StatusSucceeded,
		},
		deployment.StatusFailed: {
			deployment.StatusQueued, deployment.StatusApplying, deployment.StatusFailed,
		},
		deployment.StatusCancelled: {
			deployment.StatusQueued, deployment.StatusCancelled,
		},
		deployment.StatusRolledBack: {
			deployment.StatusQueued, deployment.StatusApplying,
			deployment.StatusRollingBack, deployment.StatusRolledBack,
		},
	}
	for terminal, path := range paths {
		t.Run(string(terminal), func(t *testing.T) {
			d := advance(t, newDeployment(t), path[:len(path)-1]...)
			finished := at.Add(time.Hour)
			d, err := d.TransitionTo(terminal, finished)
			if err != nil {
				t.Fatalf("TransitionTo %s: %v", terminal, err)
			}
			if d.FinishedAt == nil || !d.FinishedAt.Equal(finished) {
				t.Errorf("FinishedAt = %v, want %s", d.FinishedAt, finished)
			}
		})
	}
}

// Verification is where the verdict comes from. Cancelling it would leave the
// change applied with nothing recorded about whether it worked, which is the
// one outcome an operator cannot act on. Stop watching and accept, or stop
// watching and revert — but say which.
func TestVerificationCannotBeCancelled(t *testing.T) {
	d := advance(t, newDeployment(t), deployment.StatusQueued, deployment.StatusApplying, deployment.StatusVerifying)
	if _, err := d.TransitionTo(deployment.StatusCancelled, at.Add(time.Hour)); !errors.Is(err, deployment.ErrIllegalTransition) {
		t.Errorf("got %v, want ErrIllegalTransition", err)
	}
}

func TestANonTerminalTransitionDoesNotFinishTheDeployment(t *testing.T) {
	d := advance(t, newDeployment(t), deployment.StatusQueued, deployment.StatusApplying)
	if d.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil", d.FinishedAt)
	}
}

// The ordinary path, end to end. It is written out because the transition table
// is easy to change in a way that is individually defensible and collectively
// breaks the one sequence that has to work.
func TestTheOrdinaryPathRunsToSuccess(t *testing.T) {
	d := advance(t, newDeployment(t),
		deployment.StatusAwaitingApproval,
		deployment.StatusQueued,
		deployment.StatusApplying,
		deployment.StatusVerifying,
		deployment.StatusPromoting,
		deployment.StatusSucceeded,
	)
	if d.Status != deployment.StatusSucceeded {
		t.Errorf("Status = %s, want succeeded", d.Status)
	}
	if d.StartedAt == nil || d.FinishedAt == nil {
		t.Errorf("the deployment ran without a start (%v) or a finish (%v)", d.StartedAt, d.FinishedAt)
	}
}

// A failed verification leads to a rollback and then to rolled back — never to
// succeeded, and never straight from verifying to rolled back without the
// intermediate state that says work is in progress.
func TestAFailedVerificationRollsBack(t *testing.T) {
	d := advance(t, newDeployment(t),
		deployment.StatusQueued,
		deployment.StatusApplying,
		deployment.StatusVerifying,
		deployment.StatusRollingBack,
		deployment.StatusRolledBack,
	)
	if d.Status != deployment.StatusRolledBack {
		t.Errorf("Status = %s, want rolled_back", d.Status)
	}
}

// Previous links a deployment to the one it replaces, which is what makes a
// rollback target derivable from the record rather than guessed.
func TestADeploymentMayNameTheOneItReplaces(t *testing.T) {
	first := newDeployment(t)
	d := draft()
	d.Previous = &first.ID
	second, err := deployment.New(newGen(), newClock(), author(), d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if second.Previous == nil || *second.Previous != first.ID {
		t.Errorf("Previous = %v, want %s", second.Previous, first.ID)
	}
}

func advance(t *testing.T, d deployment.Deployment, steps ...deployment.Status) deployment.Deployment {
	t.Helper()
	for i, s := range steps {
		d = step(t, d, s, at.Add(time.Duration(i+1)*time.Minute))
	}
	return d
}

func step(t *testing.T, d deployment.Deployment, s deployment.Status, when time.Time) deployment.Deployment {
	t.Helper()
	next, err := d.TransitionTo(s, when)
	if err != nil {
		t.Fatalf("TransitionTo %s: %v", s, err)
	}
	return next
}
