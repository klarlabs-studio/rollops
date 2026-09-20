// Package project creates the durable namespace everything else is filed
// under.
//
// It is the first call anybody makes and the smallest service here: a project
// holds no runtime handles, so creating one commits to nothing about where its
// releases will run (INV-007). What it does commit to is a name, which is how
// the project is addressed on every surface — so the name is unique, and a
// second project asking for a taken one is refused rather than reconciled.
package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
)

var (
	// ErrIncomplete reports a service constructed without something it needs.
	ErrIncomplete = errors.New("project: service is missing a dependency")

	// ErrRejected marks a command the domain would not build a record from.
	//
	// The domain reports what is wrong with the record; it has no opinion about
	// whose fault that is. Every field it validates comes straight off the
	// command, so here the answer is known: the caller's. Without this a
	// transport cannot tell a malformed name from a database that fell over,
	// and would answer a typo by telling the operator the system is broken.
	//
	// It wraps rather than replaces, so the specific cause —
	// project.ErrInvalidName — is still reachable.
	ErrRejected = errors.New("project: the command was refused")
)

// Config is what a Service needs.
type Config struct {
	Transactor port.Transactor
	Projects   port.ProjectRepository

	// Events is the domain event log. The record is appended in the same
	// transaction as the project it describes, so the timeline cannot name a
	// namespace nothing can be filed under (ADR-0003).
	Events port.EventLog

	Clock identity.Clock
	IDs   identity.Generator
}

// Service creates projects.
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

// CreateCommand opens a namespace.
type CreateCommand struct {
	// Name is how the project is asked for on every surface, so it is unique
	// and constrained to an alias rather than free text.
	Name        string
	Description string
	Labels      map[string]string

	Actor identity.Principal
}

// Create stores the project and records it.
//
// There is no lookup before the write. A taken name is the uncommon case here,
// unlike a rerun registering the same artifact, and checking first would still
// leave the race the repository settles anyway.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (project.Project, error) {
	p, err := project.New(s.cfg.IDs, s.cfg.Clock, project.Project{
		Name:        cmd.Name,
		Description: cmd.Description,
		Labels:      cmd.Labels,
	})
	if err != nil {
		return project.Project{}, fmt.Errorf("%w: %w", ErrRejected, err)
	}

	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := s.cfg.Projects.Create(ctx, p); err != nil {
			return fmt.Errorf("project: storing %q: %w", p.Name, err)
		}
		return s.record(ctx, cmd.Actor, p)
	})
	if err != nil {
		return project.Project{}, err
	}
	return p, nil
}
