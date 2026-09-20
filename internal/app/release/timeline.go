package release

import (
	"context"
	"encoding/json"
	"fmt"

	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
)

// Payloads carry what identifies the thing and nothing that can be added to it
// later. Labels and annotations are outside a release's fingerprint (INV-003)
// and are the one part of it that can change, so a log that copied them would
// record a claim about the release that stops being true — in a log nothing may
// rewrite (INV-013).

type artifactRegistered struct {
	ProjectID identity.ProjectID `json:"project_id"`
	Kind      artifact.Kind      `json:"kind"`
	Digest    string             `json:"digest"`
	Size      int64              `json:"size,omitempty"`
	MediaType string             `json:"media_type,omitempty"`

	// Locator is absent. It names a registry, and a private one's host is the
	// sort of detail an event log is read by people who should not need it.
	// The artifact record holds it, and the digest here is what identifies the
	// bytes anyway.

	// Attestations counts what was supplied rather than naming it: whether an
	// artifact arrived with provenance is the auditable fact, and the documents
	// themselves live on the artifact.
	Attestations int `json:"attestations,omitempty"`
}

type releaseCreated struct {
	ProjectID   identity.ProjectID `json:"project_id"`
	Version     string             `json:"version"`
	Fingerprint string             `json:"fingerprint"`
	Source      sourceRef          `json:"source"`
	Artifacts   []artifactRole     `json:"artifacts"`
}

// sourceRef is the revision the release was built from, flattened. It is the
// answer to "what code is this", which is the first thing anybody reading a
// release event wants and the last thing they should have to join for.
type sourceRef struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Ref        string `json:"ref,omitempty"`
}

type artifactRole struct {
	Role       string              `json:"role"`
	ArtifactID identity.ArtifactID `json:"artifact_id"`
}

// recordArtifact files the registration against the artifact. It starts its own
// chain: a build is not caused by anything this system did, and hanging it off
// whatever happened to be nearby would invent a cause.
func (s *Service) recordArtifact(
	ctx context.Context, by identity.Principal, a artifact.Artifact,
) error {
	payload, err := encode(artifactRegistered{
		ProjectID:    a.ProjectID,
		Kind:         a.Kind,
		Digest:       a.Digest.String(),
		Size:         a.Size,
		MediaType:    a.MediaType,
		Attestations: attestations(a),
	})
	if err != nil {
		return err
	}
	return s.appendTo(ctx, by, event.ArtifactRegistered, event.AggregateArtifact, string(a.ID), payload)
}

// recordRelease files the release against the release. Like a registration it
// is a root: the artifacts it names were registered on their own occasions, and
// a release is the first moment they are one thing.
func (s *Service) recordRelease(
	ctx context.Context, by identity.Principal, r release.Release,
) error {
	roles := make([]artifactRole, len(r.Artifacts))
	for i, a := range r.Artifacts {
		roles[i] = artifactRole{Role: a.Role, ArtifactID: a.ArtifactID}
	}
	payload, err := encode(releaseCreated{
		ProjectID:   r.ProjectID,
		Version:     r.Version,
		Fingerprint: r.Fingerprint().String(),
		Source: sourceRef{
			Provider:   r.Source.Provider,
			Repository: r.Source.Repository,
			Revision:   r.Source.Revision,
			Ref:        r.Source.Ref,
		},
		Artifacts: roles,
	})
	if err != nil {
		return err
	}
	return s.appendTo(ctx, by, event.ReleaseCreated, event.AggregateRelease, string(r.ID), payload)
}

// appendTo stamps and writes one event. It exists so that no call site can
// forget to run an envelope through event.New, which is where attribution and
// redaction are applied (INV-005, INV-012).
func (s *Service) appendTo(
	ctx context.Context,
	by identity.Principal,
	typ event.Type,
	aggregate event.AggregateType,
	id string,
	payload json.RawMessage,
) error {
	stamped, err := event.New(s.cfg.IDs, s.cfg.Clock, by, event.Event{
		Type:          typ,
		AggregateType: aggregate,
		AggregateID:   id,
		Payload:       payload,
	})
	if err != nil {
		return fmt.Errorf("release: recording %s: %w", typ, err)
	}
	if _, err := s.cfg.Events.Append(ctx, stamped); err != nil {
		return fmt.Errorf("release: recording %s: %w", typ, err)
	}
	return nil
}

func attestations(a artifact.Artifact) int {
	n := len(a.SBOMs) + len(a.Signatures)
	if a.Provenance != (provenance.DocumentRef{}) {
		n++
	}
	return n
}

func encode(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("release: encoding event payload: %w", err)
	}
	return raw, nil
}
