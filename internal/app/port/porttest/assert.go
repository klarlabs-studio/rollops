package porttest

import (
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/provenance"
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

// sameTimePtr distinguishes absent from present before comparing instants. An
// absent time means the thing has not happened yet, and a comparison that
// treated it as the zero instant would report a deployment that never started
// as agreeing with one that started in 1970.
func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

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
	assertDocuments(t, "sboms", got.SBOMs, want.SBOMs)
	assertDocuments(t, "signatures", got.Signatures, want.Signatures)
	if !sameTime(got.CreatedAt, want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	// An artifact that came back must still be a valid one: a store that drops
	// a field can produce a record the domain would have refused to write.
	if err := got.Validate(); err != nil {
		t.Errorf("the stored artifact is no longer valid: %v", err)
	}
}

// assertDocuments compares references one by one rather than by count. The
// digest is the field a structural encoder drops, and a list of the right
// length whose digests are all zero is the exact shape that failure takes.
func assertDocuments(t *testing.T, field string, got, want []provenance.DocumentRef) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d %s, want %d", len(got), field, len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %+v, want %+v", field, i, got[i], want[i])
		}
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

func assertPlan(t *testing.T, got, want plan.DeploymentPlan) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.EnvironmentID != want.EnvironmentID {
		t.Errorf("EnvironmentID = %q, want %q", got.EnvironmentID, want.EnvironmentID)
	}
	if got.ReleaseID != want.ReleaseID {
		t.Errorf("ReleaseID = %q, want %q", got.ReleaseID, want.ReleaseID)
	}
	// The base revision is what apply compares against the live world. A store
	// that dropped it would turn every stale plan into an applicable one.
	if got.BaseRevision != want.BaseRevision {
		t.Errorf("BaseRevision = %d, want %d", got.BaseRevision, want.BaseRevision)
	}
	if got.Strategy != want.Strategy {
		t.Errorf("Strategy = %q, want %q", got.Strategy, want.Strategy)
	}
	assertOperations(t, "operations", got.Operations, want.Operations)
	assertDecision(t, got.Policy, want.Policy)

	if got.Rollback.FromRelease != want.Rollback.FromRelease ||
		got.Rollback.ToRelease != want.Rollback.ToRelease ||
		got.Rollback.Automatic != want.Rollback.Automatic {
		t.Errorf("Rollback = %+v, want %+v", got.Rollback, want.Rollback)
	}
	assertOperations(t, "rollback operations", got.Rollback.Operations, want.Rollback.Operations)

	if got.CreatedBy.ID != want.CreatedBy.ID || got.CreatedBy.Type != want.CreatedBy.Type {
		t.Errorf("CreatedBy = %+v, want %+v", got.CreatedBy, want.CreatedBy)
	}
	if !sameTime(got.CreatedAt, want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	// Expiry is half of what makes a plan safe to apply later. A store that
	// lost it would leave the plan applicable forever.
	if !sameTime(got.ExpiresAt, want.ExpiresAt) {
		t.Errorf("ExpiresAt = %s, want %s", got.ExpiresAt, want.ExpiresAt)
	}
	if got.Hash != want.Hash {
		t.Errorf("Hash = %s, want %s", got.Hash, want.Hash)
	}
	// Recomputed from what came back, not read from a column. The hash is the
	// whole argument that the plan applied is the plan approved, and a stored
	// one that disagreed with its contents is the failure it exists to catch.
	if err := got.VerifyHash(); err != nil {
		t.Errorf("the stored plan does not verify: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the stored plan is no longer valid: %v", err)
	}
}

// assertOperations compares step by step rather than by count. Dependencies and
// diffs are what a structural encoder silently flattens, and a list of the
// right length whose operations all depend on nothing is the shape that failure
// takes.
func assertOperations(t *testing.T, field string, got, want []plan.PlannedOperation) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d %s, want %d", len(got), field, len(want))
		return
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Target != want[i].Target ||
			got[i].Kind != want[i].Kind || got[i].Summary != want[i].Summary ||
			got[i].Reversible != want[i].Reversible {
			t.Errorf("%s[%d] = %+v, want %+v", field, i, got[i], want[i])
		}
		if len(got[i].Dependencies) != len(want[i].Dependencies) {
			t.Errorf("%s[%d] dependencies = %v, want %v", field, i, got[i].Dependencies, want[i].Dependencies)
			continue
		}
		for j := range want[i].Dependencies {
			if got[i].Dependencies[j] != want[i].Dependencies[j] {
				t.Errorf("%s[%d] dependency %d = %q, want %q",
					field, i, j, got[i].Dependencies[j], want[i].Dependencies[j])
			}
		}
		assertChanges(t, fmt.Sprintf("%s[%d]", field, i), got[i].Diff.Changes, want[i].Diff.Changes)
	}
}

