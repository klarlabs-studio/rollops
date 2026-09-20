// What the checks said about a deployment.
//
// Verifying is the one deployment verb this layer dispatches synchronously. A
// verifier observes and changes nothing, so there is no substrate waiting on a
// held connection and no engine to hand the work to — the same reason planning
// happens here and applying does not.
//
// A failing verdict is not an error. The call did what it was asked, and what
// it found is the answer; apierr.VerificationFailed exists for the engine, which
// is refused by a verdict rather than reporting one.
package apiv2

import (
	"context"
	"time"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/verification"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// Verifier names the check that gave an answer. A verdict that cannot say who
// reached it sends an operator to read all of them.
type Verifier struct {
	Kind    string
	Name    string
	Version string
}

// Measurement is one figure a verifier read. It is what a query matches on —
// an error rate that crossed a threshold, a latency that did not.
type Measurement struct {
	Name  string
	Value float64
}

// EvidenceRef points at what a verifier looked at: a dashboard, a log query, a
// trace.
//
// It is a reference rather than content, and it is rendered. INV-012 warns that
// a URI somebody constructed is where a credential can arrive, and the run is
// the one record here that deliberately keeps one anyway — withholding it from
// every read would leave evidence nobody can reach, which is the same as not
// having kept it. The line is correctability: a run can be dropped, where the
// event it is paired with never can be.
type EvidenceRef struct {
	Kind string
	URI  string
}

// Check is one verifier's answer.
type Check struct {
	Verifier     Verifier
	Verdict      string
	Measurements []Measurement

	// StartedAt and FinishedAt are the window this check observed, which is
	// not the run's: a run's window spans every check it combined.
	StartedAt  time.Time
	FinishedAt time.Time

	Reason   string
	Evidence []EvidenceRef
}

// VerificationRun is what the checks said, as the API describes it.
//
// Verdict is derived from the checks rather than stored beside them, so a
// reader cannot be shown a summary that disagrees with what it summarises.
type VerificationRun struct {
	ID           string
	DeploymentID string
	PlanID       string
	Verdict      string
	Checks       []Check
	StartedAt    time.Time
	FinishedAt   time.Time
	Actor        Principal
}

// VerifyDeploymentRequest asks whether a deployment that has landed is serving.
type VerifyDeploymentRequest struct {
	DeploymentID string

	// There is no reason field. Verifying asks a question rather than
	// overriding an answer, so there is nothing for a caller to justify.
	Actor identity.Principal

	IdempotencyKey string
}

// VerifyDeploymentResponse is the verdict and where it left the deployment.
//
// Both are here because the verdict decides the status and a caller that had to
// fetch the deployment separately would be reading it after whatever happened
// next. On a replay the deployment is re-read rather than remembered, so a
// retry that arrives after the deployment was promoted describes the run as it
// was and the deployment as it is (§23.3).
type VerifyDeploymentResponse struct {
	Deployment Deployment
	Run        VerificationRun
}

// VerifyDeployment runs the environment's checks and returns what they found.
func (s *Service) VerifyDeployment(
	ctx context.Context, req VerifyDeploymentRequest,
) (VerifyDeploymentResponse, error) {
	deploymentID, err := identity.ParseDeploymentID(req.DeploymentID)
	if err != nil {
		return VerifyDeploymentResponse{}, badArgument("apiv2: deployment id: %w", err)
	}

	fp := fingerprint(string(deploymentID), req.Actor.ID)
	return once(ctx, s, opVerifyDeployment, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, VerifyDeploymentResponse, error) {
			_, v, err := s.deployer.Verify(ctx, deploy.VerifyCommand{
				DeploymentID: deploymentID,
				Actor:        req.Actor,
			})
			if err != nil {
				return "", VerifyDeploymentResponse{}, failure(
					"apiv2: verify deployment %s: %w", deploymentID, err)
			}
			out, err := s.verified(ctx, v.ID)
			return string(v.ID), out, err
		},
		func(ctx context.Context, id string) (VerifyDeploymentResponse, error) {
			runID, err := identity.ParseVerificationRunID(id)
			if err != nil {
				// Not badArgument: this id is one we recorded, so a malformed
				// one is our fault and telling the caller their request was
				// invalid would send them editing something that is fine.
				return VerifyDeploymentResponse{}, internal(
					"apiv2: recorded verification run id %q: %w", id, err)
			}
			return s.verified(ctx, runID)
		},
	)
}

// verified assembles the response by reading what was stored, so that a first
// call and a replay of it are built the same way rather than by two paths that
// can drift.
func (s *Service) verified(
	ctx context.Context, id identity.VerificationRunID,
) (VerifyDeploymentResponse, error) {
	run, err := s.verifications.Get(ctx, id)
	if err != nil {
		return VerifyDeploymentResponse{}, failure("apiv2: verification run %s: %w", id, err)
	}
	d, err := s.deployments.Get(ctx, run.DeploymentID)
	if err != nil {
		return VerifyDeploymentResponse{}, failure("apiv2: deployment %s: %w", run.DeploymentID, err)
	}
	return VerifyDeploymentResponse{
		Deployment: viewDeployment(d),
		Run:        viewVerificationRun(run),
	}, nil
}

// GetVerificationRunRequest names one verification run.
type GetVerificationRunRequest struct{ ID string }

// GetVerificationRun returns what one verification concluded.
//
// This is where a timeline entry leads: the completion event carries the run id
// and the verdicts, and the reason a verifier gave and the evidence it pointed
// at are here.
func (s *Service) GetVerificationRun(
	ctx context.Context, req GetVerificationRunRequest,
) (VerificationRun, error) {
	id, err := identity.ParseVerificationRunID(req.ID)
	if err != nil {
		return VerificationRun{}, badArgument("apiv2: verification run id: %w", err)
	}
	run, err := s.verifications.Get(ctx, id)
	if err != nil {
		return VerificationRun{}, failure("apiv2: verification run %s: %w", id, err)
	}
	return viewVerificationRun(run), nil
}

func viewVerificationRun(r verification.Run) VerificationRun {
	return VerificationRun{
		ID:           string(r.ID),
		DeploymentID: string(r.DeploymentID),
		PlanID:       string(r.PlanID),
		Verdict:      string(r.Verdict()),
		Checks:       mapped(r.Checks, viewCheck),
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
		Actor:        viewPrincipal(r.Actor),
	}
}

func viewCheck(c verification.Check) Check {
	return Check{
		Verifier: Verifier{
			Kind:    c.Verifier.Kind,
			Name:    c.Verifier.Name,
			Version: c.Verifier.Version,
		},
		Verdict:      string(c.Result.Verdict),
		Measurements: mapped(c.Result.Measurements, viewMeasurement),
		StartedAt:    c.Result.StartedAt,
		FinishedAt:   c.Result.FinishedAt,
		Reason:       c.Result.Reason,
		Evidence:     mapped(c.Result.Evidence, viewEvidence),
	}
}

func viewMeasurement(m verifyv1.Measurement) Measurement {
	return Measurement{Name: m.Name, Value: m.Value}
}

func viewEvidence(e verifyv1.EvidenceRef) EvidenceRef {
	return EvidenceRef{Kind: e.Kind, URI: e.URI}
}
