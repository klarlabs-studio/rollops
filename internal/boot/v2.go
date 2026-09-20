package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/app/deploy"
	appenv "go.klarlabs.de/rollops/internal/app/environment"
	"go.klarlabs.de/rollops/internal/app/port"
	appproject "go.klarlabs.de/rollops/internal/app/project"
	apprelease "go.klarlabs.de/rollops/internal/app/release"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/engine/desired"
	"go.klarlabs.de/rollops/internal/engine/planner"
	"go.klarlabs.de/rollops/internal/engine/targets"
	"go.klarlabs.de/rollops/internal/policy/gate"
	"go.klarlabs.de/rollops/internal/secrets"
	"go.klarlabs.de/rollops/internal/target"
	"go.klarlabs.de/rollops/internal/verify"
	"go.klarlabs.de/rollops/internal/verify/runner"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

// DefaultPlanLifetime bounds how long a plan stays applicable. An hour is long
// enough for a plan to be read and approved and short enough that the world it
// was computed against has probably not moved underneath it.
const DefaultPlanLifetime = time.Hour

// DefaultApproveAtOrAbove is the risk band the floor gates at when nobody said.
//
// It is not empty. An unbound environment held to nothing is the failure worth
// avoiding: the floor exists precisely for the production environment somebody
// forgot to attach a policy to. It is not low either — a floor that stops every
// deployment is one operators learn to route around, and a gate nobody respects
// gates nothing.
const DefaultApproveAtOrAbove = policy.RiskHigh

// Repositories is the persistence the v2 service reads and writes through.
//
// It is an interface rather than *sqlite.Store so that this package names the
// nine accessors it uses and nothing else. The store also carries the v1
// engine's freeze and lease rows, which the v2 stack has no business reaching.
type Repositories interface {
	port.Transactor

	Projects() port.ProjectRepository
	Environments() port.EnvironmentRepository
	Artifacts() port.ArtifactRepository
	Releases() port.ReleaseRepository
	Plans() port.PlanRepository
	Deployments() port.DeploymentRepository
	VerificationRuns() port.VerificationRunRepository
	Approvals() port.ApprovalRepository
	Idempotency() port.IdempotencyRepository

	// Events is the whole log. The API is handed only its reader half; the
	// three writers below need the appender, and one accessor serving both is
	// what keeps the timeline single.
	Events() port.EventLog
}

// V2Config is the process-wide wiring for the v2 service.
type V2Config struct {
	Store  Repositories
	Getenv Getenv

	// Checks are the verifiers every deployment is measured by. Empty takes
	// verify.Unconfigured, which observes nothing and says so — see V2.
	Checks []verifyv1.Verifier

	// PlanLifetime is how long a plan stays applicable. Zero takes
	// DefaultPlanLifetime.
	PlanLifetime time.Duration

	Log io.Writer // startup notes; nil discards
}

func (c V2Config) getenv(key string) string {
	if c.Getenv == nil {
		return ""
	}
	return c.Getenv(key)
}

func (c V2Config) logf(format string, args ...any) {
	if c.Log == nil {
		return
	}
	_, _ = fmt.Fprintf(c.Log, format, args...)
}

