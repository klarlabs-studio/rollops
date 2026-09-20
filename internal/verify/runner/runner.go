// Package runner asks an environment's checks whether a deployment is serving
// and reports every answer (§11).
//
// It stands behind deploy.Verifier the way gate stands behind
// deploy.PolicyEngine: what a check concludes is the plugin's business, what
// the conclusion then costs is deploy.Service's, and the whole of this
// package's job is to ask each check the right question and lose none of the
// replies.
//
// It combines nothing on purpose. verifyv1.Combine is where §11.3's
// "inconclusive MUST NOT silently become pass" lives, and reducing the answers
// here would put a second copy of that rule inside an adapter — where getting
// it wrong would look like a check that passed rather than like a rule that
// was broken.
//
// The checks are bound at construction, because an environment does not
// declare them: it declares where a deployment lands. So the fan-out is one
// check per target, which is also why an answer names the target it came from
// (see qualify).
package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/rollops/internal/app/deploy"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// DefaultMaxParallel bounds the fan-out when a configuration does not. It is
// small because the ceiling that matters is not this process — it is whatever
// each check talks to, and a verification is not an excuse to load-test the
// cluster it is supposed to be observing.
const DefaultMaxParallel = 8

// ErrUnusableChecks reports configuration that could not verify anything. It is
// returned from New rather than discovered on the first deployment of the day,
// for the reason gate gives about rules: a check that silently does nothing is
// indistinguishable from a check nobody configured.
var ErrUnusableChecks = errors.New("runner: checks cannot be run as written")

// Config is what the suite runs.
type Config struct {
	// Checks are the verifiers in force. Each is bound to its own
	// configuration — a URL, a query, a command — before any deployment is
	// known; what the suite supplies per call is which target to ask about and
	// which release is meant to be answering.
	Checks []verifyv1.Verifier

	// MaxParallel bounds how many checks are in flight at once. An environment
	// is a fleet and the fan-out multiplies by its targets, so unbounded, a
	// verification across fifty of them opens fifty conversations at once and
	// the checks become the incident. Zero takes DefaultMaxParallel.
	MaxParallel int
}

// Suite runs Config against a deployment. It is immutable once built and safe
// to share: one is constructed at startup and used by every verification.
type Suite struct {
	checks []verifyv1.Verifier
	limit  int
}

var _ deploy.Verifier = (*Suite)(nil)

// New returns a suite, or reports the check that could not be run.
func New(cfg Config) (*Suite, error) {
	if len(cfg.Checks) == 0 {
		return nil, fmt.Errorf("%w: no checks are configured, so nothing could ever verify", ErrUnusableChecks)
	}
	if cfg.MaxParallel < 0 {
		return nil, fmt.Errorf("%w: %d checks may run at once", ErrUnusableChecks, cfg.MaxParallel)
	}
	seen := make(map[verifyv1.VerifierMetadata]struct{}, len(cfg.Checks))
	for i, c := range cfg.Checks {
		if c == nil {
			return nil, fmt.Errorf("%w: check %d is missing", ErrUnusableChecks, i)
		}
		meta := c.Metadata()
		if strings.TrimSpace(meta.Kind) == "" || strings.TrimSpace(meta.Name) == "" {
			// An answer is attributed by kind and name. One with neither
			// cannot be told from any other, which is the one thing the
			// timeline exists to do.
			return nil, fmt.Errorf("%w: check %d reports itself as %q/%q", ErrUnusableChecks, i, meta.Kind, meta.Name)
		}
		// Version is left out of the key: two builds of one check are the same
		// check, and an environment that ran both would still be reporting one
		// answer under two identities.
		key := verifyv1.VerifierMetadata{Kind: meta.Kind, Name: meta.Name}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("%w: %s/%s is configured twice", ErrUnusableChecks, meta.Kind, meta.Name)
		}
		seen[key] = struct{}{}
	}
	limit := cfg.MaxParallel
	if limit == 0 {
		limit = DefaultMaxParallel
	}
	s := &Suite{checks: make([]verifyv1.Verifier, len(cfg.Checks)), limit: limit}
	copy(s.checks, cfg.Checks)
	return s, nil
}

