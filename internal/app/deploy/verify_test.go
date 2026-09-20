package deploy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/verification"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// passing is the check every test starts from: one verifier that ran and was
// happy.
func passing() deploy.CheckResult {
	return deploy.CheckResult{
		Verifier: verifyv1.VerifierMetadata{Kind: "http", Name: "health", Version: "1"},
		Result: verifyv1.VerificationResult{
			Verdict:      verifyv1.VerdictPass,
			Measurements: []verifyv1.Measurement{{Name: "status", Value: 200}},
		},
	}
}

func answered(verdict verifyv1.Verdict) deploy.CheckResult {
	c := passing()
	c.Result.Verdict = verdict
	return c
}

// applying drives a deployment to the status verification is reached from, so
// that the test exercises the state machine rather than writing a status past
// it.
func (h *harness) applying(t *testing.T, d deployment.Deployment) deployment.Deployment {
	t.Helper()
	moved, err := d.TransitionTo(deployment.StatusApplying, h.clock.now)
	if err != nil {
		t.Fatalf("transition to applying: %v", err)
	}
	rev, err := h.store.Deployments().Update(context.Background(), moved)
	if err != nil {
		t.Fatalf("store applying: %v", err)
	}
	moved.Revision = rev
	return moved
}

// applied is a deployment sitting where verification can be asked for.
func (h *harness) applied(t *testing.T) deployment.Deployment {
	t.Helper()
	return h.applying(t, h.apply(t, h.plan(t)))
}

