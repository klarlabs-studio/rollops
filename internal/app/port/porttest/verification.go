package porttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/verification"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// verificationRun builds a completed run over the given verdicts, one check
// each. The actor carries a credential in its claims so that every store is
// asked the INV-012 question, and the checks carry a reason and evidence so
// that the other half of that line — what a run may keep — is asked too.
func (f *fixture) verificationRun(
	t *testing.T, d deployment.Deployment, p plan.DeploymentPlan, verdicts ...verifyv1.Verdict,
) verification.Run {
	t.Helper()
	id, err := identity.NewVerificationRunID(f.gen)
	if err != nil {
		t.Fatalf("build verification run id: %v", err)
	}
	began := f.clock.Now()
	checks := make([]verification.Check, len(verdicts))
	for i, v := range verdicts {
		checks[i] = verification.Check{
			Verifier: verifyv1.VerifierMetadata{
				Kind: "prometheus", Name: "error-rate", Version: "1.2.0",
			},
			Result: verifyv1.VerificationResult{
				Verdict:      v,
				Measurements: []verifyv1.Measurement{{Name: "error_rate", Value: 0.004}},
				StartedAt:    began,
				FinishedAt:   began.Add(30 * time.Second),
				Reason:       "error rate held under the threshold for ten minutes",
				Evidence: []verifyv1.EvidenceRef{
					{Kind: "url", URI: "https://grafana.example/d/abc?from=now-10m"},
				},
			},
		}
	}
	return verification.Run{
		ID:           id,
		DeploymentID: d.ID,
		PlanID:       p.ID,
		Checks:       checks,
		StartedAt:    began,
		FinishedAt:   began.Add(30 * time.Second),
		Actor: identity.Principal{
			ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
			Claims: map[string]string{"role": "release-manager", "token": "s3cr3t"},
		},
	}
}

