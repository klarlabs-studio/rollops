package verification_test

import (
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/verification"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

var (
	began  = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ended  = began.Add(30 * time.Second)
	gen    = identity.NewSequenceGenerator()
	newGen = func() identity.Generator { return gen }
)

func newClock() identity.Clock { return identity.NewFixedClock(ended) }

func author() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

func answered(v verifyv1.Verdict) verification.Check {
	return verification.Check{
		Verifier: verifyv1.VerifierMetadata{Kind: "http", Name: "health", Version: "1"},
		Result: verifyv1.VerificationResult{
			Verdict:    v,
			StartedAt:  began,
			FinishedAt: ended,
		},
	}
}

func draft(checks ...verification.Check) verification.Run {
	return verification.Run{
		DeploymentID: "dpl_1",
		PlanID:       "pln_1",
		Checks:       checks,
		StartedAt:    began,
	}
}

func newRun(t *testing.T, r verification.Run) verification.Run {
	t.Helper()
	out, err := verification.New(newGen(), newClock(), author(), r)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return out
}

func TestANewRunIsStampedWithIdentityAndTheMomentItSettled(t *testing.T) {
	r := newRun(t, draft(answered(verifyv1.VerdictPass)))

	if r.ID == "" {
		t.Error("no id")
	}
	if !strings.HasPrefix(string(r.ID), "vrf_") {
		t.Errorf("id = %q, want a verification run id", r.ID)
	}
	// The run is written when the checks have answered, so the clock is the
	// moment it finished. When it began is the caller's to supply: verification
	// starts a transaction earlier, and re-reading the clock here would report
	// a window of zero however long the checks actually took.
	if !r.FinishedAt.Equal(ended) {
		t.Errorf("FinishedAt = %s, want %s", r.FinishedAt, ended)
	}
	if !r.StartedAt.Equal(began) {
		t.Errorf("StartedAt = %s, want %s", r.StartedAt, began)
	}
}

// The verdict is not a field. A stored one could disagree with the checks it
// was supposed to summarise, and of the two copies nothing says which to
// believe — so there is one copy and it is the checks.
func TestTheVerdictIsReadOffTheChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		checks []verification.Check
		want   verifyv1.Verdict
	}{
		{"everything passed", []verification.Check{
			answered(verifyv1.VerdictPass), answered(verifyv1.VerdictPass),
		}, verifyv1.VerdictPass},
		{"one said no", []verification.Check{
			answered(verifyv1.VerdictPass), answered(verifyv1.VerdictFail),
		}, verifyv1.VerdictFail},
		{"one could not tell", []verification.Check{
			answered(verifyv1.VerdictPass), answered(verifyv1.VerdictInconclusive),
		}, verifyv1.VerdictInconclusive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRun(t, draft(tc.checks...))
			if got := r.Verdict(); got != tc.want {
				t.Errorf("Verdict() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A verification that ran no checks observed nothing, which is not the same as
// observing that everything is well. Recording it is the point: it is the
// evidence that verification was configured to do nothing.
func TestARunWithNoChecksIsRecordedAndIsNotAPass(t *testing.T) {
	r := newRun(t, draft())

	if got := r.Verdict(); got != verifyv1.VerdictInconclusive {
		t.Errorf("Verdict() = %q, want inconclusive", got)
	}
}

// A verifier that answers with something this version does not recognise has
// not said yes. The run records the word it was given rather than refusing to
// store it — what a verifier said is a fact, and dropping it would leave an
// operator with no way to see why the run came out an error.
func TestAnUnrecognisedVerdictIsKeptAndReadsAsAnError(t *testing.T) {
	r := newRun(t, draft(answered("looks-fine-to-me")))

	if got := r.Verdict(); got != verifyv1.VerdictError {
		t.Errorf("Verdict() = %q, want error", got)
	}
	if got := r.Checks[0].Result.Verdict; got != "looks-fine-to-me" {
		t.Errorf("the run rewrote what the verifier said to %q", got)
	}
}

// A run is persisted and rendered on every surface, so a credential that
// reached its actor would be impossible to recall (INV-012).
func TestTheActorsSecretsDoNotSurviveCreation(t *testing.T) {
	by := author()
	by.Claims = map[string]string{"api_key": "sk-live-1234", "team": "platform"}

	r, err := verification.New(newGen(), newClock(), by, draft(answered(verifyv1.VerdictPass)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := r.Actor.Claims["api_key"]; got == "sk-live-1234" {
		t.Error("the api key was stored verbatim")
	}
	if got := r.Actor.Claims["team"]; got != "platform" {
		t.Errorf("team = %q, want platform — redaction took a claim that was not secret", got)
	}
}

func TestARunHasToSayWhatItVerified(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*verification.Run)
	}{
		{"no deployment", func(r *verification.Run) { r.DeploymentID = "" }},
		// Without a plan there is nothing saying what the checks were supposed
		// to be observing, and a verdict nobody can tie to a plan cannot be
		// read back as evidence for anything.
		{"no plan", func(r *verification.Run) { r.PlanID = "" }},
		{"never began", func(r *verification.Run) { r.StartedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := draft(answered(verifyv1.VerdictPass))
			tc.spoil(&d)

			if _, err := verification.New(newGen(), newClock(), author(), d); err == nil {
				t.Fatal("New accepted a run that does not say what it verified")
			}
		})
	}
}

// A check nobody can attribute is a verdict with no author. Two of them
// disagreeing would leave an operator with no way to tell which verifier to go
// and look at.
func TestACheckHasToNameTheVerifierThatGaveIt(t *testing.T) {
	anonymous := answered(verifyv1.VerdictFail)
	anonymous.Verifier.Name = ""

	if _, err := verification.New(newGen(), newClock(), author(), draft(anonymous)); err == nil {
		t.Fatal("New accepted a check with no verifier")
	}
}

// Time only runs forwards. A window that closes before it opens is a clock
// nobody can compute a duration from.
func TestARunCannotFinishBeforeItStarted(t *testing.T) {
	d := draft(answered(verifyv1.VerdictPass))
	d.StartedAt = ended.Add(time.Minute)

	if _, err := verification.New(newGen(), newClock(), author(), d); err == nil {
		t.Fatal("New accepted a run that finished before it started")
	}
}
