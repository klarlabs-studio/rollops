package release

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
)

const (
	projectID = identity.ProjectID("prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002")
	appID     = identity.ArtifactID("art_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d010")
	migID     = identity.ArtifactID("art_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d011")
	commit    = "9f2c1b7e4a6d8c0f3e5b2a9d7c4f1e8b6a3d0c27"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func source() provenance.SourceRevision {
	return provenance.SourceRevision{
		Provider:   provenance.ProviderGit,
		Repository: "github.com/klarlabs-studio/rollops",
		Revision:   commit,
		Ref:        "refs/tags/v1.2.3",
	}
}

func author() identity.Principal {
	return identity.Principal{ID: "alice", Type: identity.PrincipalHuman}
}

func valid() Release {
	return Release{
		ID:        "rel_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001",
		ProjectID: projectID,
		Version:   "1.2.3",
		Artifacts: []Artifact{
			{ArtifactID: appID, Role: "app"},
			{ArtifactID: migID, Role: "migration"},
		},
		Source:    source(),
		CreatedBy: author(),
		CreatedAt: at,
	}
}

func TestValidRelease(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestReleaseRejectsAnIncompleteRecord(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Release)
	}{
		{"no id", func(r *Release) { r.ID = "" }},
		{"foreign id", func(r *Release) { r.ID = "dep_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001" }},
		{"no project", func(r *Release) { r.ProjectID = "" }},
		{"no version", func(r *Release) { r.Version = "  " }},
		{"no artifacts", func(r *Release) { r.Artifacts = nil }},
		{"artifact with no role", func(r *Release) { r.Artifacts[0].Role = "" }},
		{"artifact with a foreign id", func(r *Release) { r.Artifacts[0].ArtifactID = "rel_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d010" }},
		{"mutable source", func(r *Release) { r.Source.Revision = "main" }},
		{"unattributed", func(r *Release) { r.CreatedBy = identity.Principal{} }},
		{"no creation time", func(r *Release) { r.CreatedAt = time.Time{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := valid()
			c.mutate(&r)
			if err := r.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// A role is how a deployment selects the artifact it needs. Two artifacts
// claiming one role makes that selection ambiguous, and the winner would be
// whichever the storage layer happened to return first.
func TestRolesAreUnique(t *testing.T) {
	r := valid()
	r.Artifacts[1].Role = "app"
	if err := r.Validate(); !errors.Is(err, ErrDuplicateRole) {
		t.Errorf("got %v, want ErrDuplicateRole", err)
	}
}

func TestAnArtifactAppearsOnce(t *testing.T) {
	r := valid()
	r.Artifacts[1].ArtifactID = appID
	if err := r.Validate(); !errors.Is(err, ErrDuplicateArtifact) {
		t.Errorf("got %v, want ErrDuplicateArtifact", err)
	}
}

func TestArtifactLooksUpByRole(t *testing.T) {
	r := valid()
	got, ok := r.ArtifactFor("migration")
	if !ok {
		t.Fatal("role \"migration\" was not found")
	}
	if got != migID {
		t.Errorf("got %q, want %q", got, migID)
	}
	if _, ok := r.ArtifactFor("frontend"); ok {
		t.Error("an absent role was found")
	}
}

// INV-003 / spec 4.6: a release is reproducibly identifiable from its artifact
// set and source revision. Nothing else may enter the fingerprint, or the same
// release built twice would not compare equal.
func TestFingerprintIsDeterministic(t *testing.T) {
	a, b := valid(), valid()
	b.ID = "rel_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d099"
	b.CreatedAt = at.Add(72 * time.Hour)
	b.CreatedBy = identity.Principal{ID: "bob", Type: identity.PrincipalAgent}
	b.Labels = map[string]string{"promoted": "true"}
	b.Annotations = map[string]string{"note": "hotfix"}
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("fingerprint changed although artifacts and source did not")
	}
}

func TestFingerprintIgnoresArtifactOrder(t *testing.T) {
	a := valid()
	b := valid()
	b.Artifacts[0], b.Artifacts[1] = b.Artifacts[1], b.Artifacts[0]
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("fingerprint depended on the order artifacts were listed")
	}
}

// Sorting by role alone leaves ties undecided, and a fingerprint that depends
// on an undecided order is not a fingerprint.
func TestFingerprintOrdersTiedRolesByArtifact(t *testing.T) {
	// Validate rejects a duplicate role, but Fingerprint is computed on
	// whatever it is handed, including a record read back from an older store.
	a := valid()
	a.Artifacts = []Artifact{{ArtifactID: appID, Role: "app"}, {ArtifactID: migID, Role: "app"}}
	b := valid()
	b.Artifacts = []Artifact{{ArtifactID: migID, Role: "app"}, {ArtifactID: appID, Role: "app"}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Error("fingerprint depended on the order of two artifacts sharing a role")
	}
}

func digestOfTestBytes() digest.Digest { return digest.Of([]byte("doc")) }

func TestFingerprintFollowsContent(t *testing.T) {
	base := valid().Fingerprint()
	cases := []struct {
		name   string
		mutate func(*Release)
	}{
		{"a different artifact", func(r *Release) { r.Artifacts[0].ArtifactID = "art_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d0ff" }},
		{"a different role", func(r *Release) { r.Artifacts[0].Role = "frontend" }},
		{"an extra artifact", func(r *Release) {
			r.Artifacts = append(r.Artifacts, Artifact{ArtifactID: "art_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d0ee", Role: "chart"})
		}},
		{"a different commit", func(r *Release) { r.Source.Revision = "1111111111111111111111111111111111111111" }},
		{"a different repository", func(r *Release) { r.Source.Repository = "github.com/someone/else" }},
		{"a different version", func(r *Release) { r.Version = "1.2.4" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := valid()
			c.mutate(&r)
			if r.Fingerprint() == base {
				t.Error("fingerprint did not change although content did")
			}
		})
	}
}

// Role and artifact id are concatenated into the fingerprint, so a separator
// that can appear inside either would let two different releases collide.
func TestFingerprintResistsFieldSmuggling(t *testing.T) {
	a := valid()
	a.Artifacts = []Artifact{{ArtifactID: appID, Role: "app"}}
	b := valid()
	b.Artifacts = []Artifact{{ArtifactID: appID, Role: "app\x00" + string(migID)}}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("a role containing the separator collided with a different release")
	}
}

// INV-003: metadata may be added after creation because it does not change
// what the release is. The release itself is not mutated in place.
func TestLabellingIsAdditiveAndDoesNotChangeIdentity(t *testing.T) {
	r := valid()
	before := r.Fingerprint()
	got := r.WithLabel("promoted-to", "production")
	if got.Fingerprint() != before {
		t.Error("adding a label changed the release fingerprint")
	}
	if got.Labels["promoted-to"] != "production" {
		t.Errorf("label not set: %v", got.Labels)
	}
	if r.Labels != nil {
		t.Error("WithLabel mutated the receiver")
	}
	second := got.WithLabel("ticket", "OPS-41")
	if len(got.Labels) != 1 {
		t.Error("the second WithLabel wrote through to the first result")
	}
	if len(second.Labels) != 2 {
		t.Errorf("got %d labels, want 2", len(second.Labels))
	}
}

func TestAnnotationsAreAdditiveToo(t *testing.T) {
	r := valid()
	got := r.WithAnnotation("changelog", "https://example.com/1.2.3")
	if got.Fingerprint() != r.Fingerprint() {
		t.Error("adding an annotation changed the release fingerprint")
	}
	if got.Annotations["changelog"] != "https://example.com/1.2.3" {
		t.Errorf("annotation not set: %v", got.Annotations)
	}
	if r.Annotations != nil {
		t.Error("WithAnnotation mutated the receiver")
	}
}

func TestReleaseProvenanceMustBeProvenance(t *testing.T) {
	r := valid()
	r.Provenance = provenance.DocumentRef{
		Kind:   provenance.DocumentSBOM,
		Format: "spdx-json",
		Digest: digestOfTestBytes(),
	}
	if err := r.Validate(); err == nil {
		t.Error("Validate() accepted an sbom in the provenance slot")
	}
	r.Provenance.Kind = provenance.DocumentProvenance
	r.Provenance.Format = "slsa-v1"
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestNewStampsIdentityAttributionAndTime(t *testing.T) {
	g := identity.NewFixedGenerator("11111111-1111-7111-8111-111111111111")
	c := identity.NewFixedClock(at)
	r, err := New(g, c, author(), Release{
		ProjectID: projectID,
		Version:   "1.2.3",
		Artifacts: []Artifact{{ArtifactID: appID, Role: "app"}},
		Source:    source(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := identity.ReleaseID("rel_11111111-1111-7111-8111-111111111111"); r.ID != want {
		t.Errorf("ID = %q, want %q", r.ID, want)
	}
	if r.CreatedBy.ID != "alice" {
		t.Errorf("CreatedBy = %+v, want alice", r.CreatedBy)
	}
	if !r.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v", r.CreatedAt, at)
	}
}

// INV-011: a release is persisted and rendered widely. A credential that
// arrived in the author's claims must not travel with it.
func TestNewRedactsTheAuthorsClaims(t *testing.T) {
	by := identity.Principal{
		ID:     "ci",
		Type:   identity.PrincipalService,
		Claims: map[string]string{"email": "ci@example.com", "token": "ghp_realsecretvalue"},
	}
	r, err := New(identity.NewGenerator(), identity.NewFixedClock(at), by, Release{
		ProjectID: projectID,
		Version:   "1.2.3",
		Artifacts: []Artifact{{ArtifactID: appID, Role: "app"}},
		Source:    source(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.CreatedBy.Claims["token"]; got == "ghp_realsecretvalue" {
		t.Error("the author's credential was stored on the release")
	}
	if got := r.CreatedBy.Claims["email"]; got != "ci@example.com" {
		t.Errorf("a benign claim was lost: %q", got)
	}
}

func TestNewRefusesAnUnattributedRelease(t *testing.T) {
	_, err := New(identity.NewGenerator(), identity.NewFixedClock(at), identity.Principal{}, Release{
		ProjectID: projectID,
		Version:   "1.2.3",
		Artifacts: []Artifact{{ArtifactID: appID, Role: "app"}},
		Source:    source(),
	})
	if err == nil {
		t.Error("New accepted a release with no author")
	}
}

func TestNewPropagatesAGeneratorFailure(t *testing.T) {
	boom := errors.New("no entropy")
	g := identity.GeneratorFunc(func() (string, error) { return "", boom })
	if _, err := New(g, identity.NewFixedClock(at), author(), valid()); !errors.Is(err, boom) {
		t.Errorf("got %v, want the generator's error", err)
	}
}
