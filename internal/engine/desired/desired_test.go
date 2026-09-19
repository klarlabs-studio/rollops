package desired_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/engine/desired"
	"go.klarlabs.de/rollops/internal/engine/planner"
	"go.klarlabs.de/rollops/internal/store/memory"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// world is a renderer over a store holding a project and its artifacts.
type world struct {
	store     *memory.Store
	renderer  *desired.Renderer
	artifacts port.ArtifactRepository
	ids       identity.Generator
	clock     identity.Clock
	project   identity.ProjectID
}

func setup(t *testing.T) *world {
	t.Helper()
	store := memory.New()
	r, err := desired.New(store.Artifacts())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := &world{
		store:     store,
		renderer:  r,
		artifacts: store.Artifacts(),
		ids:       identity.NewGenerator(),
		clock:     identity.NewFixedClock(at),
	}
	w.project = newProject(t, store, w.ids, w.clock, "checkout")
	return w
}

func newProject(
	t *testing.T, store *memory.Store, ids identity.Generator, clock identity.Clock, name string,
) identity.ProjectID {
	t.Helper()
	p, err := project.New(ids, clock, project.Project{Name: name})
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	if err := store.Projects().Create(context.Background(), p); err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}
	return p.ID
}

// register records an image artifact and returns its id.
func (w *world) register(t *testing.T, tag string) identity.ArtifactID {
	t.Helper()
	d := digest.Of([]byte(tag))
	a, err := artifact.New(w.ids, w.clock, artifact.Artifact{
		ProjectID: w.project,
		Kind:      artifact.KindOCIImage,
		Digest:    d,
		Locator:   "ghcr.io/example/" + tag + "@" + d.String(),
		Size:      1024,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
	})
	if err != nil {
		t.Fatalf("artifact.New: %v", err)
	}
	if err := w.artifacts.Create(context.Background(), a); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return a.ID
}

func (w *world) release(t *testing.T, roles map[string]identity.ArtifactID) release.Release {
	t.Helper()
	r := release.Release{
		ProjectID: w.project,
		Version:   "2.0.0",
		Source: provenance.SourceRevision{
			Provider:   "github",
			Repository: "example/app",
			Revision:   "0d3c1f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e",
		},
	}
	for _, role := range sortedRoles(roles) {
		r.Artifacts = append(r.Artifacts, release.Artifact{ArtifactID: roles[role], Role: role})
	}
	out, err := release.New(w.ids, w.clock,
		identity.Principal{ID: "u-1", Type: identity.PrincipalHuman}, r)
	if err != nil {
		t.Fatalf("release.New: %v", err)
	}
	return out
}

func sortedRoles(m map[string]identity.ArtifactID) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (w *world) request(rel release.Release, cfg map[string]value.Ref) planner.DesiredRequest {
	return planner.DesiredRequest{
		Environment: environment.Environment{
			ID: "env-1", ProjectID: w.project, Name: "production",
			Kind: environment.KindProduction,
		},
		Binding: environment.TargetBinding{Name: "api", Driver: "kubernetes", Config: cfg},
		Release: rel,
	}
}

func (w *world) render(t *testing.T, req planner.DesiredRequest) targetv2.DesiredState {
	t.Helper()
	got, err := w.renderer.DesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("DesiredState: %v", err)
	}
	return got
}

func decode(t *testing.T, s targetv2.DesiredState) desired.Document {
	t.Helper()
	var doc desired.Document
	if err := json.Unmarshal(s.Spec, &doc); err != nil {
		t.Fatalf("the spec is not a %s document: %v", s.Kind, err)
	}
	return doc
}

func TestTheDocumentNamesTheReleaseAndEveryArtifact(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{
		"app":       w.register(t, "app"),
		"migration": w.register(t, "migration"),
	})

	doc := decode(t, w.render(t, w.request(rel, nil)))

	if doc.Release.Version != "2.0.0" {
		t.Errorf("version %q, want 2.0.0", doc.Release.Version)
	}
	if doc.Release.ID != string(rel.ID) {
		t.Errorf("release %q, want %q", doc.Release.ID, rel.ID)
	}
	if len(doc.Artifacts) != 2 {
		t.Fatalf("%d artifacts, want 2", len(doc.Artifacts))
	}
	for _, a := range doc.Artifacts {
		if a.Digest == "" || a.Locator == "" || a.Kind == "" {
			t.Errorf("artifact %q is not deployable: %+v", a.Role, a)
		}
		if !strings.HasSuffix(a.Locator, "@"+a.Digest) {
			t.Errorf("artifact %q is not pinned: %s", a.Role, a.Locator)
		}
	}
}

func TestArtifactsComeOutInRoleOrder(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{
		"worker": w.register(t, "worker"),
		"app":    w.register(t, "app"),
		"web":    w.register(t, "web"),
	})

	doc := decode(t, w.render(t, w.request(rel, nil)))

	roles := make([]string, 0, len(doc.Artifacts))
	for _, a := range doc.Artifacts {
		roles = append(roles, a.Role)
	}
	if want := []string{"app", "web", "worker"}; !slices.Equal(roles, want) {
		t.Fatalf("roles %v, want %v", roles, want)
	}
}