func runVerificationRuns(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	// stored seeds a world, a plan and one deployment made from it — a run has
	// to name a deployment that exists, so there is no shorter starting point.
	stored := func(t *testing.T) (world, plan.DeploymentPlan, deployment.Deployment) {
		t.Helper()
		w, p := storedPlan(t, newRepos)
		d := w.f.deployment(t, p)
		rev, err := w.r.Deployments.Create(ctx, d)
		if err != nil {
			t.Fatalf("Create deployment: %v", err)
		}
		d.Revision = rev
		return w, p, d
	}

	t.Run("a stored run reads back whole", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictPass, verifyv1.VerdictInconclusive)
		if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.VerificationRuns.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertVerificationRun(t, got, run)
	})

	// The verdict is derived from the checks rather than stored beside them, so
	// a store that dropped or reordered one would change the answer a promotion
	// was made on without losing a field anyone would notice missing.
	t.Run("the verdict the stored checks combine to is unchanged", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictPass, verifyv1.VerdictFail)
		if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.VerificationRuns.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Verdict() != verifyv1.VerdictFail {
			t.Fatalf("the stored run reads as %q, want fail", got.Verdict())
		}
	})

	// The run is the one place reason and evidence live. The event log does not
	// carry them because it can never be corrected (INV-013); a run can be
	// dropped, so it is allowed to hold what an operator actually needs to go
	// and look at. A store that quietly redacted them would leave a fail with
	// no account of itself.
	t.Run("the reason and evidence a verifier gave survive", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictFail)
		if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.VerificationRuns.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		want := run.Checks[0].Result
		if got.Checks[0].Result.Reason != want.Reason {
			t.Errorf("Reason = %q, want %q", got.Checks[0].Result.Reason, want.Reason)
		}
		if len(got.Checks[0].Result.Evidence) != len(want.Evidence) {
			t.Fatalf("read back %d evidence refs, want %d",
				len(got.Checks[0].Result.Evidence), len(want.Evidence))
		}
		if got.Checks[0].Result.Evidence[0] != want.Evidence[0] {
			t.Errorf("Evidence = %+v, want %+v",
				got.Checks[0].Result.Evidence[0], want.Evidence[0])
		}
	})

	// INV-012. The actor is the one part of a run that comes from an identity
	// provider, and claims are where a bearer token arrives.
	t.Run("a secret in the actor's claims is never stored", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictPass)
		if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.VerificationRuns.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Actor.Claims["token"] == "s3cr3t" {
			t.Error("the token was stored verbatim")
		}
		if got.Actor.Claims["role"] != "release-manager" {
			t.Errorf("role = %q, want release-manager — redaction took a claim that was not secret",
				got.Actor.Claims["role"])
		}
	})

	// A deployment can be verified more than once — a canary probed again after
	// a pause — and which answer came last is the whole question an operator is
	// asking, so the order is part of the contract rather than the store's
	// iteration order.
	t.Run("runs come back oldest first", func(t *testing.T) {
		w, p, d := stored(t)
		want := []verifyv1.Verdict{
			verifyv1.VerdictInconclusive, verifyv1.VerdictFail, verifyv1.VerdictPass,
		}
		ids := make([]identity.VerificationRunID, len(want))
		for i, v := range want {
			run := w.f.verificationRun(t, d, p, v)
			ids[i] = run.ID
			if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
				t.Fatalf("Create %s: %v", v, err)
			}
		}

		got, err := w.r.VerificationRuns.ListForDeployment(ctx, d.ID)
		if err != nil {
			t.Fatalf("ListForDeployment: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("read back %d runs, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i].ID != ids[i] {
				t.Errorf("run %d is %s, want %s — when a verdict was given is part of the record",
					i, got[i].ID, ids[i])
			}
			if v := got[i].Verdict(); v != want[i] {
				t.Errorf("run %d reads as %q, want %q", i, v, want[i])
			}
		}
	})

	t.Run("another deployment's runs are not returned", func(t *testing.T) {
		w, p, d := stored(t)
		other := w.f.deployment(t, p)
		if _, err := w.r.Deployments.Create(ctx, other); err != nil {
			t.Fatalf("Create other deployment: %v", err)
		}
		if err := w.r.VerificationRuns.Create(
			ctx, w.f.verificationRun(t, d, p, verifyv1.VerdictPass),
		); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.VerificationRuns.ListForDeployment(ctx, other.ID)
		if err != nil {
			t.Fatalf("ListForDeployment: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("another deployment's runs leaked in: %+v", got)
		}
	})

	t.Run("a deployment nobody verified has no runs, not an error", func(t *testing.T) {
		w, _, d := stored(t)
		got, err := w.r.VerificationRuns.ListForDeployment(ctx, d.ID)
		if err != nil {
			t.Fatalf("ListForDeployment: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("an unverified deployment has %d runs", len(got))
		}
	})

	// A verdict about nothing is not evidence, and a run whose deployment is
	// missing is a row no read could ever reach.
	t.Run("a run about a deployment nobody stored is refused", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictPass)
		run.DeploymentID = "dpl_nobody"

		if err := w.r.VerificationRuns.Create(ctx, run); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("the same run twice is refused", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictPass)
		if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := w.r.VerificationRuns.Create(ctx, run); !errors.Is(err, port.ErrAlreadyExists) {
			t.Fatalf("err = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("a run nobody stored is not found", func(t *testing.T) {
		w, _, _ := stored(t)
		if _, err := w.r.VerificationRuns.Get(ctx, "vrf_nobody"); !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	// A check nobody can attribute is a verdict with no author, and a store
	// that accepted one would hold a record an operator cannot act on.
	t.Run("an incomplete run is refused", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictFail)
		run.Checks[0].Verifier.Name = ""

		if err := w.r.VerificationRuns.Create(ctx, run); err == nil {
			t.Fatal("a check naming no verifier was stored")
		}
	})

	// The in-memory store hands back whatever it holds, so a caller that edits
	// a run it read would be editing persisted state — a difference from SQLite
	// that would only ever show up in production.
	t.Run("editing what was read back does not edit the store", func(t *testing.T) {
		w, p, d := stored(t)
		run := w.f.verificationRun(t, d, p, verifyv1.VerdictPass)
		if err := w.r.VerificationRuns.Create(ctx, run); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.VerificationRuns.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		got.Checks[0].Result.Verdict = verifyv1.VerdictFail
		got.Checks[0].Result.Measurements[0].Value = 99
		got.Actor.Claims["role"] = "intruder"

		again, err := w.r.VerificationRuns.Get(ctx, run.ID)
		if err != nil {
			t.Fatalf("Get again: %v", err)
		}
		if again.Verdict() != verifyv1.VerdictPass {
			t.Errorf("the stored run now reads as %q", again.Verdict())
		}
		if again.Checks[0].Result.Measurements[0].Value == 99 {
			t.Error("the stored measurement was edited through the copy")
		}
		if again.Actor.Claims["role"] == "intruder" {
			t.Error("the stored claims were edited through the copy")
		}
	})
}