// assertChanges expects a sensitive value to have been dropped on the way in.
// The comparison is written against the redacted form deliberately: storing the
// value is the defect (INV-011), so a store that round-tripped it faithfully
// would be the one failing.
func assertChanges(t *testing.T, field string, got, want []plan.Change) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d changes in %s, want %d", len(got), field, len(want))
		return
	}
	for i := range want {
		expected := want[i]
		if expected.Sensitive {
			expected.From, expected.To = "", ""
		}
		if got[i] != expected {
			t.Errorf("%s change %d = %+v, want %+v", field, i, got[i], expected)
		}
	}
}

func assertDecision(t *testing.T, got, want policy.Decision) {
	t.Helper()
	if got.Allowed != want.Allowed {
		t.Errorf("Policy.Allowed = %v, want %v", got.Allowed, want.Allowed)
	}
	if got.Risk.Level != want.Risk.Level || got.Risk.Score != want.Risk.Score {
		t.Errorf("Policy.Risk = %+v, want %+v", got.Risk, want.Risk)
	}
	// Requirements are what stands between a plan and an apply. Losing one
	// turns a gated plan into an ungated one that still reads as reviewed.
	if len(got.Requirements) != len(want.Requirements) {
		t.Errorf("got %d requirements, want %d", len(got.Requirements), len(want.Requirements))
	} else {
		for i := range want.Requirements {
			if got.Requirements[i] != want.Requirements[i] {
				t.Errorf("requirement %d = %+v, want %+v", i, got.Requirements[i], want.Requirements[i])
			}
		}
	}
	if len(got.Reasons) != len(want.Reasons) {
		t.Errorf("got %d reasons, want %d", len(got.Reasons), len(want.Reasons))
	} else {
		for i := range want.Reasons {
			if got.Reasons[i] != want.Reasons[i] {
				t.Errorf("reason %d = %+v, want %+v", i, got.Reasons[i], want.Reasons[i])
			}
		}
	}
	if len(got.Risk.Factors) != len(want.Risk.Factors) {
		t.Errorf("got %d risk factors, want %d", len(got.Risk.Factors), len(want.Risk.Factors))
		return
	}
	for i := range want.Risk.Factors {
		if got.Risk.Factors[i] != want.Risk.Factors[i] {
			t.Errorf("risk factor %d = %+v, want %+v", i, got.Risk.Factors[i], want.Risk.Factors[i])
		}
	}
}

func assertDeployment(t *testing.T, got, want deployment.Deployment) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.EnvironmentID != want.EnvironmentID {
		t.Errorf("EnvironmentID = %q, want %q", got.EnvironmentID, want.EnvironmentID)
	}
	if got.ReleaseID != want.ReleaseID {
		t.Errorf("ReleaseID = %q, want %q", got.ReleaseID, want.ReleaseID)
	}
	// A deployment without its plan was never reviewed: apply would have
	// nothing to verify the hash of and nothing to compare the world against.
	if got.PlanID != want.PlanID {
		t.Errorf("PlanID = %q, want %q", got.PlanID, want.PlanID)
	}
	if got.Strategy != want.Strategy {
		t.Errorf("Strategy = %q, want %q", got.Strategy, want.Strategy)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	// Who and why are different questions, and a store that kept one without
	// the other leaves a timeline that cannot be explained.
	if got.Trigger != want.Trigger {
		t.Errorf("Trigger = %+v, want %+v", got.Trigger, want.Trigger)
	}
	if got.Actor.ID != want.Actor.ID || got.Actor.Type != want.Actor.Type {
		t.Errorf("Actor = %+v, want %+v", got.Actor, want.Actor)
	}
	if !sameTime(got.CreatedAt, want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	if !sameTimePtr(got.StartedAt, want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	if !sameTimePtr(got.FinishedAt, want.FinishedAt) {
		t.Errorf("FinishedAt = %v, want %v", got.FinishedAt, want.FinishedAt)
	}
	if (got.Previous == nil) != (want.Previous == nil) ||
		(got.Previous != nil && *got.Previous != *want.Previous) {
		t.Errorf("Previous = %v, want %v", got.Previous, want.Previous)
	}
	if got.Revision != want.Revision {
		t.Errorf("Revision = %d, want %d", got.Revision, want.Revision)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the stored deployment is no longer valid: %v", err)
	}
}
