// Package provenance records where a thing came from: the immutable source
// revision it was built from, who built it, and the external documents — SBOMs,
// signatures, attestations — that vouch for it.
//
// The core stores references and normalized metadata (spec §5.2). It does not
// define a proprietary attestation format, and it does not parse SPDX,
// CycloneDX, SLSA or Sigstore payloads here; it records what they are, where
// they live, and the digest that proves a fetched copy is the one meant.
package provenance

import (
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/domain/digest"
)

// ProviderGit is the only source provider whose revision format the domain
// knows. Others are accepted as opaque strings.
const ProviderGit = "git"

// ErrMutableRevision reports a source revision that does not resolve to fixed
// content — a branch or tag name where a commit was required.
var ErrMutableRevision = errors.New("provenance: revision is not immutable")

// ErrUnverifiableRef reports a document reference with no digest. Such a
// reference points at whatever its locator serves today, which defeats the
// purpose of referencing an attestation at all.
var ErrUnverifiableRef = errors.New("provenance: reference has no digest")

// SourceRevision identifies the exact source a Release was built from.
type SourceRevision struct {
	Provider   string
	Repository string
	Revision   string
	Ref        string
	TreeDigest string
	URL        string
}

// Validate reports whether the revision names fixed content.
func (r SourceRevision) Validate() error {
	if strings.TrimSpace(r.Provider) == "" {
		return errors.New("provenance: source revision has no provider")
	}
	if strings.TrimSpace(r.Repository) == "" {
		return errors.New("provenance: source revision has no repository")
	}
	if strings.TrimSpace(r.Revision) == "" {
		return fmt.Errorf("%w: revision is empty", ErrMutableRevision)
	}
	if r.Provider != ProviderGit {
		return nil
	}
	// Git is the provider we ship, so its revision format is checked rather
	// than trusted. An abbreviated object id is excluded too: it is unique
	// today and ambiguous once the repository grows.
	if !isObjectID(r.Revision) {
		return fmt.Errorf("%w: %q is not a full git object id", ErrMutableRevision, r.Revision)
	}
	return nil
}

// isObjectID reports whether s is a full lower-case git object id, under either
// SHA-1 or the SHA-256 object format.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// DocumentKind names what an external document asserts.
type DocumentKind string

const (
	DocumentSBOM        DocumentKind = "sbom"
	DocumentSignature   DocumentKind = "signature"
	DocumentAttestation DocumentKind = "attestation"
	DocumentProvenance  DocumentKind = "provenance"
)

// DocumentRef points at an external document by digest. One type serves every
// kind because the shape is identical; Kind keeps a signature from being filed
// where an SBOM belongs.
type DocumentRef struct {
	Kind    DocumentKind
	Format  string
	Locator string
	Digest  digest.Digest
}

// Validate reports whether the reference can be resolved and verified.
func (d DocumentRef) Validate() error {
	switch d.Kind {
	case DocumentSBOM, DocumentSignature, DocumentAttestation, DocumentProvenance:
	default:
		return fmt.Errorf("provenance: unknown document kind %q", d.Kind)
	}
	if strings.TrimSpace(d.Format) == "" {
		return fmt.Errorf("provenance: %s reference declares no format", d.Kind)
	}
	if d.Digest.IsZero() {
		return fmt.Errorf("%w: %s reference %q", ErrUnverifiableRef, d.Kind, d.Locator)
	}
	return d.Digest.Validate()
}

// ValidateAs reports whether the reference is valid and of the expected kind.
func (d DocumentRef) ValidateAs(want DocumentKind) error {
	if d.Kind != want {
		return fmt.Errorf("provenance: reference is a %s, want a %s", d.Kind, want)
	}
	return d.Validate()
}

// Material is an input consumed by a build.
type Material struct {
	URI    string
	Digest digest.Digest
}

// Validate reports whether the material identifies an input.
func (m Material) Validate() error {
	if strings.TrimSpace(m.URI) == "" {
		return errors.New("provenance: material has no uri")
	}
	if m.Digest.IsZero() {
		return nil
	}
	return m.Digest.Validate()
}

// Provenance is the normalized record of how something was built.
type Provenance struct {
	Source       SourceRevision
	Builder      string
	BuildType    string
	InvocationID string
	Materials    []Material
	Attestations []DocumentRef
}

// Validate reports whether the provenance is complete enough to be evidence.
func (p Provenance) Validate() error {
	if err := p.Source.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Builder) == "" {
		return errors.New("provenance: no builder")
	}
	if strings.TrimSpace(p.BuildType) == "" {
		return errors.New("provenance: no build type")
	}
	for i, m := range p.Materials {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("provenance: material %d: %w", i, err)
		}
	}
	for i, a := range p.Attestations {
		if err := a.ValidateAs(DocumentAttestation); err != nil {
			return fmt.Errorf("provenance: attestation %d: %w", i, err)
		}
	}
	return nil
}
