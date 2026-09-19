// Package apiv2 is the service layer every transport is a projection of.
//
// §23.1 asks for the semantics to be settled once and then exposed
// consistently through gRPC, HTTP/JSON, MCP and the CLI. That ordering is the
// whole point of this package: a rule written in an HTTP handler is a rule the
// MCP tool does not have, and four transports that each decide what a missing
// project means will eventually disagree about it. Everything here takes a
// request value and returns a response value, so a transport's only job is to
// carry them.
//
// What comes back is a view, never the domain aggregate. Two reasons, and the
// second is the one that would hurt. §24 asks that internal schemas stay
// internal, so that renaming a domain field is not a breaking API change. More
// sharply: an environment's target bindings and variables hold value.Ref, and
// a Ref may name a secret. Rendering the whole aggregate would put the estate's
// credential layout on the wire (INV-012), so a view names a configuration key
// and never what it resolves to — the same reason
// environment.TargetBinding.String omits it.
//
// Every failure leaves as an *apierr.Error, so a transport maps one code set
// rather than re-reading domain sentinels it should not know about (§23.4).
package apiv2

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
)

// Config names what the service reads through. Every field is required: a
// missing repository is a nil dereference on the first call that needs it, and
// the composition root is the only place that can tell it was forgotten.
type Config struct {
	Projects     port.ProjectRepository
	Environments port.EnvironmentRepository
	Releases     port.ReleaseRepository
	Artifacts    port.ArtifactRepository
}

// Service answers the v2 API.
type Service struct {
	projects     port.ProjectRepository
	environments port.EnvironmentRepository
	releases     port.ReleaseRepository
	artifacts    port.ArtifactRepository
}

// New returns a service, naming the first dependency it was not given.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Projects == nil:
		return nil, errors.New("apiv2: no project repository")
	case cfg.Environments == nil:
		return nil, errors.New("apiv2: no environment repository")
	case cfg.Releases == nil:
		return nil, errors.New("apiv2: no release repository")
	case cfg.Artifacts == nil:
		return nil, errors.New("apiv2: no artifact repository")
	}
	return &Service{
		projects:     cfg.Projects,
		environments: cfg.Environments,
		releases:     cfg.Releases,
		artifacts:    cfg.Artifacts,
	}, nil
}

// Project is a project as the API describes it.
type Project struct {
	ID          string
	Name        string
	Description string
	Labels      map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Revision is what an update must be built on. §24 asks for it wherever
	// optimistic concurrency applies, and it is only useful if a reader gets it
	// — a caller that has to fetch the revision separately has already raced.
	Revision uint64
}

// TargetBinding names a substrate an environment deploys to.
//
// Config lists the keys the binding sets, sorted, and nothing else. What they
// resolve to is a credential as often as not (INV-012), and the names alone
// answer the question a reader actually has: whether this environment
// configures the thing they expected.
type TargetBinding struct {
	Name   string
	Driver string
	Config []string
	Labels map[string]string
}

// PolicyBinding is a policy in force for an environment.
type PolicyBinding struct {
	Name string
	Ref  string
	Mode string
}

// Lifecycle is how long an environment is meant to last.
type Lifecycle struct {
	TTL           time.Duration
	DeleteOnClose bool
}

// Environment is an environment as the API describes it.
type Environment struct {
	ID        string
	ProjectID string
	Name      string
	Kind      string
	Targets   []TargetBinding
	Policies  []PolicyBinding

	// Variables lists the variable names the environment sets, sorted. See
	// TargetBinding.Config for why the values are not here.
	Variables []string

	Labels    map[string]string
	Lifecycle Lifecycle
	Revision  uint64
}

// GetProjectRequest names one project.
type GetProjectRequest struct{ ID string }

// GetProject returns one project.
func (s *Service) GetProject(ctx context.Context, req GetProjectRequest) (Project, error) {
	id, err := identity.ParseProjectID(req.ID)
	if err != nil {
		return Project{}, badArgument("apiv2: project id: %w", err)
	}
	p, err := s.projects.Get(ctx, id)
	if err != nil {
		return Project{}, failure("apiv2: project %s: %w", id, err)
	}
	return viewProject(p), nil
}

// ListProjectsRequest asks for a page of projects.
type ListProjectsRequest struct{ Page page.Request }

// ListProjectsResponse is a page of projects.
type ListProjectsResponse struct {
	Projects []Project
	Next     string
}

// ListProjects returns a page of projects.
func (s *Service) ListProjects(ctx context.Context, req ListProjectsRequest) (ListProjectsResponse, error) {
	stored, err := s.projects.List(ctx)
	if err != nil {
		return ListProjectsResponse{}, failure("apiv2: projects: %w", err)
	}
	p, err := page.Of(stored, req.Page, func(p project.Project) string { return string(p.ID) })
	if err != nil {
		return ListProjectsResponse{}, failure("apiv2: projects: %w", err)
	}
	return ListProjectsResponse{Projects: mapped(p.Items, viewProject), Next: p.Next}, nil
}

