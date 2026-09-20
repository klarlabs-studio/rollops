// Package environment declares the places a project's releases can be
// deployed to.
//
// Nothing here talks to a cluster. An environment is a record of what
// infrastructure should be, not a reading of what it is, so one can be declared
// before the thing it names exists (INV-007) — and an environment with no
// target yet is a legitimate record that simply has nowhere to land.
//
// The configuration it carries is where secrets live, and they live in it as
// references: a value.Ref names secret material and never holds it, so nothing
// this package writes can leak one by copying a field (INV-012).
package environment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/value"
)

var (
	// ErrIncomplete reports a service constructed without something it needs.
	ErrIncomplete = errors.New("environment: service is missing a dependency")

	// ErrRejected marks a command the domain would not build a record from.
	//
	// The domain reports what is wrong with the record; it has no opinion about
	// whose fault that is. Every field it validates comes straight off the
	// command, so here the answer is known: the caller's. Without this a
	// transport cannot tell a misspelt kind from a database that fell over, and
	// would answer a typo by telling the operator the system is broken.
	//
	// It wraps rather than replaces, so the specific cause —
	// environment.ErrDuplicateTarget, name.ErrInvalid — is still reachable.
	ErrRejected = errors.New("environment: the command was refused")
)

// Config is what a Service needs.
type Config struct {
	Transactor   port.Transactor
	Projects     port.ProjectRepository
	Environments port.EnvironmentRepository

	// Events is the domain event log. The record is appended in the same
	// transaction as the environment it describes, so the timeline cannot name
	// a place nothing can be deployed to (ADR-0003).
	Events port.EventLog

	Clock identity.Clock
	IDs   identity.Generator
}

// Service creates environments.
type Service struct {
	cfg Config
}

// New returns a service, or reports what it was not given.
func New(cfg Config) (*Service, error) {
	var missing []string
	for _, d := range []struct {
		name    string
		present bool
	}{
		{"transactor", cfg.Transactor != nil},
		{"projects repository", cfg.Projects != nil},
		{"environments repository", cfg.Environments != nil},
		{"events log", cfg.Events != nil},
		{"clock", cfg.Clock != nil},
		{"ids generator", cfg.IDs != nil},
	} {
		if !d.present {
			missing = append(missing, d.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(missing, ", "))
	}
	return &Service{cfg: cfg}, nil
}

// CreateCommand declares a place to deploy to.
type CreateCommand struct {
	ProjectID identity.ProjectID

	// Name is unique within the project and not across the estate: two projects
	// may each have a production.
	Name string

	// Kind is what policy distinguishes environments by, so that one named
	// "prod" is not treated as production by accident — or a production one
	// missed because it is not.
	Kind environment.Kind

	Targets   []environment.TargetBinding
	Policies  []environment.PolicyBinding
	Variables map[string]value.Ref
	Labels    map[string]string
	Lifecycle environment.Lifecycle

	Actor identity.Principal
}

// Create stores the environment and records it.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (environment.Environment, error) {
	if _, err := s.cfg.Projects.Get(ctx, cmd.ProjectID); err != nil {
		return environment.Environment{}, fmt.Errorf("environment: project %s: %w", cmd.ProjectID, err)
	}
	e, err := environment.New(s.cfg.IDs, environment.Environment{
		ProjectID: cmd.ProjectID,
		Name:      cmd.Name,
		Kind:      cmd.Kind,
		Targets:   cmd.Targets,
		Policies:  cmd.Policies,
		Variables: cmd.Variables,
		Labels:    cmd.Labels,
		Lifecycle: cmd.Lifecycle,
	})
	if err != nil {
		return environment.Environment{}, fmt.Errorf("%w: %w", ErrRejected, err)
	}

	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		rev, err := s.cfg.Environments.Create(ctx, e)
		if err != nil {
			return fmt.Errorf("environment: storing %q in %s: %w", e.Name, e.ProjectID, err)
		}
		e.Revision = rev
		return s.record(ctx, cmd.Actor, e)
	})
	if err != nil {
		return environment.Environment{}, err
	}
	return e, nil
}
