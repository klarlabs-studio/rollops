// Package port declares the interfaces the application layer depends on and
// the storage layer satisfies.
//
// Nothing here mentions SQL, HTTP or any provider: a port that named its
// implementation would make the use cases untestable without it and would tie
// the domain to one transport (INV-006). The interfaces are deliberately
// narrow and one per aggregate rather than a single Store, so that a use case
// declares the two repositories it touches instead of depending on all of them
// (spec 17.1).
package port

import (
	"context"
	"errors"

	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
)

var (
	// ErrNotFound reports that no aggregate has the requested identity.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists reports a write that would duplicate something the
	// domain requires to be unique — a project name, a release version, or an
	// artifact already registered under the same digest (INV-002).
	ErrAlreadyExists = errors.New("already exists")

	// ErrRevisionConflict reports that the aggregate changed between the read
	// and the write. It is distinct from ErrNotFound because the caller's
	// recourse differs: a conflict is worth retrying from a fresh read, a
	// missing aggregate is not.
	ErrRevisionConflict = errors.New("revision conflict")
)

// Transactor runs a unit of work atomically. The application layer says "these
// writes are one unit" and never sees the transaction itself; an implementation
// carries it on the derived context, which is what keeps database/sql out of
// every repository signature (ADR-0003).
//
// Nesting reuses the open transaction rather than opening a savepoint: partial
// rollback of half a command is not a behaviour any use case has asked for.
type Transactor interface {
	WithinTransaction(ctx context.Context, fn func(context.Context) error) error
}

// ProjectRepository persists projects.
type ProjectRepository interface {
	// Create stores a new project. It returns ErrAlreadyExists if the name is
	// taken, because a name is how a project is addressed on every surface.
	Create(ctx context.Context, p project.Project) error

	// Update stores a change to an existing project and returns the revision it
	// committed at. It returns ErrRevisionConflict if p.Revision is not the
	// stored one — including when it is zero, which means the caller did not
	// read the project first.
	Update(ctx context.Context, p project.Project) (identity.Revision, error)

	Get(ctx context.Context, id identity.ProjectID) (project.Project, error)
	GetByName(ctx context.Context, name string) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
}

// EnvironmentRepository persists environments and their target bindings.
type EnvironmentRepository interface {
	// Create stores a new environment. Names are unique within a project, not
	// globally: two projects may each have a "production".
	Create(ctx context.Context, e environment.Environment) error
	Update(ctx context.Context, e environment.Environment) (identity.Revision, error)

	Get(ctx context.Context, id identity.EnvironmentID) (environment.Environment, error)
	GetByName(ctx context.Context, p identity.ProjectID, name string) (environment.Environment, error)
	List(ctx context.Context, p identity.ProjectID) ([]environment.Environment, error)
}

// ArtifactRepository persists artifact metadata — never artifact content. What
// is stored is where the bytes are and what they must hash to (ADR-0004).
//
// There is no Update: an artifact is immutable (INV-002), so the absence of the
// method is the enforcement.
type ArtifactRepository interface {
	// Create registers an artifact. Registering the same digest twice within a
	// project returns ErrAlreadyExists rather than a second row: it is the same
	// artifact, and content is what identifies it.
	Create(ctx context.Context, a artifact.Artifact) error

	Get(ctx context.Context, id identity.ArtifactID) (artifact.Artifact, error)
	GetByDigest(ctx context.Context, p identity.ProjectID, k artifact.Kind, d string) (artifact.Artifact, error)
	List(ctx context.Context, p identity.ProjectID) ([]artifact.Artifact, error)
}

// ReleaseRepository persists releases. Like an artifact a release is immutable
// (INV-003), so there is no Update.
type ReleaseRepository interface {
	// Create stores a release and the roles binding its artifacts. It returns
	// ErrNotFound if an artifact it names is not registered: a release that
	// referred to nothing would be undeployable at exactly the wrong moment.
	Create(ctx context.Context, r release.Release) error

	Get(ctx context.Context, id identity.ReleaseID) (release.Release, error)
	GetByVersion(ctx context.Context, p identity.ProjectID, version string) (release.Release, error)
	List(ctx context.Context, p identity.ProjectID) ([]release.Release, error)

	// FindByFingerprint answers "have we already built this". The fingerprint
	// is recomputed from the stored release rather than trusted, so a row whose
	// recorded fingerprint disagrees with its content is not returned.
	FindByFingerprint(ctx context.Context, p identity.ProjectID, f string) ([]release.Release, error)
}