func (h *harness) verify(t *testing.T, d deployment.Deployment) (deployment.Deployment, deploy.Verification) {
	t.Helper()
	out, v, err := h.service.Verify(context.Background(), deploy.VerifyCommand{
		DeploymentID: d.ID,
		Actor:        actor(),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return out, v
}

func TestVerifyingReportsWhatTheChecksObserved(t *testing.T) {
	h := newHarness(t)
	d := h.applied(t)

	_, v := h.verify(t, d)

	if v.Verdict != verifyv1.VerdictPass {
		t.Errorf("verdict = %q, want pass", v.Verdict)
	}
	if len(v.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(v.Checks))
	}
	if got := v.Checks[0].Verifier.Name; got != "health" {
		t.Errorf("check = %q, want health", got)
	}
	if len(v.Checks[0].Result.Measurements) != 1 {
		t.Error("the measurement is gone; a verdict with no measurement is an assertion rather than evidence")
	}
}

// A pass does not finish the deployment. What makes a verified deployment
// succeed is promoting it, which is a separate decision and a separate verb.
func TestAPassingVerificationLeavesTheDeploymentVerifying(t *testing.T) {
	h := newHarness(t)
	d := h.applied(t)

	out, _ := h.verify(t, d)

	if out.Status != deployment.StatusVerifying {
		t.Errorf("status = %q, want verifying", out.Status)
	}
	if out.FinishedAt != nil {
		t.Error("a verified deployment is finished; nothing has decided to promote it yet")
	}
}

// §11.4 makes onFailure configurable — rollback, pause, promote anyway — so
// choosing rollback here would implement one branch of a policy as though it
// were the only one. Pausing discards nothing and leaves every edge open.
func TestAFailingVerificationPausesRatherThanRollingBack(t *testing.T) {
	h := newHarness(t)
	h.verifier.checks = []deploy.CheckResult{answered(verifyv1.VerdictFail)}
	d := h.applied(t)

	out, v := h.verify(t, d)

	if v.Verdict != verifyv1.VerdictFail {
		t.Errorf("verdict = %q, want fail", v.Verdict)
	}
	if out.Status != deployment.StatusPaused {
		t.Errorf("status = %q, want paused", out.Status)
	}
}

// §11.3: inconclusive MUST NOT silently become pass. The enforcement is here
// rather than in the adapter because this is where the verdict turns into a
// status somebody acts on.
func TestOneInconclusiveCheckIsNotAPass(t *testing.T) {
	h := newHarness(t)
	h.verifier.checks = []deploy.CheckResult{passing(), answered(verifyv1.VerdictInconclusive)}
	d := h.applied(t)

	out, v := h.verify(t, d)

	if v.Verdict != verifyv1.VerdictInconclusive {
		t.Errorf("verdict = %q, want inconclusive", v.Verdict)
	}
	if out.Status != deployment.StatusPaused {
		t.Errorf("status = %q, want paused", out.Status)
	}
}

// A verification that ran no checks observed nothing, which is not the same as
// observing that everything is well.
func TestAVerificationWithNoChecksIsNotAPass(t *testing.T) {
	h := newHarness(t)
	h.verifier.checks = nil
	d := h.applied(t)

	out, v := h.verify(t, d)

	if v.Verdict == verifyv1.VerdictPass {
		t.Error("no checks ran and the verdict is a pass")
	}
	if out.Status != deployment.StatusPaused {
		t.Errorf("status = %q, want paused", out.Status)
	}
}

func TestVerifyingRecordsThatItStartedAndWhatItConcluded(t *testing.T) {
	h := newHarness(t)
	d := h.applied(t)

	h.verify(t, d)

	es := h.timeline(t)
	started := find(t, es, event.DeploymentVerificationStarted)
	done := find(t, es, event.DeploymentVerificationDone)
	if started.AggregateID != string(d.ID) || done.AggregateID != string(d.ID) {
		t.Errorf("filed against %q and %q, want %q", started.AggregateID, done.AggregateID, d.ID)
	}
	if !strings.Contains(string(done.Payload), `"verdict":"pass"`) {
		t.Errorf("payload %s does not carry the verdict", done.Payload)
	}
}

// The reason is prose written for a person and the evidence is a URI somebody
// constructed — the one field here a credential could plausibly arrive in. The
// verdicts and the measurements are what a query matches on, and they stay.
func TestTheVerificationEventCarriesVerdictsWithoutProseOrEvidence(t *testing.T) {
	h := newHarness(t)
	h.verifier.checks = []deploy.CheckResult{{
		Verifier: verifyv1.VerifierMetadata{Kind: "prometheus", Name: "errors"},
		Result: verifyv1.VerificationResult{
			Verdict:      verifyv1.VerdictFail,
			Reason:       "error rate climbed above the threshold",
			Measurements: []verifyv1.Measurement{{Name: "error_rate", Value: 0.04}},
			Evidence: []verifyv1.EvidenceRef{{
				Kind: "url",
				URI:  "https://grafana.example/d/abc?token=s3cret",
			}},
		},
	}}
	d := h.applied(t)

	h.verify(t, d)

	body := string(find(t, h.timeline(t), event.DeploymentVerificationDone).Payload)
	for _, leaked := range []string{"s3cret", "grafana.example", "error rate climbed"} {
		if strings.Contains(body, leaked) {
			t.Errorf("payload %s carries %q", body, leaked)
		}
	}
	for _, want := range []string{`"verdict":"fail"`, "errors", "error_rate"} {
		if !strings.Contains(body, want) {
			t.Errorf("payload %s does not carry %s", body, want)
		}
	}
}

// Only a deployment that has applied something has anything to verify. The
// refusal is the state machine's rather than a list of statuses kept here.
func TestADeploymentThatHasNotAppliedCannotBeVerified(t *testing.T) {
	h := newHarness(t)
	queued := h.apply(t, h.plan(t))

	_, _, err := h.service.Verify(context.Background(), deploy.VerifyCommand{
		DeploymentID: queued.ID,
		Actor:        actor(),
	})

	if !errors.Is(err, deploy.ErrNotVerifiable) {
		t.Fatalf("err = %v, want ErrNotVerifiable", err)
	}
}

// A verifier that breaks did not return a verdict, and a deployment cannot be
// paused on the strength of a check that never ran. It stays verifying, which
// is the status that says verification is outstanding.
func TestAVerifierThatBreaksLeavesTheDeploymentVerifying(t *testing.T) {
	h := newHarness(t)
	h.verifier.err = errors.New("prometheus is unreachable")
	d := h.applied(t)

	_, _, err := h.service.Verify(context.Background(), deploy.VerifyCommand{
		DeploymentID: d.ID,
		Actor:        actor(),
	})
	if err == nil {
		t.Fatal("Verify: want the verifier's failure")
	}

	stored, err := h.store.Deployments().Get(context.Background(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != deployment.StatusVerifying {
		t.Errorf("status = %q, want verifying", stored.Status)
	}
	for _, e := range h.timeline(t) {
		if e.Type == event.DeploymentVerificationDone {
			t.Error("a verification that never answered was recorded as completed")
		}
	}
}

// run reads back what Verify said it stored. Going through the repository
// rather than trusting the returned value is the point: what an operator will
// read months later is the row, not the response.
func (h *harness) run(t *testing.T, id identity.VerificationRunID) verification.Run {
	t.Helper()
	stored, err := h.store.VerificationRuns().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("read the verification run back: %v", err)
	}
	return stored
}

// The returned identity is the whole reason the run exists: an idempotent
// verify that is asked twice replays by re-reading the run it already produced,
// so a verification with no identity cannot be replayed at all.
func TestAVerificationNamesTheRunItWasRecordedAs(t *testing.T) {
	h := newHarness(t)
	d := h.applied(t)

	_, v := h.verify(t, d)

	if v.ID == "" {
		t.Fatal("the verification names no run; nothing can be replayed by it")
	}
	stored := h.run(t, v.ID)
	if stored.DeploymentID != d.ID {
		t.Errorf("run is about %q, want %q", stored.DeploymentID, d.ID)
	}
	if stored.PlanID != d.PlanID {
		t.Errorf("run cites plan %q, want %q", stored.PlanID, d.PlanID)
	}
	if got := stored.Verdict(); got != v.Verdict {
		t.Errorf("stored verdict %q, returned %q", got, v.Verdict)
	}
}

// The event deliberately drops the reason and the evidence, because it can
// never be corrected. The run is where they live, and if they do not survive
// the write the decision to drop them from the event loses them outright.
func TestTheStoredRunKeepsTheReasonAndEvidenceTheEventDoesNot(t *testing.T) {
	h := newHarness(t)
	h.verifier.checks = []deploy.CheckResult{{
		Verifier: verifyv1.VerifierMetadata{Kind: "prometheus", Name: "errors"},
		Result: verifyv1.VerificationResult{
			Verdict:      verifyv1.VerdictFail,
			Reason:       "error rate climbed above the threshold",
			Measurements: []verifyv1.Measurement{{Name: "error_rate", Value: 0.04}},
			Evidence: []verifyv1.EvidenceRef{{
				Kind: "url",
				URI:  "https://grafana.example/d/abc",
			}},
		},
	}}
	d := h.applied(t)

	_, v := h.verify(t, d)

	stored := h.run(t, v.ID)
	if len(stored.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(stored.Checks))
	}
	got := stored.Checks[0]
	if got.Verifier.Name != "errors" {
		t.Errorf("check = %q, want errors", got.Verifier.Name)
	}
	if got.Result.Reason != "error rate climbed above the threshold" {
		t.Errorf("reason = %q; the event dropped it and the run is the only copy", got.Result.Reason)
	}
	if len(got.Result.Evidence) != 1 || got.Result.Evidence[0].URI != "https://grafana.example/d/abc" {
		t.Errorf("evidence = %v; a verdict nobody can go and look at is an assertion", got.Result.Evidence)
	}
}

// A run states what was observed in a window, so the window has to be the one
// the checks actually ran in rather than the instant they were written down.
func TestTheRunSpansTheWindowTheChecksRanIn(t *testing.T) {
	h := newHarness(t)
	h.verifier.during = func() { h.clock.now = h.clock.now.Add(30 * time.Second) }
	d := h.applied(t)

	_, v := h.verify(t, d)

	stored := h.run(t, v.ID)
	if !stored.StartedAt.Equal(start) {
		t.Errorf("started at %s, want %s", stored.StartedAt, start)
	}
	if want := start.Add(30 * time.Second); !stored.FinishedAt.Equal(want) {
		t.Errorf("finished at %s, want %s", stored.FinishedAt, want)
	}
}

// A run is stored whatever the checks said. A failing verification is the one
// an operator most needs the evidence for.
func TestAFailingVerificationIsRecordedTooAndAgainstTheStatusItLeft(t *testing.T) {
	h := newHarness(t)
	h.verifier.checks = []deploy.CheckResult{answered(verifyv1.VerdictFail)}
	d := h.applied(t)

	out, v := h.verify(t, d)

	if out.Status != deployment.StatusPaused {
		t.Fatalf("status = %q, want paused", out.Status)
	}
	if got := h.run(t, v.ID).Verdict(); got != verifyv1.VerdictFail {
		t.Errorf("stored verdict = %q, want fail", got)
	}
	runs, err := h.store.VerificationRuns().ListForDeployment(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("ListForDeployment: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("got %d runs, want 1", len(runs))
	}
}

// INV-012. The run is persisted and rendered like everything else here, and a
// credential that reached it would be as hard to recall as one in an event.
func TestTheStoredRunCarriesNoSecretFromTheActor(t *testing.T) {
	h := newHarness(t)
	d := h.applied(t)

	_, v := h.verify(t, d)

	stored := h.run(t, v.ID)
	if got := stored.Actor.Claims["token"]; got == actor().Claims["token"] {
		t.Errorf("the actor's token was stored with the run as %q", got)
	}
	if stored.Actor.ID != actor().ID {
		t.Errorf("actor = %q, want %q", stored.Actor.ID, actor().ID)
	}
}

// A verifier that breaks returned no verdict, so there is nothing a run could
// truthfully say was observed.
func TestAVerifierThatBreaksRecordsNoRun(t *testing.T) {
	h := newHarness(t)
	h.verifier.err = errors.New("prometheus is unreachable")
	d := h.applied(t)

	_, _, err := h.service.Verify(context.Background(), deploy.VerifyCommand{
		DeploymentID: d.ID,
		Actor:        actor(),
	})
	if err == nil {
		t.Fatal("Verify: want the verifier's failure")
	}

	runs, err := h.store.VerificationRuns().ListForDeployment(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("ListForDeployment: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("got %d runs; nothing was observed", len(runs))
	}
}

func TestVerifyingADeploymentNobodyCreatedIsNotFound(t *testing.T) {
	h := newHarness(t)
	absent, err := identity.NewDeploymentID(h.gen)
	if err != nil {
		t.Fatalf("NewDeploymentID: %v", err)
	}

	_, _, err = h.service.Verify(context.Background(), deploy.VerifyCommand{
		DeploymentID: absent,
		Actor:        actor(),
	})

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
