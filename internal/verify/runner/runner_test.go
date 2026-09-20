package runner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/verify/runner"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// probe is a verifier whose answer the test chooses and whose questions the
// test can read back. Everything asserted about fan-out is asserted from what
// the probes were asked, because what a check is told is the whole of the
// runner's contract with it.
type probe struct {
	meta   verifyv1.VerifierMetadata
	answer func(ctx context.Context, req verifyv1.VerificationRequest) (verifyv1.VerificationResult, error)

	mu   sync.Mutex
	seen []verifyv1.VerificationRequest
}

func named(kind, name string) *probe {
	return &probe{meta: verifyv1.VerifierMetadata{Kind: kind, Name: name, Version: "v1"}}
}

// passing is the probe every test that is not about verdicts uses, so a
// non-pass appearing in one of those came from the runner.
func passing(kind, name string) *probe {
	p := named(kind, name)
	p.answer = func(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
		return verifyv1.VerificationResult{Verdict: verifyv1.VerdictPass}, nil
	}
	return p
}

func (p *probe) Metadata() verifyv1.VerifierMetadata { return p.meta }

func (p *probe) Verify(
	ctx context.Context, req verifyv1.VerificationRequest,
) (verifyv1.VerificationResult, error) {
	p.mu.Lock()
	p.seen = append(p.seen, req)
	p.mu.Unlock()
	return p.answer(ctx, req)
}

func (p *probe) asked() []verifyv1.VerificationRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]verifyv1.VerificationRequest, len(p.seen))
	copy(out, p.seen)
	return out
}

