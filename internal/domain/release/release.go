// Package release models the primary business object: a fixed set of artifacts,
// built from a known source revision, that moves through environments.
//
// A release is immutable after creation except for labels and annotations,
// which are additive and deliberately excluded from its fingerprint (INV-003).
package release

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/canonical"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
)

var (
	// ErrDuplicateRole reports two artifacts claiming one role, which would
	// make the artifact a deployment selects depend on iteration order.
	ErrDuplicateRole = errors.New("release: duplicate artifact role")

	// ErrDuplicateArtifact reports the same artifact listed twice.
	ErrDuplicateArtifact = errors.New("release: artifact listed more than once")
)

// Artifact binds one artifact to the role it plays in the release — "app",
// "migration", "frontend", "chart". The role is what a deployment selects on,
// so it is part of the release's identity.
type Artifact struct {
	ArtifactID identity.ArtifactID
	Role       string
}

// Release is a fixed set of artifacts built from a known source revision.
type Release struct {
	ID          identity.ReleaseID
	ProjectID   identity.ProjectID
	Version     string
	Artifacts   []Artifact
	Source      provenance.SourceRevision
	Provenance  provenance.DocumentRef
	CreatedBy   identity.Principal
	CreatedAt   time.Time
	Labels      map[string]string
	Annotations map[string]string
}

// New stamps identity, attribution and time onto r, then validates it. The
// author's claims are redacted before they are stored: a release is persisted
// and rendered on every surface, so a credential that reached it would be
// impossible to recall (INV-012).
func New(g identity.Generator, c identity.Clock, by identity.Principal, r Release) (Release, error) {
	id, err := identity.NewReleaseID(g)
	if err != nil {
		return Release{}, err
	}
	r.ID = id
	r.CreatedBy = by.Redacted()
	r.CreatedAt = c.Now()
	if err := r.Validate(); err != nil {
		return Release{}, err
	}
	return r, nil
}

// Validate reports whether the release is a complete, unambiguous record.
func (r Release) Validate() error {
	if _, err := identity.ParseReleaseID(string(r.ID)); err != nil {
		return fmt.Errorf("release: id: %w", err)
	}
	if _, err := identity.ParseProjectID(string(r.ProjectID)); err != nil {
		return fmt.Errorf("release: project id: %w", err)
	}
	if strings.TrimSpace(r.Version) == "" {
		return errors.New("release: no version")
	}
	if len(r.Artifacts) == 0 {
		return errors.New("release: no artifacts; there would be nothing to deploy")
	}
	roles := make(map[string]struct{}, len(r.Artifacts))
	ids := make(map[identity.ArtifactID]struct{}, len(r.Artifacts))
	for i, a := range r.Artifacts {
		if _, err := identity.ParseArtifactID(string(a.ArtifactID)); err != nil {
			return fmt.Errorf("release: artifact %d: %w", i, err)
		}
		if strings.TrimSpace(a.Role) == "" {
			return fmt.Errorf("release: artifact %d has no role", i)
		}
		if _, dup := roles[a.Role]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateRole, a.Role)
		}
		if _, dup := ids[a.ArtifactID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateArtifact, a.ArtifactID)
		}
		roles[a.Role] = struct{}{}
		ids[a.ArtifactID] = struct{}{}
	}
	if err := r.Source.Validate(); err != nil {
		return fmt.Errorf("release: source: %w", err)
	}
	if err := r.CreatedBy.Validate(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if r.CreatedAt.IsZero() {
		return errors.New("release: no creation time")
	}
	if r.Provenance != (provenance.DocumentRef{}) {
		if err := r.Provenance.ValidateAs(provenance.DocumentProvenance); err != nil {
			return fmt.Errorf("release: provenance: %w", err)
		}
	}
	return nil
}

// ArtifactFor returns the artifact playing role, if the release has one.
func (r Release) ArtifactFor(role string) (identity.ArtifactID, bool) {
	for _, a := range r.Artifacts {
		if a.Role == role {
			return a.ArtifactID, true
		}
	}
	return "", false
}

// Fingerprint derives the release's content identity from its version, source
// revision and artifact set, and from nothing else. Two releases built from the
// same inputs share a fingerprint however they were labelled, when they were
// created, or by whom.
func (r Release) Fingerprint() digest.Digest {
	var w canonical.Writer
	w.String(string(r.ProjectID))
	w.String(r.Version)
	w.String(r.Source.Provider)
	w.String(r.Source.Repository)
	w.String(r.Source.Revision)

	// Listing order is presentation, not identity. The artifacts are written
	// with plain fields rather than through canonical.Each because the count
	// and the per-item bounding that Each adds would change every fingerprint
	// already on disk.
	sorted := slices.Clone(r.Artifacts)
	slices.SortFunc(sorted, func(x, y Artifact) int {
		if c := strings.Compare(x.Role, y.Role); c != 0 {
			return c
		}
		return strings.Compare(string(x.ArtifactID), string(y.ArtifactID))
	})
	for _, a := range sorted {
		w.String(a.Role)
		w.String(string(a.ArtifactID))
	}
	return w.Sum()
}

// WithLabel returns a copy carrying an additional label. Labels are outside the
// fingerprint, so this does not change what the release is.
func (r Release) WithLabel(key, value string) Release {
	r.Labels = withEntry(r.Labels, key, value)
	return r
}

// WithAnnotation returns a copy carrying an additional annotation.
func (r Release) WithAnnotation(key, value string) Release {
	r.Annotations = withEntry(r.Annotations, key, value)
	return r
}

// withEntry copies rather than writes through, so the result does not share a
// map with the receiver — a value copy that shared one would not be immutable.
func withEntry(m map[string]string, key, value string) map[string]string {
	next := make(map[string]string, len(m)+1)
	maps.Copy(next, m)
	next[key] = value
	return next
}