// V2 assembles the service every v2 transport is a projection of.
//
// Like Options, this is the one place the production stack is built, so that
// the CLI and a future daemon cannot be handed different policy floors or
// different check sets. Every step names the first thing it could not build,
// the way apiv2.New and deploy.New do: a dependency quietly left nil is a
// deployment that gets most of the way through and then dereferences.
func V2(ctx context.Context, cfg V2Config) (*apiv2.Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("boot: no store to build the v2 service over")
	}

	prov, err := secrets.FromEnv(cfg.getenv)
	if err != nil {
		return nil, fmt.Errorf("boot: secrets: %w", err)
	}
	resolver, err := secrets.ValueResolver(ctx, prov)
	if err != nil {
		return nil, fmt.Errorf("boot: secret resolver: %w", err)
	}

	tr, err := targets.New(target.Builtin(), resolver)
	if err != nil {
		return nil, fmt.Errorf("boot: target resolver: %w", err)
	}
	dr, err := desired.New(cfg.Store.Artifacts())
	if err != nil {
		return nil, fmt.Errorf("boot: desired state: %w", err)
	}
	pl, err := planner.New(tr, dr)
	if err != nil {
		return nil, fmt.Errorf("boot: planner: %w", err)
	}

	floor := cfg.floor()
	// Policies is empty because nothing loads rule sets from configuration yet.
	// An environment binding a policy by name is therefore refused rather than
	// skipped — gate.Evaluate treats an unresolvable binding as a gate nobody
	// can evaluate, which is the fail-closed reading.
	pol, err := gate.New(gate.Config{Floor: floor})
	if err != nil {
		// The default band is always a band, so a floor gate cannot refuse
		// unless the environment overrode it.
		return nil, fmt.Errorf("boot: policy floor (ROLLOPS_APPROVE_AT_OR_ABOVE): %w", err)
	}
	cfg.logf("rollops: policy floor requires approval at or above risk %s\n", floor.ApproveAtOrAbove)

	checks := cfg.Checks
	if len(checks) == 0 {
		// runner.New refuses an empty set, and rightly: a suite with no checks
		// returns an empty result set, which is indistinguishable from a suite
		// that ran and found nothing wrong. So the absence becomes an answer —
		// inconclusive, which blocks in verifyv1.Combine (§11.3).
		checks = []verifyv1.Verifier{verify.Unconfigured{}}
		cfg.logf("rollops: no verification checks configured; deployments will not promote\n")
	}
	ver, err := runner.New(runner.Config{Checks: checks})
	if err != nil {
		return nil, fmt.Errorf("boot: verification: %w", err)
	}

	clock := identity.ClockFunc(time.Now)
	ids := identity.NewGenerator()

	lifetime := cfg.PlanLifetime
	if lifetime <= 0 {
		lifetime = DefaultPlanLifetime
	}

	deployer, err := deploy.New(deploy.Config{
		Transactor:       cfg.Store,
		Plans:            cfg.Store.Plans(),
		Deployments:      cfg.Store.Deployments(),
		Approvals:        cfg.Store.Approvals(),
		Releases:         cfg.Store.Releases(),
		Environments:     cfg.Store.Environments(),
		Planner:          pl,
		Policy:           pol,
		Verifier:         ver,
		Clock:            clock,
		IDs:              ids,
		Events:           cfg.Store.Events(),
		VerificationRuns: cfg.Store.VerificationRuns(),
		PlanLifetime:     lifetime,
	})
	if err != nil {
		return nil, fmt.Errorf("boot: deployer: %w", err)
	}

	founder, err := appproject.New(appproject.Config{
		Transactor: cfg.Store,
		Projects:   cfg.Store.Projects(),
		Events:     cfg.Store.Events(),
		Clock:      clock,
		IDs:        ids,
	})
	if err != nil {
		return nil, fmt.Errorf("boot: projects: %w", err)
	}

	binder, err := appenv.New(appenv.Config{
		Transactor:   cfg.Store,
		Projects:     cfg.Store.Projects(),
		Environments: cfg.Store.Environments(),
		Events:       cfg.Store.Events(),
		Clock:        clock,
		IDs:          ids,
	})
	if err != nil {
		return nil, fmt.Errorf("boot: environments: %w", err)
	}

	registrar, err := apprelease.New(apprelease.Config{
		Transactor: cfg.Store,
		Projects:   cfg.Store.Projects(),
		Artifacts:  cfg.Store.Artifacts(),
		Releases:   cfg.Store.Releases(),
		Events:     cfg.Store.Events(),
		Clock:      clock,
		IDs:        ids,
	})
	if err != nil {
		return nil, fmt.Errorf("boot: releases: %w", err)
	}

	svc, err := apiv2.New(apiv2.Config{
		Projects:         cfg.Store.Projects(),
		Environments:     cfg.Store.Environments(),
		Releases:         cfg.Store.Releases(),
		Artifacts:        cfg.Store.Artifacts(),
		Deployments:      cfg.Store.Deployments(),
		Plans:            cfg.Store.Plans(),
		VerificationRuns: cfg.Store.VerificationRuns(),
		Events:           cfg.Store.Events(),
		Deployer:         deployer,
		Registrar:        registrar,
		Founder:          founder,
		Binder:           binder,
		Keys:             cfg.Store.Idempotency(),
		Clock:            clock,
	})
	if err != nil {
		return nil, fmt.Errorf("boot: api: %w", err)
	}
	return svc, nil
}

// floor reads the band the policy floor gates at.
//
// The value is handed on unparsed rather than checked here, so that what counts
// as a risk band is decided in one place. gate.New already refuses a
// misspelling and names it; a second vocabulary in this package would be one
// that could fall behind the first, and the way it would fail is by accepting a
// band gate then ranks above critical and never reaches.
func (c V2Config) floor() gate.Rules {
	band := DefaultApproveAtOrAbove
	if v := strings.ToLower(strings.TrimSpace(c.getenv("ROLLOPS_APPROVE_AT_OR_ABOVE"))); v != "" {
		band = policy.RiskLevel(v)
	}
	return gate.Rules{ApproveAtOrAbove: band}
}
