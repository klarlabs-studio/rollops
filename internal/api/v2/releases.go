package apiv2

import (
	"cmp"
	"context"
	"slices"
	"time"

	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
)

// Principal is who did something, as the API describes it.
//
// Claims are absent. identity.Principal.Redacted scrubs secret-looking keys,
// but that is a denylist over a map an identity provider fills — a token under
// an unexpected name survives it. Nothing a reader needs is in there, so the
// view does not carry the map at all (INV-012).
type Principal struct {
	ID          string
	Type        string
	DisplayName string
}

// Source is the commit a release was built from.
type Source struct {
	Provider   string
	Repository string
	Revision   string
	Ref        string
	TreeDigest string
	URL        string
}

// DocumentRef points at provenance, an SBOM or a signature. It is a reference:
// the document itself lives wherever it was published, and RollOps stores where
// it is and what it hashes to (ADR-0004).
type DocumentRef struct {
	Kind    string
	Format  string
	Locator string
	Digest  string
}

// ReleaseArtifact binds one artifact to the role it plays in a release.
type ReleaseArtifact struct {
	Role       string
	ArtifactID string
}

// Release is a release as the API describes it.
type Release struct {
	ID          string
	ProjectID   string
	Version     string
	Artifacts   []ReleaseArtifact
	Source      Source
	Provenance  DocumentRef
	CreatedBy   Principal
	CreatedAt   time.Time
	Labels      map[string]string
	Annotations map[string]string
}

// Artifact is an artifact as the API describes it.
type Artifact struct {
	ID         string
	ProjectID  string
	Kind       string
	Digest     string
	Locator    string
	Size       int64
	MediaType  string
	Metadata   map[string]string
	Provenance DocumentRef
	SBOMs      []DocumentRef
	Signatures []DocumentRef
	CreatedAt  time.Time
}

// GetReleaseRequest names one release.
type GetReleaseRequest struct{ ID string }

// GetRelease returns one release.
func (s *Service) GetRelease(ctx context.Context, req GetReleaseRequest) (Release, error) {
	id, err := identity.ParseReleaseID(req.ID)
	if err != nil {
		return Release{}, badArgument("apiv2: release id: %w", err)
	}
	r, err := s.releases.Get(ctx, id)
	if err != nil {
		return Release{}, failure("apiv2: release %s: %w", id, err)
	}
	return viewRelease(r), nil
}

// ListReleasesRequest asks for a page of one project's releases.
type ListReleasesRequest struct {
	ProjectID string
	Page      page.Request
}

// ListReleasesResponse is a page of releases.
type ListReleasesResponse struct {
	Releases []Release
	Next     string
}

// ListReleases returns a page of one project's releases.
func (s *Service) ListReleases(ctx context.Context, req ListReleasesRequest) (ListReleasesResponse, error) {
	projectID, err := identity.ParseProjectID(req.ProjectID)
	if err != nil {
		return ListReleasesResponse{}, badArgument("apiv2: project id: %w", err)
	}
	stored, err := s.releases.List(ctx, projectID)
	if err != nil {
		return ListReleasesResponse{}, failure("apiv2: project %s: releases: %w", projectID, err)
	}
	p, err := page.Of(stored, req.Page, func(r release.Release) string { return string(r.ID) })
	if err != nil {
		return ListReleasesResponse{}, failure("apiv2: project %s: releases: %w", projectID, err)
	}
	return ListReleasesResponse{Releases: mapped(p.Items, viewRelease), Next: p.Next}, nil
}

// GetArtifactRequest names one artifact.
type GetArtifactRequest struct{ ID string }

// GetArtifact returns one artifact.
func (s *Service) GetArtifact(ctx context.Context, req GetArtifactRequest) (Artifact, error) {
	id, err := identity.ParseArtifactID(req.ID)
	if err != nil {
		return Artifact{}, badArgument("apiv2: artifact id: %w", err)
	}
	a, err := s.artifacts.Get(ctx, id)
	if err != nil {
		return Artifact{}, failure("apiv2: artifact %s: %w", id, err)
	}
	return viewArtifact(a), nil
}

// ListArtifactsRequest asks for a page of one project's artifacts.
type ListArtifactsRequest struct {
	ProjectID string
	Page      page.Request
}

// ListArtifactsResponse is a page of artifacts.
type ListArtifactsResponse struct {
	Artifacts []Artifact
	Next      string
}

// ListArtifacts returns a page of one project's artifacts.
func (s *Service) ListArtifacts(ctx context.Context, req ListArtifactsRequest) (ListArtifactsResponse, error) {
	projectID, err := identity.ParseProjectID(req.ProjectID)
	if err != nil {
		return ListArtifactsResponse{}, badArgument("apiv2: project id: %w", err)
	}
	stored, err := s.artifacts.List(ctx, projectID)
	if err != nil {
		return ListArtifactsResponse{}, failure("apiv2: project %s: artifacts: %w", projectID, err)
	}
	p, err := page.Of(stored, req.Page, func(a artifact.Artifact) string { return string(a.ID) })
	if err != nil {
		return ListArtifactsResponse{}, failure("apiv2: project %s: artifacts: %w", projectID, err)
	}
	return ListArtifactsResponse{Artifacts: mapped(p.Items, viewArtifact), Next: p.Next}, nil
}

func viewRelease(r release.Release) Release {
	v := Release{
		ID:          string(r.ID),
		ProjectID:   string(r.ProjectID),
		Version:     r.Version,
		Source:      viewSource(r.Source),
		Provenance:  viewDocument(r.Provenance),
		CreatedBy:   viewPrincipal(r.CreatedBy),
		CreatedAt:   r.CreatedAt,
		Labels:      copyMap(r.Labels),
		Annotations: copyMap(r.Annotations),
	}
	for _, a := range r.Artifacts {
		v.Artifacts = append(v.Artifacts, ReleaseArtifact{Role: a.Role, ArtifactID: string(a.ArtifactID)})
	}
	// Sorted by role for the same reason the desired-state document is: a
	// release lists artifacts in declaration order, and a list that reorders
	// between two reads of an unchanged release reads as a change.
	slices.SortFunc(v.Artifacts, func(a, b ReleaseArtifact) int { return cmp.Compare(a.Role, b.Role) })
	return v
}

func viewArtifact(a artifact.Artifact) Artifact {
	return Artifact{
		ID:         string(a.ID),
		ProjectID:  string(a.ProjectID),
		Kind:       string(a.Kind),
		Digest:     a.Digest.String(),
		Locator:    a.Locator,
		Size:       a.Size,
		MediaType:  a.MediaType,
		Metadata:   copyMap(a.Metadata),
		Provenance: viewDocument(a.Provenance),
		SBOMs:      mapped(a.SBOMs, viewDocument),
		Signatures: mapped(a.Signatures, viewDocument),
		CreatedAt:  a.CreatedAt,
	}
}

func viewSource(s provenance.SourceRevision) Source {
	return Source{
		Provider:   s.Provider,
		Repository: s.Repository,
		Revision:   s.Revision,
		Ref:        s.Ref,
		TreeDigest: s.TreeDigest,
		URL:        s.URL,
	}
}

func viewDocument(d provenance.DocumentRef) DocumentRef {
	return DocumentRef{
		Kind:    string(d.Kind),
		Format:  d.Format,
		Locator: d.Locator,
		Digest:  d.Digest.String(),
	}
}

func viewPrincipal(p identity.Principal) Principal {
	return Principal{ID: p.ID, Type: string(p.Type), DisplayName: p.DisplayName}
}
