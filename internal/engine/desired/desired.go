// Package desired turns a release into the state a target should converge on.
//
// It is the planner's Desired port, and the document it produces is the whole
// of what RollOps knows a deployment to be: this release, these artifacts,
// pinned by digest. Nothing in it is substrate-specific — a target reads it and
// decides what a Helm chart, a compose file or a manifest directory should say
// (INV-007).
//
// The binding's configuration is deliberately absent. The target was built from
// that same binding and already holds it; copying it in would put a resolved
// credential into bytes that are checksummed, persisted and handed across a
// plugin's subprocess boundary (INV-012). A configuration change still shows up
// in a plan, because the target compares against its own live state and reports
// that something differs.
package desired

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/engine/planner"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Kind names the shape of the document. It is versioned because a target
// written against it is a separate binary on its own release cycle: an added
// field is compatible, and anything that is not gets a new kind rather than a
// target silently misreading the old one.
const Kind = "rollops.release/v1"

var (
	// ErrNothingToDeploy reports a release naming no artifacts. The domain
	// refuses to build one, so reaching this means a release was assembled by
	// hand — and rendering it would ask a target to converge on nothing, which
	// most substrates read as "delete everything".
	ErrNothingToDeploy = errors.New("desired: release names no artifacts")

	// ErrForeignArtifact reports a release pointing at another project's
	// artifact. Artifacts are looked up by id alone, so the project boundary is
	// checked here rather than assumed: without it a release could deploy an
	// image its project was never allowed to see.
	ErrForeignArtifact = errors.New("desired: artifact belongs to another project")
)

// Document is what a target is asked to converge on.
//
// It is JSON rather than a Go type crossing the boundary because a target may
// be a plugin in another language, and the field names are the contract.
type Document struct {
	Kind      string     `json:"kind"`
	Release   Release    `json:"release"`
	Artifacts []Artifact `json:"artifacts"`
}

// Release identifies what is being deployed and where it came from.
type Release struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Source  Source `json:"source"`
}

// Source is the commit the release was built from, so that a target can label
// what it deploys with something an operator can trace back.
type Source struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Ref        string `json:"ref,omitempty"`
}

// Artifact is one deployable thing and the role it plays in the release.
//
// Locator is pinned by digest — the artifact domain refuses an OCI reference
// that is not — so a target resolving it a week later gets the same bytes the
// plan was approved for.
type Artifact struct {
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	Digest    string `json:"digest"`
	Locator   string `json:"locator"`
	MediaType string `json:"mediaType,omitempty"`
}

// Renderer builds documents from releases.
type Renderer struct {
	artifacts port.ArtifactRepository
}

// New returns a renderer. The repository is required: a release stores artifact
// ids, and without somewhere to resolve them the document would name what to
// deploy without saying where to get it.
func New(artifacts port.ArtifactRepository) (*Renderer, error) {
	if artifacts == nil {
		return nil, errors.New("desired: no artifact repository; nothing could be resolved")
	}
	return &Renderer{artifacts: artifacts}, nil
}

// DesiredState renders the release the request names.
//
// The same release renders to the same bytes for every binding it is deployed
// to. That is the point of the checksum: two environments running one release
// are running the same thing, and what differs between them is the target's own
// configuration rather than what it was told to deploy.
func (r *Renderer) DesiredState(
	ctx context.Context, req planner.DesiredRequest,
) (targetv2.DesiredState, error) {
	doc, err := r.document(ctx, req.Release)
	if err != nil {
		return targetv2.DesiredState{}, err
	}
	spec, err := json.Marshal(doc)
	if err != nil {
		return targetv2.DesiredState{}, fmt.Errorf("desired: release %s: %w", req.Release.Version, err)
	}
	return targetv2.DesiredState{
		Kind:     Kind,
		Spec:     spec,
		Checksum: digest.Of(spec).String(),
		// Labels are for whatever the substrate stamps on what it creates, so
		// that somebody reading a live cluster can get back to the release.
		// They carry names, not ids alone: an id is precise and unreadable.
		Labels: map[string]string{
			"rollops.io/release":     string(req.Release.ID),
			"rollops.io/version":     req.Release.Version,
			"rollops.io/environment": req.Environment.Name,
			"rollops.io/target":      req.Binding.Name,
		},
		// Rendered is for a desired state that pointed at something external and
		// had it resolved. This one points at nothing: it is already the whole
		// of what RollOps knows, and the target supplies the rest.
		Rendered: nil,
	}, nil
}

// document resolves every artifact the release names.
func (r *Renderer) document(ctx context.Context, rel release.Release) (Document, error) {
	if len(rel.Artifacts) == 0 {
		return Document{}, fmt.Errorf("%w: %s", ErrNothingToDeploy, rel.Version)
	}
	doc := Document{
		Kind: Kind,
		Release: Release{
			ID:      string(rel.ID),
			Version: rel.Version,
			Source: Source{
				Provider:   rel.Source.Provider,
				Repository: rel.Source.Repository,
				Revision:   rel.Source.Revision,
				Ref:        rel.Source.Ref,
			},
		},
		Artifacts: make([]Artifact, 0, len(rel.Artifacts)),
	}
	for _, ra := range rel.Artifacts {
		a, err := r.artifacts.Get(ctx, ra.ArtifactID)
		if err != nil {
			return Document{}, fmt.Errorf("desired: release %s: role %q: %w", rel.Version, ra.Role, err)
		}
		if a.ProjectID != rel.ProjectID {
			return Document{}, fmt.Errorf("%w: release %s: role %q", ErrForeignArtifact, rel.Version, ra.Role)
		}
		doc.Artifacts = append(doc.Artifacts, Artifact{
			Role:      ra.Role,
			Kind:      string(a.Kind),
			Digest:    a.Digest.String(),
			Locator:   a.Locator,
			MediaType: a.MediaType,
		})
	}
	// Sorted by role so that two renders of one release are byte-identical
	// whatever order the release happens to list its artifacts in. The checksum
	// is what tells a target it has already converged, and a checksum that
	// moved for no reason would make every plan report a change.
	slices.SortFunc(doc.Artifacts, func(a, b Artifact) int { return cmp.Compare(a.Role, b.Role) })
	return doc, nil
}