// Verify asks every check about every target the environment declares.
//
// It never fails the run. deploy.Service reads an error from here as the
// machinery having broken and abandons the verification without recording what
// anything observed, so a check that could not answer becomes an answer saying
// so (see ask) rather than a reason to discard the ones that could.
//
// An environment with no targets is verified by nothing, and nothing is
// returned. That is not a gap: verifyv1.Combine reduces an empty set to
// inconclusive, which blocks, so the deployment stops on the honest ground
// that nobody looked.
func (s *Suite) Verify(ctx context.Context, req deploy.VerificationRequest) ([]deploy.CheckResult, error) {
	targets := req.Environment.Targets
	out := make([]deploy.CheckResult, len(targets)*len(s.checks))
	if len(out) == 0 {
		return nil, nil
	}

	// The release names the desired state; the deployment names the attempt at
	// it. A substrate has never heard of our attempts, and two of them at the
	// same release are asking the same question — so a check measuring what is
	// live is answering about the release or about nothing we can attribute.
	revision := string(req.Release.ID)

	// Results are written into fixed positions rather than appended, so the
	// order is decided by the configuration and not by which check happened to
	// answer first. A timeline that reorders between runs is one nobody can
	// diff. Target-major, because an operator reading it is asking which
	// target is broken.
	var wg sync.WaitGroup
	inFlight := make(chan struct{}, s.limit)
	for ti, target := range targets {
		for ci, check := range s.checks {
			meta := check.Metadata()
			meta.Name = qualify(meta.Name, target.Name)

			slot := ti*len(s.checks) + ci
			out[slot] = deploy.CheckResult{Verifier: meta}

			wg.Add(1)
			go func() {
				defer wg.Done()
				inFlight <- struct{}{}
				defer func() { <-inFlight }()
				// Only the target's name is passed on. A binding also holds the
				// credentials it connects with, and a check's answer is stored
				// in a verification run and rendered wherever one is shown
				// (INV-012).
				out[slot].Result = ask(ctx, check, verifyv1.VerificationRequest{
					Target:   target.Name,
					Revision: revision,
				})
			}()
		}
	}
	wg.Wait()
	return out, nil
}

// ask runs one check and turns anything that is not an answer into one.
//
// A verifier returning an error has said the check itself broke, which
// verifyv1 already has a verdict for. Propagating it instead would abandon the
// whole verification over one unreachable backend, and VerdictError blocks in
// Combine exactly as a fail does — so nothing is let through by declining to
// panic about it here.
func ask(
	ctx context.Context, v verifyv1.Verifier, req verifyv1.VerificationRequest,
) verifyv1.VerificationResult {
	started := time.Now()
	// Checked before dispatching, not only inside each plugin, so a run
	// abandoned partway still answers for the checks that had not begun. A
	// check with no answer is one that silently vanished, and Combine would
	// then be reducing a set that is quietly short of what was configured.
	if verdict := verifyv1.Interrupted(ctx); verdict != "" {
		return window(started, verifyv1.VerificationResult{Verdict: verdict, Reason: ctx.Err().Error()})
	}
	res, err := v.Verify(ctx, req)
	if err != nil {
		return window(started, verifyv1.VerificationResult{
			Verdict: verifyv1.VerdictError,
			Reason:  "check did not complete: " + err.Error(),
		})
	}
	// A verdict the check chose is passed through untouched, measurements,
	// reason, window and all. Rewriting any of it here would be this package
	// deciding what was observed, which is the plugin's answer to give.
	return res
}

// qualify names the instance that answered.
//
// deploy.CheckResult records which verifier spoke and not which target it was
// asked about, so one check across three targets would otherwise return three
// answers under one name — and the attribution that exists so an operator need
// not read every check would point at all of them equally.
//
// The shape is not meant to be split apart again. A surface that needs the
// target should be handed one rather than taught to parse a name.
func qualify(check, target string) string { return check + "@" + target }

// window stamps a result the suite synthesised, so an answer it wrote itself
// can be lined up against the deploy alongside the ones the checks returned.
func window(started time.Time, res verifyv1.VerificationResult) verifyv1.VerificationResult {
	res.StartedAt, res.FinishedAt = started, time.Now()
	return res
}