func TestTwoRendersOfOneReleaseAreIdentical(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})
	req := w.request(rel, nil)

	first, second := w.render(t, req), w.render(t, req)

	if first.Checksum != second.Checksum {
		t.Fatalf("checksums %q and %q differ", first.Checksum, second.Checksum)
	}
	if string(first.Spec) != string(second.Spec) {
		t.Fatal("the same release rendered to different bytes")
	}
}

func TestTwoReleasesRenderToDifferentChecksums(t *testing.T) {
	w := setup(t)
	one := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})
	two := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app-next")})

	if a, b := w.render(t, w.request(one, nil)), w.render(t, w.request(two, nil)); a.Checksum == b.Checksum {
		t.Fatalf("two releases share checksum %q", a.Checksum)
	}
}

func TestTheChecksumIdentifiesTheDocument(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})

	got := w.render(t, w.request(rel, nil))

	if want := digest.Of(got.Spec).String(); got.Checksum != want {
		t.Fatalf("checksum %q, want %q", got.Checksum, want)
	}
}

func TestAnArtifactThatIsNotRegisteredNamesItsRole(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})
	rel.Artifacts[0].ArtifactID = "art_00000000-0000-4000-8000-0000000000ff"

	_, err := w.renderer.DesiredState(context.Background(), w.request(rel, nil))

	if err == nil {
		t.Fatal("a release referencing nothing was rendered anyway")
	}
	if !strings.Contains(err.Error(), "app") {
		t.Errorf("error %q does not name the role", err)
	}
}

func TestAnArtifactFromAnotherProjectIsRefused(t *testing.T) {
	w := setup(t)
	other := &world{
		store: w.store, renderer: w.renderer, artifacts: w.artifacts, ids: w.ids, clock: w.clock,
		project: newProject(t, w.store, w.ids, w.clock, "billing"),
	}
	rel := w.release(t, map[string]identity.ArtifactID{"app": other.register(t, "app")})

	_, err := w.renderer.DesiredState(context.Background(), w.request(rel, nil))

	if err == nil {
		t.Fatal("an artifact belonging to another project was deployed")
	}
}

func TestNoTargetConfigurationReachesTheDocument(t *testing.T) {
	// The target is configured at construction, from the same binding. Copying
	// that configuration into a document the target is asked to converge on
	// would put a resolved credential into bytes that get checksummed, logged
	// by a plugin and passed over a subprocess boundary (INV-012).
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})

	got := w.render(t, w.request(rel, map[string]value.Ref{
		"kubeconfig": value.Secret("prod-kubeconfig"),
		"namespace":  value.Literal("payments"),
	}))

	for _, leak := range []string{"kubeconfig", "prod-kubeconfig", "payments"} {
		if strings.Contains(string(got.Spec), leak) {
			t.Errorf("the document carries %q", leak)
		}
	}
}

func TestTheDocumentDeclaresTheShapeItIsIn(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})

	if got := w.render(t, w.request(rel, nil)); got.Kind != desired.Kind {
		t.Fatalf("kind %q, want %q", got.Kind, desired.Kind)
	}
}

func TestLabelsSayWhatIsBeingDeployedAndWhere(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})

	got := w.render(t, w.request(rel, nil))

	for key, want := range map[string]string{
		"rollops.io/release":     string(rel.ID),
		"rollops.io/version":     "2.0.0",
		"rollops.io/environment": "production",
		"rollops.io/target":      "api",
	} {
		if got.Labels[key] != want {
			t.Errorf("label %s is %q, want %q", key, got.Labels[key], want)
		}
	}
}

func TestNothingIsRenderedForTheTarget(t *testing.T) {
	// Rendered is for a spec that pointed somewhere and was resolved. This
	// document points at nothing: it is the whole desired state as far as
	// RollOps knows it, and the target resolves the rest from its own config.
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})

	if got := w.render(t, w.request(rel, nil)); got.Rendered != nil {
		t.Fatalf("rendered %q, want none", got.Rendered)
	}
}

func TestARendererNeedsSomewhereToLookUpArtifacts(t *testing.T) {
	if _, err := desired.New(nil); err == nil {
		t.Fatal("a renderer with no artifact repository was built anyway")
	}
}

func TestTheRendererIsWhatThePlannerAsksFor(t *testing.T) {
	var _ planner.Desired = setup(t).renderer
}

func TestAReleaseWithNothingToDeployIsRefused(t *testing.T) {
	w := setup(t)
	rel := w.release(t, map[string]identity.ArtifactID{"app": w.register(t, "app")})
	rel.Artifacts = nil

	if _, err := w.renderer.DesiredState(context.Background(), w.request(rel, nil)); err == nil {
		t.Fatal("a release with no artifacts was rendered anyway")
	} else if !errors.Is(err, desired.ErrNothingToDeploy) {
		t.Fatalf("err %v, want ErrNothingToDeploy", err)
	}
}
