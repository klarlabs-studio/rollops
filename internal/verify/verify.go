// Package verify holds the first-party verifiers (§11.2). Health observation,
// smoke commands and metric analysis were three bespoke gates with three
// bespoke signatures; here they are three implementations of one contract, so
// a fourth — an HTTP probe, a log query, a synthetic check — is a new type
// rather than a new branch in the engine.
//
// The rule every verifier here obeys: a check that could not be run reports
// inconclusive, never a pass and never a fail. "Nobody looked" is not evidence
// about the deploy in either direction (§11.3, P8, INV-010).
package verify

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/analysis"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// window stamps a result with when the check ran, so it can be lined up
// against everything else that happened during the deploy.
func window(started time.Time, res verifyv1.VerificationResult) verifyv1.VerificationResult {
	res.StartedAt, res.FinishedAt = started, time.Now()
	return res
}

// Unconfigured is what an installation that configured no checks verifies with.
//
// A suite must hold at least one check — a runner with none could only ever
// report an empty set, and an empty set is indistinguishable from a suite that
// ran and found nothing wrong. So the absence is made into an answer: nobody
// looked, said out loud, which blocks in Combine exactly as this package's rule
// requires (§11.3). The alternative is an installation that promotes every
// deployment on the strength of a verification nobody wrote.
type Unconfigured struct{}

func (Unconfigured) Metadata() verifyv1.VerifierMetadata {
	return verifyv1.VerifierMetadata{Kind: "unconfigured", Name: "unconfigured", Version: "v1"}
}

// Verify reports the misconfiguration rather than the deployment. The reason is
// the whole point of the type: an operator reading a blocked promotion needs to
// land on "configure a check", not on a target that looks unhealthy.
func (Unconfigured) Verify(ctx context.Context, _ verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
	started := time.Now()
	// A cancelled run and an unconfigured installation lead somewhere
	// different, and only one of them is fixed by editing configuration.
	if v := verifyv1.Interrupted(ctx); v != "" {
		return window(started, verifyv1.VerificationResult{Verdict: v, Reason: ctx.Err().Error()}), nil
	}
	return window(started, verifyv1.VerificationResult{
		Verdict: verifyv1.VerdictInconclusive,
		Reason:  "no verification checks are configured, so nothing observed this deployment",
	}), nil
}

// Health asks the target whether it is serving.
type Health struct {
	Target *targetv2.Bound
}

func (Health) Metadata() verifyv1.VerifierMetadata {
	return verifyv1.VerifierMetadata{Kind: "health", Name: "health", Version: "v1"}
}

// Verify maps the target's own health verdict into the verification
// vocabulary. Degraded passes — a target serving at reduced capacity is still
// serving — but unknown does not: a target that did not say is a target nobody
// measured, and letting the zero value through is the permissive default this
// package exists to remove.
func (h Health) Verify(ctx context.Context, _ verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
	started := time.Now()
	if v := verifyv1.Interrupted(ctx); v != "" {
		return window(started, verifyv1.VerificationResult{Verdict: v, Reason: ctx.Err().Error()}), nil
	}
	obs, err := h.Target.Observe(ctx, targetv2.ObserveRequest{})
	if err != nil {
		return window(started, verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictInconclusive,
			Reason:  "health check inconclusive: target could not be observed: " + err.Error(),
		}), nil
	}
	switch obs.Health.State {
	case targetv2.HealthHealthy, targetv2.HealthDegraded:
		return window(started, verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictPass,
			Reason:  stateName(obs.Health.State),
		}), nil
	case targetv2.HealthUnhealthy:
		// The wording predates the verdict vocabulary and is matched by
		// surfaces downstream of the gate, so it is preserved rather than
		// regularised.
		reason := "health check failed"
		if obs.Health.Reason != "" {
			reason += ": " + obs.Health.Reason
		}
		return window(started, verifyv1.VerificationResult{Verdict: verifyv1.VerdictFail, Reason: reason}), nil
	default:
		return window(started, verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictInconclusive,
			Reason:  "health check inconclusive: target reported no health state",
		}), nil
	}
}

func stateName(s targetv2.HealthState) string {
	switch s {
	case targetv2.HealthHealthy:
		return "healthy"
	case targetv2.HealthDegraded:
		return "degraded"
	case targetv2.HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// Command runs a smoke command and reads its exit code.
type Command struct {
	Cmd        []string
	ExpectExit int
	Run        func(ctx context.Context, cmd []string) (exitCode int, err error)
}

func (Command) Metadata() verifyv1.VerifierMetadata {
	return verifyv1.VerifierMetadata{Kind: "command", Name: "smoke", Version: "v1"}
}

// Verify treats the exit code as the observation and a command that never
// launched as no observation at all. The two used to be the same failure,
// which meant a missing binary on the runner read as a broken deploy.
func (c Command) Verify(ctx context.Context, _ verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
	started := time.Now()
	if v := verifyv1.Interrupted(ctx); v != "" {
		return window(started, verifyv1.VerificationResult{Verdict: v, Reason: ctx.Err().Error()}), nil
	}
	code, err := c.Run(ctx, c.Cmd)
	switch {
	case err != nil:
		return window(started, verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictInconclusive,
			Reason:  "smoke test did not run: " + err.Error(),
		}), nil
	case code != c.ExpectExit:
		return window(started, verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictFail,
			Reason:  fmt.Sprintf("smoke test exit %d (expected %d)", code, c.ExpectExit),
		}), nil
	}
	return window(started, verifyv1.VerificationResult{Verdict: verifyv1.VerdictPass}), nil
}

// Metrics runs a compiled metric analysis.
type Metrics struct {
	Analyzer *analysis.Analyzer
}

func (Metrics) Metadata() verifyv1.VerifierMetadata {
	return verifyv1.VerifierMetadata{Kind: "metrics", Name: "analysis", Version: "v1"}
}

// Verify passes the analyzer's verdict through unflattened. The analyzer
// already speaks the five-verdict vocabulary, and collapsing it back to a bool
// here would undo the distinction the contract exists to carry.
func (m Metrics) Verify(ctx context.Context, _ verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
	started := time.Now()
	res := m.Analyzer.Run(ctx)
	out := verifyv1.VerificationResult{Verdict: res.Verdict, Reason: res.Reason}
	for _, mm := range res.Measurements {
		for name, v := range mm.Values {
			out.Measurements = append(out.Measurements, verifyv1.Measurement{Name: name, Value: v})
		}
	}
	return window(started, out), nil
}
