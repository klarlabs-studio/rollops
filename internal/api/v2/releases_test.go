package apiv2_test

import (
	"context"
	"testing"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
)

func (w *world) artifact(t *testing.T, p identity.ProjectID, tag string) artifact.Artifact {
	t.Helper()
	d := digest.Of([]byte(tag))
	a, err := artifact.New(w.ids, w.clock, artifact.Artifact{
		ProjectID: p,
		Kind:      artifact.KindOCIImage,
		Digest:    d,
		Locator:   "ghcr.io/example/" + tag + "@" + d.String(),
		Size:      4096,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Metadata:  map[string]string{"builder": "ko"},
	})
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}
	if err := w.store.Artifacts().Create(context.Background(), a); err != nil {
		t.Fatalf("Artifacts.Create: %v", err)
	}
	return a
}

func (w *world) release(t *testing.T, p identity.ProjectID, version string, roles map[string]identity.ArtifactID) release.Release {
	t.Helper()
	var arts []release.Artifact
	for role, id := range roles {
		arts = append(arts, release.Artifact{Role: role, ArtifactID: id})
	}
	r, err := release.New(w.ids, w.clock, identity.Principal{
		ID:          "alice@example.com",
		Type:        identity.PrincipalHuman,
		DisplayName: "Alice",
		Claims:      map[string]string{"token": "s3cret", "team": "platform"},
	}, release.Release{
		ProjectID: p,
		Version:   version,
		Artifacts: arts,
		Source: provenance.SourceRevision{
			Provider:   "github",
			Repository: "example/app",
			Revision:   "0d3c1f4e5a6b7c8d9e0f1a2b3c4d5e6f70819293",
			Ref:        "refs/heads/main",
		},
		Labels: map[string]string{"channel": "stable"},
	})
	if err != nil {
		t.Fatalf("release.New: %v", err)
	}
	if err := w.store.Releases().Create(context.Background(), r); err != nil {
		t.Fatalf("Releases.Create: %v", err)
	}
	return r
}