func suite(t *testing.T, checks ...verifyv1.Verifier) *runner.Suite {
	t.Helper()
	s, err := runner.New(runner.Config{Checks: checks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// across builds an environment with one target binding per name. The bindings
// carry no configuration, so a test that wants to prove configuration does not
// escape has to put it there itself.
func across(names ...string) environment.Environment {
	env := environment.Environment{Name: "prod", Kind: environment.KindProduction}
	for _, n := range names {
		env.Targets = append(env.Targets, environment.TargetBinding{Name: n, Driver: "kubernetes"})
	}
	return env
}

func request(env environment.Environment) deploy.VerificationRequest {
	return deploy.VerificationRequest{
		Environment: env,
		Release:     release.Release{ID: "rel_01HQ", Version: "1.4.0"},
		Deployment:  deployment.Deployment{ID: "dep_01HZ"},
	}
}

func run(t *testing.T, s *runner.Suite, req deploy.VerificationRequest) []deploy.CheckResult {
	t.Helper()
	out, err := s.Verify(context.Background(), req)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return out
}

// label renders a result the way an ordering assertion reads best: which check
// answered about which target.
func labels(results []deploy.CheckResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Verifier.Name
	}
	return out
}

func TestTheSuiteIsAVerifier(t *testing.T) {
	var _ deploy.Verifier = (*runner.Suite)(nil)
}

func TestEveryCheckIsAskedAboutEveryTarget(t *testing.T) {
	health, smoke := passing("health", "health"), passing("command", "smoke")
	results := run(t, suite(t, health, smoke), request(across("us-east", "eu-west")))

	if len(results) != 4 {
		t.Fatalf("got %d answers, want 4 (2 checks across 2 targets)", len(results))
	}
	// Compared as a set: checks run concurrently, so which one reaches its
	// probe first is not a promise. What the suite does promise about order it
	// promises about the answers, which TestAnswersComeOutInTheSameOrderEveryTime
	// covers.
	for _, p := range []*probe{health, smoke} {
		var targets []string
		for _, req := range p.asked() {
			targets = append(targets, req.Target)
		}
		slices.Sort(targets)
		if want := []string{"eu-west", "us-east"}; !equal(targets, want) {
			t.Errorf("%s was asked about %v, want %v", p.meta.Name, targets, want)
		}
	}
}

// deploy.CheckResult has nowhere to record which target an answer was about,
// so the instance name carries it. Without that, one check across two targets
// produces two answers an operator cannot tell apart, which is exactly what
// naming the check that failed was supposed to prevent.
func TestAnAnswerNamesTheCheckAndTheTargetItWasAbout(t *testing.T) {
	results := run(t, suite(t, passing("health", "health")), request(across("us-east")))
	want := verifyv1.VerifierMetadata{Kind: "health", Name: "health@us-east", Version: "v1"}
	if results[0].Verifier != want {
		t.Errorf("answer attributed to %+v, want %+v", results[0].Verifier, want)
	}
}

func TestTwoAnswersFromOneCheckCanBeToldApart(t *testing.T) {
	results := run(t, suite(t, passing("health", "health")), request(across("us-east", "eu-west")))
	if got := labels(results); !equal(got, []string{"health@us-east", "health@eu-west"}) {
		t.Fatalf("got %v, want each answer to name its target", got)
	}
	// The kind is left alone, so a surface grouping every health check together
	// still can.
	for _, r := range results {
		if r.Verifier.Kind != "health" {
			t.Errorf("kind was rewritten to %q", r.Verifier.Kind)
		}
	}
}

// The release identifies the desired state; the deployment identifies the
// attempt at it. A check asking a substrate what is serving can only be
// answered against the first.
func TestACheckIsToldTheReleaseRatherThanTheAttempt(t *testing.T) {
	p := passing("health", "health")
	req := request(across("us-east"))
	run(t, suite(t, p), req)

	got := p.asked()[0].Revision
	if got != string(req.Release.ID) {
		t.Errorf("check was told revision %q, want the release %q", got, req.Release.ID)
	}
	if got == string(req.Deployment.ID) {
		t.Error("check was told the deployment id, which names the attempt and not the desired state")
	}
}

func TestAnswersComeOutInTheSameOrderEveryTime(t *testing.T) {
	// Each probe finishes in the reverse of the order it is declared in, so a
	// runner that appended results as they arrived would return them backwards.
	slow := func(kind, name string, d time.Duration) *probe {
		p := named(kind, name)
		p.answer = func(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
			time.Sleep(d)
			return verifyv1.VerificationResult{Verdict: verifyv1.VerdictPass}, nil
		}
		return p
	}
	s := suite(t, slow("a", "first", 40*time.Millisecond), slow("b", "second", 0))
	results := run(t, s, request(across("us-east", "eu-west")))

	// Target-major: an operator reading a timeline asks which target is
	// broken, so every answer about one target sits together.
	want := []string{"first@us-east", "second@us-east", "first@eu-west", "second@eu-west"}
	if got := labels(results); !equal(got, want) {
		t.Errorf("answers came out as %v, want %v", got, want)
	}
}

// A verifier returning an error has said the check broke, which is a verdict in
// the vocabulary. Aborting the run instead would throw away every other check's
// answer over one broken one.
func TestACheckThatBrokeIsAnAnswerRatherThanAnAbortedRun(t *testing.T) {
	broken := named("metrics", "analysis")
	broken.answer = func(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
		return verifyv1.VerificationResult{}, errors.New("prometheus refused the connection")
	}
	results := run(t, suite(t, broken, passing("health", "health")), request(across("us-east")))

	if len(results) != 2 {
		t.Fatalf("got %d answers, want 2: a broken check must not silence the others", len(results))
	}
	got := results[0].Result
	if got.Verdict != verifyv1.VerdictError {
		t.Errorf("a check that broke reported %q, want %q", got.Verdict, verifyv1.VerdictError)
	}
	if !strings.Contains(got.Reason, "prometheus refused the connection") {
		t.Errorf("the reason %q does not say what broke", got.Reason)
	}
	if got.StartedAt.IsZero() || got.FinishedAt.IsZero() {
		t.Error("a synthesised answer has no window, so it cannot be lined up against the deploy")
	}
	if results[1].Result.Verdict != verifyv1.VerdictPass {
		t.Errorf("the other check reported %q, want it to have run normally", results[1].Result.Verdict)
	}
}

// Combining nothing is inconclusive, which blocks. Returning no answers is
// therefore the honest report for an environment with nothing to look at —
// an error would abort the run and record that nothing was ever asked.
func TestAnEnvironmentWithNoTargetsIsVerifiedByNothing(t *testing.T) {
	results := run(t, suite(t, passing("health", "health")), request(across()))
	if len(results) != 0 {
		t.Fatalf("got %d answers, want none", len(results))
	}
	if v := verifyv1.Combine(); v != verifyv1.VerdictInconclusive {
		t.Fatalf("combining nothing is %q, so returning nothing would read as a pass", v)
	}
}

func TestACancelledRunStillAnswersForEveryCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := suite(t, passing("health", "health"), passing("command", "smoke"))
	results, err := s.Verify(ctx, request(across("us-east", "eu-west")))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("got %d answers, want 4: a check with no answer is one that silently vanished", len(results))
	}
	for _, r := range results {
		if r.Result.Verdict != verifyv1.VerdictCancelled {
			t.Errorf("%s reported %q, want %q", r.Verifier.Name, r.Result.Verdict, verifyv1.VerdictCancelled)
		}
	}
}

