// Package memory is an in-memory implementation of the repository ports.
//
// It exists so that a use case can be tested against real repository behaviour
// — including revision conflicts and transaction rollback — without a file on
// disk. It passes the same conformance suite as the SQLite store, which is what
// makes it a stand-in rather than an approximation.
package memory

import (
	"context"
	"maps"
	"slices"
	"sync"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
)

// Store holds every aggregate and hands out the repositories over them. One
// store is one database: repositories taken from the same store see each
// other's writes, and repositories from different stores see nothing.
type Store struct {
	mu        sync.Mutex
	committed *state
}

// New returns an empty store.
func New() *Store {
	return &Store{committed: newState()}
}

type state struct {
	projects     map[identity.ProjectID]project.Project
	environments map[identity.EnvironmentID]environment.Environment
	artifacts    map[identity.ArtifactID]artifact.Artifact
	releases     map[identity.ReleaseID]release.Release
	plans        map[identity.PlanID]plan.DeploymentPlan
	deployments  map[identity.DeploymentID]deployment.Deployment

	// approvals is a slice because approvals are append-only and read in the
	// order they were given: who answered first is part of the record.
	approvals []policy.Approval

	// idempotency is keyed by operation and key together, which is the whole
	// of a record's identity: the same key in two operations is two records.
	idempotency map[idempotencyKey]port.IdempotencyRecord

	// events is a slice rather than a map because the log is ordered and
	// append-only. A position in it is the sequence, so the two cannot drift,
	// and a rolled-back transaction discards its working copy — which is what
	// makes the sequence gapless without a counter to reconcile.
	events []event.Event
}

func newState() *state {
	return &state{
		projects:     map[identity.ProjectID]project.Project{},
		environments: map[identity.EnvironmentID]environment.Environment{},
		artifacts:    map[identity.ArtifactID]artifact.Artifact{},
		releases:     map[identity.ReleaseID]release.Release{},
		plans:        map[identity.PlanID]plan.DeploymentPlan{},
		deployments:  map[identity.DeploymentID]deployment.Deployment{},
		idempotency:  map[idempotencyKey]port.IdempotencyRecord{},
	}
}

// idempotencyKey is a record's identity: keys are the caller's to invent, so
// one operation's key says nothing about another's.
type idempotencyKey struct{ operation, key string }

// clone copies the index but not the aggregates, which are already stored as
// deep copies and are never mutated in place.
func (s *state) clone() *state {
	return &state{
		projects:     maps.Clone(s.projects),
		environments: maps.Clone(s.environments),
		artifacts:    maps.Clone(s.artifacts),
		releases:     maps.Clone(s.releases),
		plans:        maps.Clone(s.plans),
		deployments:  maps.Clone(s.deployments),
		approvals:    slices.Clone(s.approvals),
		idempotency:  maps.Clone(s.idempotency),
		events:       slices.Clone(s.events),
	}
}

type txKey struct{}

func txFrom(ctx context.Context) (*state, bool) {
	st, ok := ctx.Value(txKey{}).(*state)
	return st, ok
}

// WithinTransaction runs fn against a working copy and adopts it only if fn
// succeeds. Nesting reuses the open transaction rather than opening a nested
// one, so an inner failure rolls the whole unit back (ADR-0003).
func (s *Store) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	if _, open := txFrom(ctx); open {
		return fn(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	working := s.committed.clone()
	if err := fn(context.WithValue(ctx, txKey{}, working)); err != nil {
		return err
	}
	s.committed = working
	return nil
}

// read runs fn against the state the caller should see: the open transaction's
// if there is one, the committed state otherwise.
func (s *Store) read(ctx context.Context, fn func(*state) error) error {
	if st, open := txFrom(ctx); open {
		return fn(st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.committed)
}

// write runs fn against a working copy. Outside a transaction the call is its
// own unit of work, so a failure leaves the committed state untouched exactly
// as it would inside one.
func (s *Store) write(ctx context.Context, fn func(*state) error) error {
	if st, open := txFrom(ctx); open {
		return fn(st)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	working := s.committed.clone()
	if err := fn(working); err != nil {
		return err
	}
	s.committed = working
	return nil
}

// Projects returns the project repository over this store.
func (s *Store) Projects() port.ProjectRepository { return projects{s} }

// Environments returns the environment repository over this store.
func (s *Store) Environments() port.EnvironmentRepository { return environments{s} }

// Artifacts returns the artifact repository over this store.
func (s *Store) Artifacts() port.ArtifactRepository { return artifacts{s} }

// Releases returns the release repository over this store.
func (s *Store) Releases() port.ReleaseRepository { return releases{s} }

// Plans returns the deployment plan repository over this store.
func (s *Store) Plans() port.PlanRepository { return plans{s} }

// Deployments returns the deployment repository over this store.
func (s *Store) Deployments() port.DeploymentRepository { return deployments{s} }

// Events returns the domain event log over this store.
// Approvals returns the approval repository.
func (s *Store) Approvals() port.ApprovalRepository { return approvals{s} }

func (s *Store) Events() port.EventLog { return events{s} }

// Idempotency returns the repository of answers already given.
func (s *Store) Idempotency() port.IdempotencyRepository { return idempotency{s} }

// sortedByID returns the values of m ordered by key. Identifiers are UUIDv7, so
// this is creation order — and it is stable, which map iteration is not.
func sortedByID[K ~string, V any](m map[K]V, keep func(V) bool) []V {
	ks := make([]K, 0, len(m))
	for k, v := range m {
		if keep == nil || keep(v) {
			ks = append(ks, k)
		}
	}
	slices.Sort(ks)
	out := make([]V, 0, len(ks))
	for _, k := range ks {
		out = append(out, m[k])
	}
	return out
}
