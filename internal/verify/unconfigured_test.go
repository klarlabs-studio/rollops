package verify_test

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/verify"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

func TestAnInstallationWithNoChecksObservesNothingRatherThanPassing(t *testing.T) {
	t.Parallel()

	res, err := verify.Unconfigured{}.Verify(context.Background(), verifyv1.VerificationRequest{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verdict != verifyv1.VerdictInconclusive {
		t.Fatalf("verdict %q; a default installation that passed would be one where nobody looked and everybody was told it was fine",
			res.Verdict)
	}
	if !strings.Contains(res.Reason, "no verification checks are configured") {
		t.Errorf("reason %q does not say why nothing was observed", res.Reason)
	}
}

func TestTheUnconfiguredCheckBlocksWhenItIsTheOnlyOne(t *testing.T) {
	t.Parallel()

	// The verdict only matters through Combine, which is what deploy.Service
	// reads. Asserting the reduction rather than the verdict alone is what
	// pins the property that matters: a deployment does not get promoted.
	res, err := verify.Unconfigured{}.Verify(context.Background(), verifyv1.VerificationRequest{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := verifyv1.Combine(res.Verdict); got == verifyv1.VerdictPass {
		t.Fatal("a suite of only the unconfigured check reduced to pass")
	}
}

func TestTheUnconfiguredCheckIdentifiesItself(t *testing.T) {
	t.Parallel()

	// runner.New refuses a check reporting neither kind nor name, because an
	// answer that cannot be attributed is the one thing a timeline exists for.
	meta := verify.Unconfigured{}.Metadata()
	if strings.TrimSpace(meta.Kind) == "" || strings.TrimSpace(meta.Name) == "" {
		t.Fatalf("metadata %+v cannot be attributed", meta)
	}
}

func TestTheUnconfiguredCheckStampsTheWindowItRanIn(t *testing.T) {
	t.Parallel()

	res, err := verify.Unconfigured{}.Verify(context.Background(), verifyv1.VerificationRequest{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.StartedAt.IsZero() || res.FinishedAt.Before(res.StartedAt) {
		t.Errorf("window %s..%s cannot be lined up against the deploy", res.StartedAt, res.FinishedAt)
	}
}

func TestAnAbandonedRunIsNotReportedAsUnconfigured(t *testing.T) {
	t.Parallel()

	// A cancelled run and an unconfigured installation are different facts and
	// lead somewhere different: one is fixed by configuring a check, the other
	// by not abandoning the deployment.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := verify.Unconfigured{}.Verify(ctx, verifyv1.VerificationRequest{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Verdict != verifyv1.VerdictCancelled {
		t.Errorf("verdict %q, want %q", res.Verdict, verifyv1.VerdictCancelled)
	}
}
