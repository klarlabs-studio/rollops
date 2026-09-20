package apiv2_test

import (
	"context"
	"testing"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/store/memory"
)

// author carries a token because a real one does: claims are what policy is
// evaluated against, and an identity provider routinely puts a credential in
// there. Nothing that reaches a caller may carry it back out (INV-012).
func author() identity.Principal {
	return identity.Principal{
		ID:          "alice@example.com",
		Type:        identity.PrincipalHuman,
		DisplayName: "Alice",
		Claims:      map[string]string{"token": "s3cret", "team": "platform"},
	}
}

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
	r, err := release.New(w.ids, w.clock, author(), release.Release{
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

// registering is the request every write-side artifact test starts from.
func registering(p identity.ProjectID, tag string) apiv2.RegisterArtifactRequest {
	d := digest.Of([]byte(tag))
	return apiv2.RegisterArtifactRequest{
		ProjectID: string(p),
		Kind:      string(artifact.KindOCIImage),
		Digest:    d.String(),
		Locator:   "ghcr.io/example/" + tag + "@" + d.String(),
		Size:      4096,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Metadata:  map[string]string{"builder": "ko"},
		Actor:     author(),
	}
}

func (w *world) registered(t *testing.T, req apiv2.RegisterArtifactRequest) apiv2.Artifact {
	t.Helper()
	a, err := w.svc.RegisterArtifact(context.Background(), req)
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	return a
}

// creating is the request every write-side release test starts from. It
// registers the artifact through the API too, because that is the path a
// caller has: nothing else can put one there.
func (w *world) creating(t *testing.T, p identity.ProjectID) apiv2.CreateReleaseRequest {
	t.Helper()
	a := w.registered(t, registering(p, "app"))
	return apiv2.CreateReleaseRequest{
		ProjectID: string(p),
		Version:   "1.4.0",
		Artifacts: []apiv2.ReleaseArtifact{{Role: "app", ArtifactID: a.ID}},
		Source: apiv2.Source{
			Provider:   "github",
			Repository: "example/app",
			Revision:   "0d3c1f4e5a6b7c8d9e0f1a2b3c4d5e6f70819293",
			Ref:        "refs/heads/main",
		},
		Labels: map[string]string{"channel": "stable"},
		Actor:  author(),
	}
}

func TestRegisteringAnArtifactReturnsWhereItIsAndWhatItHashesTo(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := registering(p.ID, "app")

	got := w.registered(t, req)

	if got.ID == "" {
		t.Fatal("the registered artifact has no id")
	}
	if got.Digest != req.Digest {
		t.Errorf("digest = %q, want %q", got.Digest, req.Digest)
	}
	if got.Locator != req.Locator {
		t.Errorf("locator = %q, want %q", got.Locator, req.Locator)
	}
	// Readable through the endpoint that was already there, which is the point
	// of registering it.
	read, err := w.svc.GetArtifact(context.Background(), apiv2.GetArtifactRequest{ID: got.ID})
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if read.ID != got.ID {
		t.Errorf("read back %q, want %q", read.ID, got.ID)
	}
}

// The digest is the idempotency key, which is why this endpoint does not take
// one. A build that reruns registers the same content and gets the same
// artifact rather than a conflict it would have to interpret.
func TestRegisteringTheSameArtifactTwiceNeedsNoIdempotencyKey(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := registering(p.ID, "app")

	first := w.registered(t, req)
	second := w.registered(t, req)

	if second.ID != first.ID {
		t.Errorf("second registration = %q, want the %q already recorded", second.ID, first.ID)
	}
}

func TestAMalformedRegistrationIsTheCallersMistake(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")

	for _, tc := range []struct {
		name string
		edit func(*apiv2.RegisterArtifactRequest)
	}{
		{"project of the wrong kind", func(r *apiv2.RegisterArtifactRequest) {
			r.ProjectID = "rel_0199a0dd-0000-7000-8000-000000000000"
		}},
		{"digest that is not one", func(r *apiv2.RegisterArtifactRequest) { r.Digest = "not-a-digest" }},
		{"kind nothing can build", func(r *apiv2.RegisterArtifactRequest) { r.Kind = "interpretive-dance" }},
		// The reason the domain checks it: a tag resolved again at pull time can
		// hand production a different image than the one that was verified.
		{"locator that is not pinned", func(r *apiv2.RegisterArtifactRequest) {
			r.Locator = "ghcr.io/example/app:latest"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := registering(p.ID, "app")
			tc.edit(&req)

			_, err := w.svc.RegisterArtifact(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

func TestRegisteringAnArtifactForAProjectNobodyCreatedIsNotFound(t *testing.T) {
	w := setup(t)
	req := registering(identity.ProjectID("prj_0199a0dd-0000-7000-8000-00000000000a"), "app")

	_, err := w.svc.RegisterArtifact(context.Background(), req)

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestCreatingAReleaseFixesTheArtifactsItWillDeploy(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := w.creating(t, p.ID)

	got, err := w.svc.CreateRelease(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	if got.Version != "1.4.0" {
		t.Errorf("version = %q, want 1.4.0", got.Version)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].ArtifactID != req.Artifacts[0].ArtifactID {
		t.Errorf("artifacts = %v, want the one that was named", got.Artifacts)
	}
	if got.Source.Revision != req.Source.Revision {
		t.Errorf("revision = %q, want %q", got.Source.Revision, req.Source.Revision)
	}
	if got.Labels["channel"] != "stable" {
		t.Errorf("labels = %v", got.Labels)
	}
}

// The gap this endpoint closes: CreatePlan needs a release, and until now
// nothing a caller could reach made one.
func TestAReleaseCreatedThroughTheAPICanBePlannedAgainst(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	env := w.environment(t, p.ID, environment.Environment{
		Name: "production",
		Targets: []environment.TargetBinding{{
			Name:   "api",
			Driver: "kubernetes",
			Config: map[string]value.Ref{"namespace": value.Literal("payments")},
		}},
	})
	r, err := w.svc.CreateRelease(context.Background(), w.creating(t, p.ID))
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	got, err := w.svc.CreatePlan(context.Background(), apiv2.CreatePlanRequest{
		EnvironmentID: string(env.ID),
		ReleaseID:     r.ID,
		Strategy:      string(deployment.StrategyRolling),
		Actor:         author(),
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if got.ReleaseID != r.ID {
		t.Errorf("plan is for release %q, want %q", got.ReleaseID, r.ID)
	}
}

func TestTheAuthorOfACreatedReleaseIsNamedWithoutTheirClaims(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")

	got, err := w.svc.CreateRelease(context.Background(), w.creating(t, p.ID))
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	if got.CreatedBy.ID != author().ID {
		t.Errorf("author = %q, want %q", got.CreatedBy.ID, author().ID)
	}
	if got.CreatedBy.DisplayName != "Alice" {
		t.Errorf("display name = %q, want Alice", got.CreatedBy.DisplayName)
	}
}

// Projects are the isolation boundary. A release that could name another
// project's artifact would have a deployment run bytes built for somebody else.
func TestAReleaseCannotNameAnotherProjectsArtifact(t *testing.T) {
	w := setup(t)
	mine := w.project(t, "checkout")
	theirs := w.project(t, "billing")
	foreign := w.registered(t, registering(theirs.ID, "billing-app"))

	req := w.creating(t, mine.ID)
	req.Artifacts = []apiv2.ReleaseArtifact{{Role: "app", ArtifactID: foreign.ID}}

	_, err := w.svc.CreateRelease(context.Background(), req)

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAMalformedReleaseRequestIsTheCallersMistake(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")

	for _, tc := range []struct {
		name string
		edit func(*apiv2.CreateReleaseRequest)
	}{
		{"project of the wrong kind", func(r *apiv2.CreateReleaseRequest) {
			r.ProjectID = "rel_0199a0dd-0000-7000-8000-000000000000"
		}},
		{"artifact of the wrong kind", func(r *apiv2.CreateReleaseRequest) {
			r.Artifacts = []apiv2.ReleaseArtifact{{Role: "app", ArtifactID: "prj_0199a0dd-0000-7000-8000-000000000000"}}
		}},
		// Nothing to deploy is not a release. It would plan to no operations and
		// leave whoever applied it unable to say what they had shipped.
		{"nothing in it", func(r *apiv2.CreateReleaseRequest) { r.Artifacts = nil }},
		{"no version to ask for it by", func(r *apiv2.CreateReleaseRequest) { r.Version = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := w.creating(t, p.ID)
			tc.edit(&req)

			_, err := w.svc.CreateRelease(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

func TestAReleaseNamingAnArtifactNobodyRegisteredIsNotFound(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := w.creating(t, p.ID)
	req.Artifacts = []apiv2.ReleaseArtifact{
		{Role: "app", ArtifactID: "art_0199a0dd-0000-7000-8000-00000000000b"},
	}

	_, err := w.svc.CreateRelease(context.Background(), req)

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

// A version is how a release is asked for on every surface, so the second call
// is a conflict — which is exactly why this endpoint takes a key. Without one a
// retry would be indistinguishable from a second attempt to use the name.
func TestTwoCreateReleaseCallsWithOneKeyCreateOneRelease(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := w.creating(t, p.ID)
	req.IdempotencyKey = "key-1"

	first, err := w.svc.CreateRelease(context.Background(), req)
	if err != nil {
		t.Fatalf("first CreateRelease: %v", err)
	}
	second, err := w.svc.CreateRelease(context.Background(), req)
	if err != nil {
		t.Fatalf("second CreateRelease: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("second release = %q, want the %q the first call created", second.ID, first.ID)
	}
	listed, err := w.svc.ListReleases(context.Background(), apiv2.ListReleasesRequest{ProjectID: string(p.ID)})
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(listed.Releases) != 1 {
		t.Errorf("%d releases in the project, want 1", len(listed.Releases))
	}
}

func TestAReleaseKeyReusedForADifferentVersionIsRefused(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := w.creating(t, p.ID)
	req.IdempotencyKey = "key-1"
	if _, err := w.svc.CreateRelease(context.Background(), req); err != nil {
		t.Fatalf("first CreateRelease: %v", err)
	}

	req.Version = "1.5.0"
	_, err := w.svc.CreateRelease(context.Background(), req)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestReusingAVersionWithoutAKeyIsAConflict(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := w.creating(t, p.ID)
	if _, err := w.svc.CreateRelease(context.Background(), req); err != nil {
		t.Fatalf("first CreateRelease: %v", err)
	}

	_, err := w.svc.CreateRelease(context.Background(), req)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestAServiceWithoutARegistrarIsRefused(t *testing.T) {
	store := memory.New()
	cfg := config(store, &movableClock{now: at}, stubDeployer{})
	cfg.Registrar = nil

	_, err := apiv2.New(cfg)

	if err == nil {
		t.Fatal("a service with no registrar was built; nothing could create a release")
	}
}