// GetEnvironmentRequest names one environment.
type GetEnvironmentRequest struct{ ID string }

// GetEnvironment returns one environment.
func (s *Service) GetEnvironment(ctx context.Context, req GetEnvironmentRequest) (Environment, error) {
	id, err := identity.ParseEnvironmentID(req.ID)
	if err != nil {
		return Environment{}, badArgument("apiv2: environment id: %w", err)
	}
	e, err := s.environments.Get(ctx, id)
	if err != nil {
		return Environment{}, failure("apiv2: environment %s: %w", id, err)
	}
	return viewEnvironment(e), nil
}

// ListEnvironmentsRequest asks for a page of one project's environments.
//
// The project is required rather than optional. Environment names are unique
// within a project and not across them, so a list spanning projects would show
// several rows called "production" with nothing to tell them apart.
type ListEnvironmentsRequest struct {
	ProjectID string
	Page      page.Request
}

// ListEnvironmentsResponse is a page of environments.
type ListEnvironmentsResponse struct {
	Environments []Environment
	Next         string
}

// ListEnvironments returns a page of one project's environments.
func (s *Service) ListEnvironments(ctx context.Context, req ListEnvironmentsRequest) (ListEnvironmentsResponse, error) {
	projectID, err := identity.ParseProjectID(req.ProjectID)
	if err != nil {
		return ListEnvironmentsResponse{}, badArgument("apiv2: project id: %w", err)
	}
	stored, err := s.environments.List(ctx, projectID)
	if err != nil {
		return ListEnvironmentsResponse{}, failure("apiv2: project %s: environments: %w", projectID, err)
	}
	p, err := page.Of(stored, req.Page, func(e environment.Environment) string { return string(e.ID) })
	if err != nil {
		return ListEnvironmentsResponse{}, failure("apiv2: project %s: environments: %w", projectID, err)
	}
	return ListEnvironmentsResponse{Environments: mapped(p.Items, viewEnvironment), Next: p.Next}, nil
}

// failure classifies a repository error while keeping the context that says
// which read produced it. The context is for the log: apierr.From withholds an
// unclassified message from the caller, because a driver error routinely names
// a host, a query or a path (INV-012).
func failure(format string, args ...any) error {
	return apierr.From(fmt.Errorf(format, args...))
}

// badArgument marks a malformed identifier as the caller's mistake, rather
// than letting it become a lookup. An id of the wrong kind is well-formed
// enough to query with, and a not-found answer would send the caller looking
// for a resource that was never addressable at that name.
//
// It builds a fresh Error each time rather than wrapping a shared sentinel:
// the cause is what makes the message useful, and errors.Is still reaches
// identity.ErrWrongKind through the chain.
func badArgument(format string, args ...any) *apierr.Error {
	err := fmt.Errorf(format, args...)
	return &apierr.Error{Code: apierr.InvalidArgument, Message: err.Error(), Err: err}
}

func viewProject(p project.Project) Project {
	return Project{
		ID:          string(p.ID),
		Name:        p.Name,
		Description: p.Description,
		Labels:      copyMap(p.Labels),
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Revision:    uint64(p.Revision),
	}
}

func viewEnvironment(e environment.Environment) Environment {
	v := Environment{
		ID:        string(e.ID),
		ProjectID: string(e.ProjectID),
		Name:      e.Name,
		Kind:      string(e.Kind),
		Variables: sortedKeys(e.Variables),
		Labels:    copyMap(e.Labels),
		Lifecycle: Lifecycle{TTL: e.Lifecycle.TTL, DeleteOnClose: e.Lifecycle.DeleteOnClose},
		Revision:  uint64(e.Revision),
	}
	for _, t := range e.Targets {
		v.Targets = append(v.Targets, TargetBinding{
			Name:   t.Name,
			Driver: t.Driver,
			Config: sortedKeys(t.Config),
			Labels: copyMap(t.Labels),
		})
	}
	for _, p := range e.Policies {
		v.Policies = append(v.Policies, PolicyBinding{Name: p.Name, Ref: p.Ref, Mode: string(p.Mode)})
	}
	return v
}

func mapped[In, Out any](in []In, f func(In) Out) []Out {
	if in == nil {
		return nil
	}
	out := make([]Out, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

// sortedKeys names what a map holds without saying what it holds it for. Sorted
// because map order is random, and a list that reorders between two reads of an
// unchanged environment reads as a change.
func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func copyMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