func TestAnExpiredDeadlineIsInconclusiveRatherThanCancelled(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	results, err := suite(t, passing("health", "health")).Verify(ctx, request(across("us-east")))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := results[0].Result.Verdict; got != verifyv1.VerdictInconclusive {
		t.Errorf("an expired deadline reported %q, want %q", got, verifyv1.VerdictInconclusive)
	}
}

// INV-012. A target is configured with credentials, and a check's answer is
// stored in a verification run and rendered on every surface that shows one.
func TestNoTargetConfigurationReachesAnAnswer(t *testing.T) {
	const literal = "THE-VALUE-NO-OPERATOR-MAY-LEAK"
	const secretName = "prod/kubeconfig"

	env := across("us-east")
	env.Targets[0].Config = map[string]value.Ref{
		"token":      value.Literal(literal),
		"kubeconfig": value.Secret(secretName),
	}
	env.Variables = map[string]value.Ref{"db": value.Literal(literal)}

	// The check reports everything it was told, so anything the runner handed
	// it lands in the answer where the assertion can find it.
	echo := named("http", "probe")
	echo.answer = func(_ context.Context, req verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
		return verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictPass,
			Reason:  fmt.Sprintf("target=%s revision=%s", req.Target, req.Revision),
		}, nil
	}
	results := run(t, suite(t, echo), request(env))

	rendered := fmt.Sprintf("%+v", results)
	if strings.Contains(rendered, literal) {
		t.Errorf("a configured value reached an answer: %s", rendered)
	}
	if strings.Contains(rendered, secretName) {
		t.Errorf("a secret reference reached an answer: %s", rendered)
	}
	for _, req := range echo.asked() {
		if strings.Contains(req.Target+req.Revision, literal) {
			t.Errorf("a configured value reached a check: %+v", req)
		}
	}
}

func TestChecksRunConcurrently(t *testing.T) {
	// Every probe waits for all four to arrive. A runner that ran them one at a
	// time would deadlock until the test's deadline rather than fail an
	// assertion about a duration, which no amount of load makes flaky.
	const want = 4
	arrived := make(chan struct{}, want)
	release := make(chan struct{})
	block := func(kind, name string) *probe {
		p := named(kind, name)
		p.answer = func(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
			arrived <- struct{}{}
			<-release
			return verifyv1.VerificationResult{Verdict: verifyv1.VerdictPass}, nil
		}
		return p
	}
	s := suite(t, block("a", "first"), block("b", "second"))

	done := make(chan []deploy.CheckResult, 1)
	go func() {
		out, _ := s.Verify(context.Background(), request(across("us-east", "eu-west")))
		done <- out
	}()

	for i := range want {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d checks had started; they are not running concurrently", i, want)
		}
	}
	close(release)
	if got := <-done; len(got) != want {
		t.Fatalf("got %d answers, want %d", len(got), want)
	}
}

