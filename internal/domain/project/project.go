// Package project models the durable namespace for one deliverable or
// cohesive software system.
//
// A project holds no provider-specific runtime handles (spec 4.3): where its
// releases actually run is an environment's concern, so that a project can
// outlive the infrastructure it was first deployed to (INV-007).
package project

import (
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/name"
)

// ErrInvalidName reports a name that cannot be used as an alias.
var ErrInvalidName = name.ErrInvalid

// Project is the durable namespace for one deliverable.
type Project struct {
	ID          identity.ProjectID
	Name        string
	Description string
	Labels      map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// New stamps identity and time onto p, then validates it.
func New(g identity.Generator, c identity.Clock, p Project) (Project, error) {
	id, err := identity.NewProjectID(g)
	if err != nil {
		return Project{}, err
	}
	p.ID = id
	p.CreatedAt = c.Now()
	p.UpdatedAt = p.CreatedAt
	if err := p.Validate(); err != nil {
		return Project{}, err
	}
	return p, nil
}

// Validate reports whether the project is a complete, unambiguous record.
func (p Project) Validate() error {
	if _, err := identity.ParseProjectID(string(p.ID)); err != nil {
		return fmt.Errorf("project: id: %w", err)
	}
	if err := name.Validate("project", p.Name); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if p.CreatedAt.IsZero() {
		return errors.New("project: no creation time")
	}
	if p.UpdatedAt.IsZero() {
		return errors.New("project: no update time")
	}
	if p.UpdatedAt.Before(p.CreatedAt) {
		return errors.New("project: updated before it was created")
	}
	return nil
}

// Rename returns a copy carrying n. A project is mutable, unlike a release,
// but CreatedAt is not — and an edit must move UpdatedAt or a reader cannot
// tell the record changed.
func (p Project) Rename(c identity.Clock, n string) (Project, error) {
	if err := name.Validate("project", n); err != nil {
		return Project{}, fmt.Errorf("project: %w", err)
	}
	p.Name = n
	p.UpdatedAt = c.Now()
	return p, nil
}