func TestAReleaseNamesItsArtifactsByRole(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	app := w.artifact(t, p.ID, "app")
	migrate := w.artifact(t, p.ID, "migrate")
	stored := w.release(t, p.ID, "2.0.0", map[string]identity.ArtifactID{
		"migration": migrate.ID,
		"app":       app.ID,
	})

	got, err := w.svc.GetRelease(context.Background(), apiv2.GetReleaseRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.Version != "2.0.0" || got.ProjectID != string(p.ID) {
		t.Errorf("release = %q of %q", got.Version, got.ProjectID)
	}
	if len(got.Artifacts) != 2 {
		t.Fatalf("got %d artifacts, want 2", len(got.Artifacts))
	}
	if got.Artifacts[0].Role != "app" || got.Artifacts[1].Role != "migration" {
		t.Errorf("roles = %q, %q; want them sorted so two reads agree", got.Artifacts[0].Role, got.Artifacts[1].Role)
	}
	if got.Artifacts[0].ArtifactID != string(app.ID) {
		t.Errorf("app artifact = %q, want %q", got.Artifacts[0].ArtifactID, app.ID)
	}
	if got.Source.Revision != "0d3c1f4e5a6b7c8d9e0f1a2b3c4d5e6f70819293" {
		t.Errorf("source revision = %q", got.Source.Revision)
	}
}

func TestTheAuthorOfAReleaseIsNamedWithoutTheirClaims(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	stored := w.release(t, p.ID, "2.0.0", map[string]identity.ArtifactID{"app": w.artifact(t, p.ID, "app").ID})

	got, err := w.svc.GetRelease(context.Background(), apiv2.GetReleaseRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.CreatedBy.ID != "alice@example.com" || got.CreatedBy.Type != string(identity.PrincipalHuman) {
		t.Errorf("author = %+v", got.CreatedBy)
	}
	if got.CreatedBy.DisplayName != "Alice" {
		t.Errorf("display name = %q", got.CreatedBy.DisplayName)
	}
}

func TestReleasesAreListedForOneProject(t *testing.T) {
	w := setup(t)
	mine := w.project(t, "checkout")
	theirs := w.project(t, "billing")
	w.release(t, mine.ID, "1.0.0", map[string]identity.ArtifactID{"app": w.artifact(t, mine.ID, "a").ID})
	w.release(t, mine.ID, "2.0.0", map[string]identity.ArtifactID{"app": w.artifact(t, mine.ID, "b").ID})
	w.release(t, theirs.ID, "1.0.0", map[string]identity.ArtifactID{"app": w.artifact(t, theirs.ID, "c").ID})

	got, err := w.svc.ListReleases(context.Background(), apiv2.ListReleasesRequest{ProjectID: string(mine.ID)})
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(got.Releases) != 2 {
		t.Fatalf("got %d releases, want the 2 belonging to checkout", len(got.Releases))
	}
	for _, r := range got.Releases {
		if r.ProjectID != string(mine.ID) {
			t.Errorf("release %q belongs to %q", r.Version, r.ProjectID)
		}
	}
}

func TestAReleaseIDOfTheWrongKindIsTheCallersMistake(t *testing.T) {
	w := setup(t)

	_, err := w.svc.GetRelease(context.Background(), apiv2.GetReleaseRequest{ID: "prj_0199a0dd-0000-7000-8000-000000000000"})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAReleaseNobodyCreatedIsNotFound(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	absent := w.release(t, p.ID, "2.0.0", map[string]identity.ArtifactID{"app": w.artifact(t, p.ID, "app").ID})

	_, err := setup(t).svc.GetRelease(context.Background(), apiv2.GetReleaseRequest{ID: string(absent.ID)})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestListingReleasesNeedsAProjectToListThemFor(t *testing.T) {
	w := setup(t)

	_, err := w.svc.ListReleases(context.Background(), apiv2.ListReleasesRequest{})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAnArtifactSaysWhereItIsAndWhatItMustHashTo(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	stored := w.artifact(t, p.ID, "app")

	got, err := w.svc.GetArtifact(context.Background(), apiv2.GetArtifactRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.Kind != string(artifact.KindOCIImage) {
		t.Errorf("kind = %q", got.Kind)
	}
	if got.Digest != stored.Digest.String() {
		t.Errorf("digest = %q, want %q", got.Digest, stored.Digest)
	}
	if got.Locator != stored.Locator {
		t.Errorf("locator = %q, want %q", got.Locator, stored.Locator)
	}
	if got.Size != 4096 || got.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("artifact = %d bytes of %q", got.Size, got.MediaType)
	}
	if got.Metadata["builder"] != "ko" {
		t.Errorf("metadata = %v", got.Metadata)
	}
}

func TestAnArtifactCarriesTheDocumentsThatAttestToIt(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	d := digest.Of([]byte("sbom"))
	a, err := artifact.New(w.ids, w.clock, artifact.Artifact{
		ProjectID: p.ID,
		Kind:      artifact.KindOCIImage,
		Digest:    digest.Of([]byte("app")),
		Locator:   "ghcr.io/example/app@" + digest.Of([]byte("app")).String(),
		SBOMs: []provenance.DocumentRef{{
			Kind:    provenance.DocumentSBOM,
			Format:  "spdx-json",
			Locator: "ghcr.io/example/app:sbom",
			Digest:  d,
		}},
	})
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}
	if err := w.store.Artifacts().Create(context.Background(), a); err != nil {
		t.Fatalf("Artifacts.Create: %v", err)
	}

	got, err := w.svc.GetArtifact(context.Background(), apiv2.GetArtifactRequest{ID: string(a.ID)})
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if len(got.SBOMs) != 1 {
		t.Fatalf("got %d sboms, want 1", len(got.SBOMs))
	}
	if got.SBOMs[0].Format != "spdx-json" || got.SBOMs[0].Digest != d.String() {
		t.Errorf("sbom = %+v", got.SBOMs[0])
	}
}

func TestArtifactsAreListedForOneProject(t *testing.T) {
	w := setup(t)
	mine := w.project(t, "checkout")
	theirs := w.project(t, "billing")
	w.artifact(t, mine.ID, "a")
	w.artifact(t, mine.ID, "b")
	w.artifact(t, theirs.ID, "c")

	got, err := w.svc.ListArtifacts(context.Background(), apiv2.ListArtifactsRequest{ProjectID: string(mine.ID)})
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(got.Artifacts) != 2 {
		t.Fatalf("got %d artifacts, want the 2 belonging to checkout", len(got.Artifacts))
	}
}

func TestAnArtifactNobodyRegisteredIsNotFound(t *testing.T) {
	w := setup(t)
	absent := w.artifact(t, w.project(t, "checkout").ID, "app")

	_, err := setup(t).svc.GetArtifact(context.Background(), apiv2.GetArtifactRequest{ID: string(absent.ID)})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestAnArtifactIDOfTheWrongKindIsTheCallersMistake(t *testing.T) {
	w := setup(t)

	_, err := w.svc.GetArtifact(context.Background(), apiv2.GetArtifactRequest{ID: "rel_0199a0dd-0000-7000-8000-000000000000"})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestListingArtifactsNeedsAProjectToListThemFor(t *testing.T) {
	w := setup(t)

	_, err := w.svc.ListArtifacts(context.Background(), apiv2.ListArtifactsRequest{})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}
