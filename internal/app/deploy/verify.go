package deploy

import (
	"context"
	"errors"
	"fmt"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/release"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// ErrNotVerifiable reports a deployment with nothing to verify yet. Only one
// that has applied something has put anything in front of a check.
var ErrNotVerifiable = errors.New("deploy: deployment cannot be verified")

// VerificationRequest is what the checks are asked about. A verifier's own
// configuration — a URL, a query, a command — is bound when it is constructed;
// what changes between calls is the deployment under observation.
type VerificationRequest struct {
	Environment environment.Environment
	Release     release.Release
	Deployment  deployment.Deployment
}

// CheckResult is one check's answer together with which verifier gave it.
// verifyv1.VerificationResult says what was concluded and not by whom, and a
// timeline that cannot name the check that failed sends an operator to read all
// of them.
type CheckResult struct {
	Verifier verifyv1.VerifierMetadata
	Result   verifyv1.VerificationResult
}

// Verifier observes whether what landed is actually serving (§11.1).
//
// It returns every check's answer rather than one combined verdict, so that
// §11.3's "inconclusive MUST NOT silently become pass" is enforced where the
// verdict turns into a status somebody acts on rather than inside whichever
// adapter happened to run the checks.
type Verifier interface {
	Verify(ctx context.Context, req VerificationRequest) ([]CheckResult, error)
}

// Verification is what Verify concluded: the combined verdict and the checks it
// was combined from.
type Verification struct {
	Verdict verifyv1.Verdict
	Checks  []CheckResult
}

// VerifyCommand asks whether a deployment that has landed is serving.
type VerifyCommand struct {
	DeploymentID identity.DeploymentID
	Actor        identity.Principal
}

// Verify runs the environment's checks against a deployment and records what
// they concluded.
//
// Unlike Apply this does dispatch to an outside system, and deliberately: a
// verifier observes and changes nothing, which is the same reason Plan may call
// a Planner from here. What this package still never does is mutate a
// substrate — promoting and rolling back record an intent and stop.
//
// The verdict decides the status and nothing else does. A pass leaves the
// deployment verifying, because what finishes a verified deployment is
// promoting it and that is a separate decision. Anything else pauses it:
// §11.4 makes onFailure configurable — rollback, pause, promote anyway — so
// rolling back from here would implement one branch of a policy as though it
// were the only one, and pausing keeps every edge open.
//
// The two writes are two transactions on purpose. Holding one open across a
// check that may query Prometheus for ten minutes would block every other
// write to the deployment; a crash in between leaves the deployment verifying,
// which is the status that says verification is outstanding.
func (s *Service) Verify(
	ctx context.Context, cmd VerifyCommand,
) (deployment.Deployment, Verification, error) {
	d, err := s.cfg.Deployments.Get(ctx, cmd.DeploymentID)
	if err != nil {
		return deployment.Deployment{}, Verification{}, fmt.Errorf(
			"deploy: deployment %s: %w", cmd.DeploymentID, err)
	}
	if !d.Status.CanTransitionTo(deployment.StatusVerifying) {
		return deployment.Deployment{}, Verification{}, fmt.Errorf(
			"%w: %s is %s", ErrNotVerifiable, d.ID, d.Status)
	}
	env, err := s.cfg.Environments.Get(ctx, d.EnvironmentID)
	if err != nil {
		return deployment.Deployment{}, Verification{}, fmt.Errorf(
			"deploy: environment %s: %w", d.EnvironmentID, err)
	}
	rel, err := s.cfg.Releases.Get(ctx, d.ReleaseID)
	if err != nil {
		return deployment.Deployment{}, Verification{}, fmt.Errorf(
			"deploy: release %s: %w", d.ReleaseID, err)
	}

	started, err := s.begin(ctx, cmd, d)
	if err != nil {
		return deployment.Deployment{}, Verification{}, err
	}

	checks, err := s.cfg.Verifier.Verify(ctx, VerificationRequest{
		Environment: env,
		Release:     rel,
		Deployment:  started,
	})
	if err != nil {
		return deployment.Deployment{}, Verification{}, fmt.Errorf("deploy: verifying %s: %w", d.ID, err)
	}
	out := Verification{Verdict: combine(checks), Checks: checks}

	settled, err := s.settle(ctx, cmd, started, out)
	if err != nil {
		return deployment.Deployment{}, Verification{}, err
	}
	return settled, out, nil
}

// begin moves the deployment to verifying and says so, so that a check running
// for minutes is visible as one rather than as a deployment that stopped.
func (s *Service) begin(
	ctx context.Context, cmd VerifyCommand, d deployment.Deployment,
) (deployment.Deployment, error) {
	var out deployment.Deployment
	err := s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		moved, err := d.TransitionTo(deployment.StatusVerifying, s.cfg.Clock.Now())
		if err != nil {
			return err
		}
		rev, err := s.cfg.Deployments.Update(ctx, moved)
		if err != nil {
			return fmt.Errorf("deploy: storing the deployment: %w", err)
		}
		moved.Revision = rev
		if err := s.recordVerificationStart(ctx, cmd, moved); err != nil {
			return err
		}
		out = moved
		return nil
	})
	if err != nil {
		return deployment.Deployment{}, err
	}
	return out, nil
}