func TestNoMoreChecksRunAtOnceThanConfigured(t *testing.T) {
	var live, peak atomic.Int64
	counting := func(kind, name string) *probe {
		p := named(kind, name)
		p.answer = func(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
			n := live.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			live.Add(-1)
			return verifyv1.VerificationResult{Verdict: verifyv1.VerdictPass}, nil
		}
		return p
	}
	s, err := runner.New(runner.Config{
		Checks:      []verifyv1.Verifier{counting("a", "first"), counting("b", "second")},
		MaxParallel: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Verify(context.Background(), request(across("a", "b", "c", "d"))); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := peak.Load(); got > 2 {
		t.Errorf("%d checks ran at once, want at most 2", got)
	}
}

func TestASuiteWithNoChecksIsRefusedAtStartup(t *testing.T) {
	_, err := runner.New(runner.Config{})
	if !errors.Is(err, runner.ErrUnusableChecks) {
		t.Fatalf("New: %v, want %v", err, runner.ErrUnusableChecks)
	}
}

func TestACheckThatIsNotThereIsRefusedAtStartup(t *testing.T) {
	_, err := runner.New(runner.Config{Checks: []verifyv1.Verifier{passing("health", "health"), nil}})
	if !errors.Is(err, runner.ErrUnusableChecks) {
		t.Fatalf("New: %v, want %v", err, runner.ErrUnusableChecks)
	}
}

func TestTwoChecksWithTheSameNameAreRefusedAtStartup(t *testing.T) {
	_, err := runner.New(runner.Config{
		Checks: []verifyv1.Verifier{passing("health", "health"), passing("health", "health")},
	})
	if !errors.Is(err, runner.ErrUnusableChecks) {
		t.Fatalf("New: %v, want %v", err, runner.ErrUnusableChecks)
	}
}

func TestACheckThatCannotSayWhatItIsIsRefusedAtStartup(t *testing.T) {
	for _, c := range []struct{ kind, name string }{{"health", " "}, {"", "health"}} {
		_, err := runner.New(runner.Config{Checks: []verifyv1.Verifier{passing(c.kind, c.name)}})
		if !errors.Is(err, runner.ErrUnusableChecks) {
			t.Errorf("New(%q/%q): %v, want %v", c.kind, c.name, err, runner.ErrUnusableChecks)
		}
	}
}

func TestANegativeParallelLimitIsRefusedAtStartup(t *testing.T) {
	_, err := runner.New(runner.Config{
		Checks:      []verifyv1.Verifier{passing("health", "health")},
		MaxParallel: -1,
	})
	if !errors.Is(err, runner.ErrUnusableChecks) {
		t.Fatalf("New: %v, want %v", err, runner.ErrUnusableChecks)
	}
}

// Two builds of one check are one check. Letting Version tell them apart would
// admit a pair that reports every answer twice under one name.
func TestTheSameCheckAtTwoVersionsIsStillTheSameCheck(t *testing.T) {
	a, b := passing("health", "health"), passing("health", "health")
	b.meta.Version = "v2"
	_, err := runner.New(runner.Config{Checks: []verifyv1.Verifier{a, b}})
	if !errors.Is(err, runner.ErrUnusableChecks) {
		t.Fatalf("New: %v, want %v", err, runner.ErrUnusableChecks)
	}
}

func TestTheSameKindMayBeConfiguredTwiceUnderDifferentNames(t *testing.T) {
	s := suite(t, passing("http", "storefront"), passing("http", "admin"))
	want := []string{"storefront@us-east", "admin@us-east"}
	if got := labels(run(t, s, request(across("us-east")))); !equal(got, want) {
		t.Errorf("got %v, want both instances asked", got)
	}
}

// A verdict the plugin chose is passed through untouched: combining is
// deploy.Service's job, and a runner that promoted an inconclusive to a fail —
// or demoted it to a pass — would be making that decision twice.
func TestAVerdictIsPassedThroughUnchanged(t *testing.T) {
	for _, want := range verifyv1.Verdicts() {
		t.Run(string(want), func(t *testing.T) {
			p := named("health", "health")
			p.answer = func(context.Context, verifyv1.VerificationRequest) (verifyv1.VerificationResult, error) {
				return verifyv1.VerificationResult{Verdict: want, Reason: "as configured"}, nil
			}
			results := run(t, suite(t, p), request(across("us-east")))
			if got := results[0].Result.Verdict; got != want {
				t.Errorf("verdict %q came back as %q", want, got)
			}
			if results[0].Result.Reason != "as configured" {
				t.Errorf("reason was rewritten to %q", results[0].Result.Reason)
			}
		})
	}
}

func TestTheConfiguredChecksAreCopiedSoALaterEditCannotChangeThem(t *testing.T) {
	checks := []verifyv1.Verifier{passing("health", "health")}
	s, err := runner.New(runner.Config{Checks: checks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	checks[0] = passing("command", "smoke")

	if got := labels(run(t, s, request(across("us-east")))); !equal(got, []string{"health@us-east"}) {
		t.Errorf("got %v, want the suite to hold the checks it was built with", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
