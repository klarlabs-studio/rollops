package apiv2_test

import (
	"context"
	"testing"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// answering makes the verifier behind the API give one verdict, with the prose
// and the evidence a real one carries.
func (s scene) answering(verdict verifyv1.Verdict) {
	s.checks.checks = []deploy.CheckResult{{
		Verifier: verifyv1.VerifierMetadata{Kind: "prometheus", Name: "error-rate", Version: "1.2.0"},
		Result: verifyv1.VerificationResult{
			Verdict:      verdict,
			Reason:       "the error rate stayed under the threshold",
			Measurements: []verifyv1.Measurement{{Name: "error_rate", Value: 0.004}},
			Evidence: []verifyv1.EvidenceRef{{
				Kind: "url",
				URI:  "https://grafana.example/d/abc?from=now-10m",
			}},
		},
	}}
}

func (s scene) verifying(d apiv2.Deployment, key string) apiv2.VerifyDeploymentRequest {
	return apiv2.VerifyDeploymentRequest{
		DeploymentID:   d.ID,
		Actor:          planner,
		IdempotencyKey: key,
	}
}

// applied is a deployment the engine has landed, which is where verification is
// asked for. serving is one status further on, because asking for a
// verification is what puts a deployment there.
func (s scene) applied(t *testing.T) apiv2.Deployment {
	t.Helper()
	return s.engineMoved(t, s.queued(t), deployment.StatusApplying)
}

func TestVerifyingReturnsTheVerdictAndWhereItLeftTheDeployment(t *testing.T) {
	s := setup(t).scene(t)
	s.answering(verifyv1.VerdictPass)

	got, err := s.svc.VerifyDeployment(context.Background(), s.verifying(s.applied(t), ""))
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}

	if got.Run.Verdict != string(verifyv1.VerdictPass) {
		t.Errorf("verdict = %q, want pass", got.Run.Verdict)
	}
	if got.Deployment.Status != string(deployment.StatusVerifying) {
		t.Errorf("status = %q, want verifying", got.Deployment.Status)
	}
	if got.Run.DeploymentID != got.Deployment.ID {
		t.Errorf("run is about %q, want %q", got.Run.DeploymentID, got.Deployment.ID)
	}
	if len(got.Run.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(got.Run.Checks))
	}
	if got.Run.Checks[0].Verifier.Name != "error-rate" {
		t.Errorf("check = %q, want error-rate", got.Run.Checks[0].Verifier.Name)
	}
}

// A verdict that is not a pass is still a successful call. The answer to "is it
// serving" is no, which is what was asked.
func TestAFailingVerdictIsAnAnswerRatherThanAnError(t *testing.T) {
	s := setup(t).scene(t)
	s.answering(verifyv1.VerdictFail)

	got, err := s.svc.VerifyDeployment(context.Background(), s.verifying(s.applied(t), ""))
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}

	if got.Run.Verdict != string(verifyv1.VerdictFail) {
		t.Errorf("verdict = %q, want fail", got.Run.Verdict)
	}
	if got.Deployment.Status != string(deployment.StatusPaused) {
		t.Errorf("status = %q, want paused", got.Deployment.Status)
	}
}

// The completion event drops the reason and the evidence because it can never
// be corrected. The read that replaces them is this one, and if it withheld
// them too they would have been dropped rather than moved.
func TestTheRunARunIdLeadsToCarriesTheReasonAndTheEvidence(t *testing.T) {
	s := setup(t).scene(t)
	s.answering(verifyv1.VerdictFail)
	verified, err := s.svc.VerifyDeployment(context.Background(), s.verifying(s.applied(t), ""))
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}

	got, err := s.svc.GetVerificationRun(context.Background(),
		apiv2.GetVerificationRunRequest{ID: verified.Run.ID})
	if err != nil {
		t.Fatalf("GetVerificationRun: %v", err)
	}

	if len(got.Checks) != 1 {
		t.Fatalf("got %d checks, want 1", len(got.Checks))
	}
	c := got.Checks[0]
	if c.Reason != "the error rate stayed under the threshold" {
		t.Errorf("reason = %q; the event dropped it and this read is the only copy", c.Reason)
	}
	if len(c.Evidence) != 1 || c.Evidence[0].URI != "https://grafana.example/d/abc?from=now-10m" {
		t.Errorf("evidence = %v; a verdict nobody can go and look at is an assertion", c.Evidence)
	}
	if len(c.Measurements) != 1 || c.Measurements[0].Name != "error_rate" {
		t.Errorf("measurements = %v, want the error rate", c.Measurements)
	}
}

// INV-012. The actor reached the run through a command, and a view that
// rendered claims would put whatever an identity provider attached on the wire.
func TestAVerificationRunNamesItsActorWithoutItsClaims(t *testing.T) {
	s := setup(t).scene(t)
	s.answering(verifyv1.VerdictPass)

	got, err := s.svc.VerifyDeployment(context.Background(), s.verifying(s.applied(t), ""))
	if err != nil {
		t.Fatalf("VerifyDeployment: %v", err)
	}

	if got.Run.Actor.ID != planner.ID {
		t.Errorf("actor = %q, want %q", got.Run.Actor.ID, planner.ID)
	}
}

// A deployment still in the queue has changed nothing there is anything to
// check. Not VERIFICATION_FAILED — nothing was verified.
func TestVerifyingADeploymentThatHasNotAppliedIsAConflict(t *testing.T) {
	s := setup(t).scene(t)

	_, err := s.svc.VerifyDeployment(context.Background(), s.verifying(s.queued(t), ""))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestAMalformedVerifyRequestIsTheCallersMistake(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*apiv2.VerifyDeploymentRequest)
	}{
		{"not an id at all", func(r *apiv2.VerifyDeploymentRequest) { r.DeploymentID = "nonsense" }},
		{"an id of the wrong kind", func(r *apiv2.VerifyDeploymentRequest) {
			r.DeploymentID = "pln_0199a0dd-0000-7000-8000-000000000000"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t).scene(t)
			req := s.verifying(s.applied(t), "")
			tc.spoil(&req)

			_, err := s.svc.VerifyDeployment(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

func TestAskingForAVerificationRunNobodyRecordedIsNotFound(t *testing.T) {
	s := setup(t).scene(t)

	_, err := s.svc.GetVerificationRun(context.Background(),
		apiv2.GetVerificationRunRequest{ID: "vrf_0199a0dd-0000-7000-8000-000000000000"})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

// Verifying dispatches to a verifier and charges whatever it queries, so a
// retry has to replay rather than ask again.
func TestTwoVerifyCallsWithOneKeyVerifyOnce(t *testing.T) {
	s := setup(t).scene(t)
	s.answering(verifyv1.VerdictPass)
	d := s.applied(t)

	first, err := s.svc.VerifyDeployment(context.Background(), s.verifying(d, "k-1"))
	if err != nil {
		t.Fatalf("first VerifyDeployment: %v", err)
	}
	second, err := s.svc.VerifyDeployment(context.Background(), s.verifying(d, "k-1"))
	if err != nil {
		t.Fatalf("second VerifyDeployment: %v", err)
	}

	if first.Run.ID != second.Run.ID {
		t.Errorf("the retry answered with run %q, want %q", second.Run.ID, first.Run.ID)
	}
	id, err := identity.ParseDeploymentID(d.ID)
	if err != nil {
		t.Fatalf("deployment id: %v", err)
	}
	runs, err := s.store.VerificationRuns().ListForDeployment(context.Background(), id)
	if err != nil {
		t.Fatalf("ListForDeployment: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("got %d runs, want 1: the checks were asked twice", len(runs))
	}
}
