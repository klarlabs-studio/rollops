// Package release registers the artifacts a project has built and fixes them
// into releases that can be deployed.
//
// Nothing here deploys anything. What this package produces is the thing a plan
// is computed against, and it produces it once: an artifact is immutable
// (INV-002) and a release is immutable except for its labels (INV-003), so
// every call either records something new or reports that it already exists.
//
// The isolation boundary is enforced here rather than in storage. A repository
// can see that an artifact exists; only this layer knows that the release
// naming it belongs to a different project, and letting that through would have
// a deployment in one project run bytes built for another.
package release

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
)

var (
	// ErrForeignArtifact reports a release naming an artifact from another
	// project. Projects are the isolation boundary, so this is a refusal rather
	// than something to reconcile.
	ErrForeignArtifact = errors.New("release: artifact belongs to another project")

	// ErrIncomplete reports a service constructed without something it needs.
	ErrIncomplete = errors.New("release: service is missing a dependency")
)

// Config is what a Service needs.
type Config struct {
	Transactor port.Transactor
	Projects   port.ProjectRepository
	Artifacts  port.ArtifactRepository
	Releases   port.ReleaseRepository

	// Events is the domain event log. Each record is appended in the same
	// transaction as the thing it describes, so the timeline cannot name a
	// release nothing can deploy (ADR-0003).
	Events port.EventLog

	Clock identity.Clock
	IDs   identity.Generator
}

// Service registers artifacts and creates releases.
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
		{"artifacts repository", cfg.Artifacts != nil},
		{"releases repository", cfg.Releases != nil},
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

// RegisterArtifactCommand records where a built thing is and what it must hash
// to. The content is not here and never will be: what is stored is metadata
// (ADR-0004).
type RegisterArtifactCommand struct {
	ProjectID identity.ProjectID
	Kind      artifact.Kind
	Digest    digest.Digest

	// Locator has to be pinned to the digest. A tag resolved again at pull time
	// can hand a different image to production than the one that was verified,
	// which is checked by the domain rather than trusted from here.
	Locator   string
	Size      int64
	MediaType string

	Metadata   map[string]string
	Provenance provenance.DocumentRef
	SBOMs      []provenance.DocumentRef
	Signatures []provenance.DocumentRef

	Actor identity.Principal
}

// RegisterArtifact records the artifact, or returns the one already registered.
//
// Content identifies an artifact, so the same digest arriving twice is the same
// artifact arriving twice rather than a conflict: a build that reruns should not
// have to know whether it is the first. The second call records nothing, because
// an artifact that was already registered is not news and a second event would
// have an auditor counting two builds where there was one.
func (s *Service) RegisterArtifact(
	ctx context.Context, cmd RegisterArtifactCommand,
) (artifact.Artifact, error) {
	if _, err := s.cfg.Projects.Get(ctx, cmd.ProjectID); err != nil {
		return artifact.Artifact{}, fmt.Errorf("release: project %s: %w", cmd.ProjectID, err)
	}
	a, err := artifact.New(s.cfg.IDs, s.cfg.Clock, artifact.Artifact{
		ProjectID:  cmd.ProjectID,
		Kind:       cmd.Kind,
		Digest:     cmd.Digest,
		Locator:    cmd.Locator,
		Size:       cmd.Size,
		MediaType:  cmd.MediaType,
		Metadata:   cmd.Metadata,
		Provenance: cmd.Provenance,
		SBOMs:      cmd.SBOMs,
		Signatures: cmd.Signatures,
	})
	if err != nil {
		return artifact.Artifact{}, err
	}

	// Looked up before the write rather than only after it fails, so the common
	// rerun does not burn an id. The write still has to handle the race, and
	// does: two callers registering at once, one loses and reads the winner's.
	existing, err := s.cfg.Artifacts.GetByDigest(ctx, cmd.ProjectID, cmd.Kind, cmd.Digest.String())
	switch {
	case err == nil:
		return existing, nil
	case !errors.Is(err, port.ErrNotFound):
		return artifact.Artifact{}, fmt.Errorf("release: reading the artifact: %w", err)
	}

	var out artifact.Artifact
	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := s.cfg.Artifacts.Create(ctx, a); err != nil {
			if errors.Is(err, port.ErrAlreadyExists) {
				out, err = s.cfg.Artifacts.GetByDigest(ctx, cmd.ProjectID, cmd.Kind, cmd.Digest.String())
				if err != nil {
					return fmt.Errorf("release: reading the artifact: %w", err)
				}
				return nil
			}
			return fmt.Errorf("release: storing the artifact: %w", err)
		}
		if err := s.recordArtifact(ctx, cmd.Actor, a); err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		return artifact.Artifact{}, err
	}
	return out, nil
}

// CreateCommand fixes the set of artifacts a release will deploy.
type CreateCommand struct {
	ProjectID identity.ProjectID

	// Version is how the release is asked for on every surface, so it is unique
	// within the project.
	Version   string
	Artifacts []release.Artifact
	Source    provenance.SourceRevision

	Provenance  provenance.DocumentRef
	Labels      map[string]string
	Annotations map[string]string

	Actor identity.Principal
}

// Create fixes the release and records it.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (release.Release, error) {
	if _, err := s.cfg.Projects.Get(ctx, cmd.ProjectID); err != nil {
		return release.Release{}, fmt.Errorf("release: project %s: %w", cmd.ProjectID, err)
	}
	if err := s.ownsArtifacts(ctx, cmd.ProjectID, cmd.Artifacts); err != nil {
		return release.Release{}, err
	}
	r, err := release.New(s.cfg.IDs, s.cfg.Clock, cmd.Actor, release.Release{
		ProjectID:   cmd.ProjectID,
		Version:     cmd.Version,
		Artifacts:   cmd.Artifacts,
		Source:      cmd.Source,
		Provenance:  cmd.Provenance,
		Labels:      cmd.Labels,
		Annotations: cmd.Annotations,
	})
	if err != nil {
		return release.Release{}, err
	}

	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := s.cfg.Releases.Create(ctx, r); err != nil {
			return fmt.Errorf("release: storing the release: %w", err)
		}
		return s.recordRelease(ctx, cmd.Actor, r)
	})
	if err != nil {
		return release.Release{}, err
	}
	return r, nil
}

// ownsArtifacts checks every artifact exists and belongs to the project. An
// empty set is not this function's refusal: the release validates that, and
// answering it here would report the wrong thing about a release that names
// nothing.
func (s *Service) ownsArtifacts(
	ctx context.Context, p identity.ProjectID, as []release.Artifact,
) error {
	for _, ref := range as {
		a, err := s.cfg.Artifacts.Get(ctx, ref.ArtifactID)
		if err != nil {
			return fmt.Errorf("release: artifact %s: %w", ref.ArtifactID, err)
		}
		if a.ProjectID != p {
			return fmt.Errorf("%w: %s is in %s, not %s", ErrForeignArtifact, a.ID, a.ProjectID, p)
		}
	}
	return nil
}
