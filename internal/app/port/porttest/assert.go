package porttest

import (
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
)

// An aggregate with no labels reads back as an aggregate with no labels,
// whether the implementation returns a nil map or an empty one. Requiring one
// spelling would make every implementation normalise for the sake of the
// comparison rather than for any caller.
func sameMap[V comparable](a, b map[string]V) bool {
	if len(a) != len(b) {
		return false
	}
	for k, want := range a {
		if got, ok := b[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// sameTime compares instants rather than representations: a time that has been
// through storage has lost its monotonic reading and may carry a different
// location for the same moment.
func sameTime(a, b time.Time) bool { return a.Equal(b) }

func assertProject(t *testing.T, got, want project.Project) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Description != want.Description {
		t.Errorf("Description = %q, want %q", got.Description, want.Description)
	}
	if !sameMap(got.Labels, want.Labels) {
		t.Errorf("Labels = %v, want %v", got.Labels, want.Labels)
	}
	if !sameTime(got.CreatedAt, want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	if !sameTime(got.UpdatedAt, want.UpdatedAt) {
		t.Errorf("UpdatedAt = %s, want %s", got.UpdatedAt, want.UpdatedAt)
	}
}

func assertEnvironment(t *testing.T, got, want environment.Environment) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if got.Kind != want.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, want.Kind)
	}
	if got.Lifecycle != want.Lifecycle {
		t.Errorf("Lifecycle = %+v, want %+v", got.Lifecycle, want.Lifecycle)
	}
	if !sameMap(got.Labels, want.Labels) {
		t.Errorf("Labels = %v, want %v", got.Labels, want.Labels)
	}
	if !sameMap(got.Variables, want.Variables) {
		t.Errorf("Variables = %v, want %v", got.Variables, want.Variables)
	}
	assertTargets(t, got.Targets, want.Targets)
	assertPolicies(t, got.Policies, want.Policies)
}

func assertTargets(t *testing.T, got, want []environment.TargetBinding) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d targets, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Driver != want[i].Driver {
			t.Errorf("target %d = %s, want %s", i, got[i], want[i])
		}
		if !sameMap(got[i].Config, want[i].Config) {
			t.Errorf("target %d config = %v, want %v", i, got[i].Config, want[i].Config)
		}
		if !sameMap(got[i].Labels, want[i].Labels) {
			t.Errorf("target %d labels = %v, want %v", i, got[i].Labels, want[i].Labels)
		}
	}
}

func assertPolicies(t *testing.T, got, want []environment.PolicyBinding) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d policies, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("policy %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertArtifact(t *testing.T, got, want artifact.Artifact) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Kind != want.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, want.Kind)
	}
	if got.Digest != want.Digest {
		t.Errorf("Digest = %s, want %s", got.Digest, want.Digest)
	}
	if got.Locator != want.Locator {
		t.Errorf("Locator = %q, want %q", got.Locator, want.Locator)
	}
	if got.Size != want.Size {
		t.Errorf("Size = %d, want %d", got.Size, want.Size)
	}
	if got.MediaType != want.MediaType {
		t.Errorf("MediaType = %q, want %q", got.MediaType, want.MediaType)
	}
	if !sameMap(got.Metadata, want.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
	if got.Provenance != want.Provenance {
		t.Errorf("Provenance = %+v, want %+v", got.Provenance, want.Provenance)
	}
	if len(got.SBOMs) != len(want.SBOMs) {
		t.Errorf("got %d sboms, want %d", len(got.SBOMs), len(want.SBOMs))
	}
	if len(got.Signatures) != len(want.Signatures) {
		t.Errorf("got %d signatures, want %d", len(got.Signatures), len(want.Signatures))
	}
	if !sameTime(got.CreatedAt, want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	// An artifact that came back must still be a valid one: a store that drops
	// a field can produce a record the domain would have refused to write.
	if err := got.Validate(); err != nil {
		t.Errorf("the stored artifact is no longer valid: %v", err)
	}
}

func assertRelease(t *testing.T, got, want release.Release) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.Version != want.Version {
		t.Errorf("Version = %q, want %q", got.Version, want.Version)
	}
	if got.Source != want.Source {
		t.Errorf("Source = %+v, want %+v", got.Source, want.Source)
	}
	if got.Provenance != want.Provenance {
		t.Errorf("Provenance = %+v, want %+v", got.Provenance, want.Provenance)
	}
	if got.CreatedBy.ID != want.CreatedBy.ID || got.CreatedBy.Type != want.CreatedBy.Type {
		t.Errorf("CreatedBy = %+v, want %+v", got.CreatedBy, want.CreatedBy)
	}
	if !sameTime(got.CreatedAt, want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	if !sameMap(got.Labels, want.Labels) {
		t.Errorf("Labels = %v, want %v", got.Labels, want.Labels)
	}
	if !sameMap(got.Annotations, want.Annotations) {
		t.Errorf("Annotations = %v, want %v", got.Annotations, want.Annotations)
	}

	// Roles are a set, not a list: the release's own lookup is what a
	// deployment uses, so that is what has to agree.
	if len(got.Artifacts) != len(want.Artifacts) {
		t.Fatalf("got %d artifacts, want %d", len(got.Artifacts), len(want.Artifacts))
	}
	for _, a := range want.Artifacts {
		id, ok := got.ArtifactFor(a.Role)
		if !ok {
			t.Errorf("role %q is missing", a.Role)
			continue
		}
		if id != a.ArtifactID {
			t.Errorf("role %q = %s, want %s", a.Role, id, a.ArtifactID)
		}
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the stored release is no longer valid: %v", err)
	}
}