// settle records what the checks concluded and moves the deployment to where
// the verdict leaves it.
func (s *Service) settle(
	ctx context.Context, cmd VerifyCommand, d deployment.Deployment, v Verification,
) (deployment.Deployment, error) {
	out := d
	err := s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		if v.Verdict != verifyv1.VerdictPass {
			paused, err := d.TransitionTo(deployment.StatusPaused, s.cfg.Clock.Now())
			if err != nil {
				return err
			}
			rev, err := s.cfg.Deployments.Update(ctx, paused)
			if err != nil {
				return fmt.Errorf("deploy: storing the deployment: %w", err)
			}
			paused.Revision = rev
			out = paused
		}
		return s.recordVerificationEnd(ctx, cmd, out, v)
	})
	if err != nil {
		return deployment.Deployment{}, err
	}
	return out, nil
}

// combine reduces the checks to the verdict acted on. It is verifyv1.Combine
// over the answers, which is where "inconclusive MUST NOT silently become pass"
// and "no checks is not a pass" both live.
func combine(checks []CheckResult) verifyv1.Verdict {
	verdicts := make([]verifyv1.Verdict, len(checks))
	for i, c := range checks {
		verdicts[i] = c.Result.Verdict
	}
	return verifyv1.Combine(verdicts...)
}

// verificationStarted says checks are running, so that a verification nobody
// ever hears back from is still visible as one that was asked for.
//
// It does not name the checks. They are not known yet — the verifier is not
// asked until after this event is written, deliberately, so that a verifier
// that hangs on its first call has already been recorded as started.
type verificationStarted struct {
	PlanID identity.PlanID `json:"plan_id"`
}

// verificationCompleted says what was observed.
//
// The reason is prose written for a person and the evidence is a URI somebody
// constructed — the one field in a result a credential could plausibly arrive
// in, which is why verifyv1.EvidenceRef is a reference rather than content in
// the first place. Neither is here. The verdicts and the measurements are what
// a query matches on, and the verification run holds the rest.
type verificationCompleted struct {
	PlanID  identity.PlanID   `json:"plan_id"`
	Verdict verifyv1.Verdict  `json:"verdict"`
	Status  deployment.Status `json:"status"`
	Checks  []checkOutcome    `json:"checks,omitempty"`
}

type checkOutcome struct {
	Name         string             `json:"name"`
	Kind         string             `json:"kind"`
	Verdict      verifyv1.Verdict   `json:"verdict"`
	Measurements map[string]float64 `json:"measurements,omitempty"`
}

func (s *Service) recordVerificationStart(
	ctx context.Context, cmd VerifyCommand, d deployment.Deployment,
) error {
	payload, err := encode(verificationStarted{PlanID: d.PlanID})
	if err != nil {
		return err
	}
	return s.recordAgainst(ctx, cmd.Actor, d, event.DeploymentVerificationStarted, payload)
}

func (s *Service) recordVerificationEnd(
	ctx context.Context, cmd VerifyCommand, d deployment.Deployment, v Verification,
) error {
	outcomes := make([]checkOutcome, len(v.Checks))
	for i, c := range v.Checks {
		o := checkOutcome{
			Name:    c.Verifier.Name,
			Kind:    c.Verifier.Kind,
			Verdict: c.Result.Verdict,
		}
		if len(c.Result.Measurements) > 0 {
			o.Measurements = make(map[string]float64, len(c.Result.Measurements))
			for _, m := range c.Result.Measurements {
				o.Measurements[m.Name] = m.Value
			}
		}
		outcomes[i] = o
	}
	payload, err := encode(verificationCompleted{
		PlanID:  d.PlanID,
		Verdict: v.Verdict,
		Status:  d.Status,
		Checks:  outcomes,
	})
	if err != nil {
		return err
	}
	return s.recordAgainst(ctx, cmd.Actor, d, event.DeploymentVerificationDone, payload)
}
