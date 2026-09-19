// Package artifact models a built thing that a Release can deploy.
//
// An artifact is immutable (INV-002). Nothing here enforces that by hiding
// fields — a copy held in memory harms nobody — so the guarantee is the
// repository's: an artifact row is written once and never updated. Validate is
// what keeps an invalid one from being written in the first place.
package artifact

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
)

// Kind names what sort of thing an artifact is. It determines how a target
// consumes it, not which target consumes it.
type Kind string

const (
	KindOCIImage       Kind = "oci-image"
	KindOCIArtifact    Kind = "oci-artifact"
	KindBinary         Kind = "binary"
	KindArchive        Kind = "archive"
	KindHelmChart      Kind = "helm-chart"
	KindManifestBundle Kind = "manifest-bundle"
	KindWASM           Kind = "wasm"
	KindFile           Kind = "file"
)

var kinds = map[Kind]struct{}{
	KindOCIImage:       {},
	KindOCIArtifact:    {},
	KindBinary:         {},
	KindArchive:        {},
	KindHelmChart:      {},
	KindManifestBundle: {},
	KindWASM:           {},
	KindFile:           {},
}

// ErrUnpinnedLocator reports a registry reference that would be resolved again
// at pull time. Such a reference can hand a different image to production than
// the one that was verified.
var ErrUnpinnedLocator = errors.New("artifact: locator is not pinned to the artifact digest")

// Artifact is a built thing, identified by its content.
type Artifact struct {
	ID         identity.ArtifactID
	ProjectID  identity.ProjectID
	Kind       Kind
	Digest     digest.Digest
	Locator    string
	Size       int64
	MediaType  string
	Metadata   map[string]string
	Provenance provenance.DocumentRef
	SBOMs      []provenance.DocumentRef
	Signatures []provenance.DocumentRef
	CreatedAt  time.Time
}

// New stamps an id and a creation time onto a, then validates it.
func New(g identity.Generator, c identity.Clock, a Artifact) (Artifact, error) {
	id, err := identity.NewArtifactID(g)
	if err != nil {
		return Artifact{}, err
	}
	a.ID = id
	a.CreatedAt = c.Now()
	if err := a.Validate(); err != nil {
		return Artifact{}, err
	}
	return a, nil
}

// Validate reports whether the artifact can be recorded and later deployed.
func (a Artifact) Validate() error {
	if _, err := identity.ParseArtifactID(string(a.ID)); err != nil {
		return fmt.Errorf("artifact: id: %w", err)
	}
	if _, err := identity.ParseProjectID(string(a.ProjectID)); err != nil {
		return fmt.Errorf("artifact: project id: %w", err)
	}
	if _, ok := kinds[a.Kind]; !ok {
		return fmt.Errorf("artifact: unknown kind %q", a.Kind)
	}
	// A non-zero Digest is valid by construction — its fields are unexported
	// and every route in goes through digest.Parse or digest.Of — so absence
	// is the only failure that can be expressed here.
	if a.Digest.IsZero() {
		return errors.New("artifact: no digest; an artifact is identified by its content")
	}
	if strings.TrimSpace(a.Locator) == "" {
		return errors.New("artifact: no locator; the content could never be fetched")
	}
	if a.Size < 0 {
		return fmt.Errorf("artifact: negative size %d", a.Size)
	}
	if a.CreatedAt.IsZero() {
		return errors.New("artifact: no creation time")
	}
	if err := a.validateLocator(); err != nil {
		return err
	}
	if a.Provenance != (provenance.DocumentRef{}) {
		if err := a.Provenance.ValidateAs(provenance.DocumentProvenance); err != nil {
			return fmt.Errorf("artifact: provenance: %w", err)
		}
	}
	for i, s := range a.SBOMs {
		if err := s.ValidateAs(provenance.DocumentSBOM); err != nil {
			return fmt.Errorf("artifact: sbom %d: %w", i, err)
		}
	}
	for i, s := range a.Signatures {
		if err := s.ValidateAs(provenance.DocumentSignature); err != nil {
			return fmt.Errorf("artifact: signature %d: %w", i, err)
		}
	}
	return nil
}

// validateLocator enforces digest pinning where the locator is resolved by a
// registry rather than fetched and then checked.
func (a Artifact) validateLocator() error {
	if !a.locatorIsResolvedRemotely() {
		return nil
	}
	pin, ok := lastCut(a.Locator, "@")
	if !ok || pin != a.Digest.String() {
		return fmt.Errorf("%w: %q must end in @%s", ErrUnpinnedLocator, a.Locator, a.Digest)
	}
	return nil
}

func (a Artifact) locatorIsResolvedRemotely() bool {
	switch a.Kind {
	case KindOCIImage, KindOCIArtifact:
		return true
	}
	return strings.HasPrefix(a.Locator, "oci://")
}

// lastCut splits on the final occurrence of sep, so a tag containing no "@"
// and a digest pin that follows one are both handled.
func lastCut(s, sep string) (string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return "", false
	}
	return s[i+len(sep):], true
}

// SameContent reports whether two artifacts are the same thing. Identity is the
// digest, not the id: the same bytes recorded twice are one artifact, so a
// second upload under a new name cannot become a second artifact.
func (a Artifact) SameContent(other Artifact) bool {
	return a.ProjectID == other.ProjectID &&
		a.Kind == other.Kind &&
		a.Digest == other.Digest
}
