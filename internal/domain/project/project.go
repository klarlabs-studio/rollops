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
	"regexp"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

// ErrInvalidName reports a name that cannot be used as an alias. A name is not
// identity (spec 4.2), but it is what appears in a CLI argument, a URL path and
// a config file, so it must be unambiguous in all three.
var ErrInvalidName = errors.New("project: invalid name")

// maxNameLen keeps a name inside a DNS label, which is the tightest place one
// is likely to end up.
const maxNameLen = 63

// namePattern admits lower case alphanumerics separated by single hyphens. It
// excludes the underscore, which is what keeps a typed ID from being accepted
// as a name.
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

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
	if err := validateName(p.Name); err != nil {
		return err
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

// Rename returns a copy carrying name. A project is mutable, unlike a release,
// but CreatedAt is not — and an edit must move UpdatedAt or a reader cannot
// tell the record changed.
func (p Project) Rename(c identity.Clock, name string) (Project, error) {
	if err := validateName(name); err != nil {
		return Project{}, err
	}
	p.Name = name
	p.UpdatedAt = c.Now()
	return p, nil
}

func validateName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("%w: a project has no name", ErrInvalidName)
	case len(name) > maxNameLen:
		return fmt.Errorf("%w: %q is longer than %d characters", ErrInvalidName, name, maxNameLen)
	case !namePattern.MatchString(name):
		return fmt.Errorf("%w: %q is not lower case alphanumerics separated by hyphens", ErrInvalidName, name)
	}
	return nil
}
