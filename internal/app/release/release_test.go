package release_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	app "go.klarlabs.de/rollops/internal/app/release"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/store/memory"
)

var start = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// actor carries a claim that must not reach storage. Every record this package
// writes is attributed, so a credential in the claims would be copied into the
// release, the artifact and the timeline at once (INV-012).
func actor() identity.Principal {
	return identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
		Claims: map[string]string{"token": "s3cret", "email": "ada@example.com"},
	}
}

type harness struct {
	store   *memory.Store
	gen     identity.Generator
	clock   fixedClock
	service *app.Service
	project project.Project
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gen := identity.NewSequenceGenerator()
	clk := fixedClock{now: start}
	store := memory.New()

	proj, err := project.New(gen, clk, project.Project{Name: "checkout"})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	if _, err := store.Projects().Create(context.Background(), proj); err != nil {
		t.Fatalf("store project: %v", err)
	}

	svc, err := app.New(app.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Artifacts:  store.Artifacts(),
		Releases:   store.Releases(),
		Events:     store.Events(),
		Clock:      clk,
		IDs:        gen,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{store: store, gen: gen, clock: clk, service: svc, project: proj}
}

// sibling is a second project in the same store, which is the only way to test
// the isolation boundary: two stores would fail for want of the artifact
// rather than for want of the project owning it.
func (h *harness) sibling(t *testing.T) project.Project {
	t.Helper()
	p, err := project.New(h.gen, h.clock, project.Project{Name: "billing"})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	if _, err := h.store.Projects().Create(context.Background(), p); err != nil {
		t.Fatalf("store project: %v", err)
	}
	return p
}

// registering is the command every artifact test starts from.
func (h *harness) registering() app.RegisterArtifactCommand {
	d := digest.Of([]byte("app"))
	return app.RegisterArtifactCommand{
		ProjectID: h.project.ID,
		Kind:      artifact.KindOCIImage,
		Digest:    d,
		Locator:   "oci://registry.example/app@" + d.String(),
		Size:      4096,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Actor:     actor(),
	}
}

func (h *harness) register(t *testing.T, cmd app.RegisterArtifactCommand) artifact.Artifact {
	t.Helper()
	a, err := h.service.RegisterArtifact(context.Background(), cmd)
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	return a
}

// releasing names one registered artifact, which is the smallest release that
// is deployable at all.
func (h *harness) releasing(t *testing.T) app.CreateCommand {
	t.Helper()
	a := h.register(t, h.registering())
	return app.CreateCommand{
		ProjectID: h.project.ID,
		Version:   "1.4.0",
		Artifacts: []release.Artifact{{ArtifactID: a.ID, Role: "app"}},
		Source: provenance.SourceRevision{
			Provider:   provenance.ProviderGit,
			Repository: "klarlabs/rollops",
			Revision:   "5f2a1c9e7b3d4a6f8e0c2b4d6a8f0c2e4b6d8a0f",
			Ref:        "refs/heads/main",
		},
		Actor: actor(),
	}
}

func (h *harness) timeline(t *testing.T) []event.Event {
	t.Helper()
	es, err := h.store.Events().Timeline(context.Background(), port.Page{})
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	return es
}

func find(t *testing.T, es []event.Event, typ event.Type) event.Event {
	t.Helper()
	for _, e := range es {
		if e.Type == typ {
			return e
		}
	}
	t.Fatalf("no %s on a timeline of %d events", typ, len(es))
	return event.Event{}
}

// ── registering an artifact ──────────────────────────────────────────────────

func TestRegisteringAnArtifactRecordsWhereTheBytesAreAndWhatTheyHashTo(t *testing.T) {
	h := newHarness(t)
	cmd := h.registering()

	got, err := h.service.RegisterArtifact(context.Background(), cmd)
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no artifact id; nothing could name it in a release")
	}
	if got.Digest != cmd.Digest {
		t.Errorf("digest = %s, want %s", got.Digest, cmd.Digest)
	}
	if got.Locator != cmd.Locator {
		t.Errorf("locator = %q, want %q", got.Locator, cmd.Locator)
	}

	stored, err := h.store.Artifacts().Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Artifacts.Get: %v", err)
	}
	if stored.Digest != cmd.Digest {
		t.Errorf("persisted digest = %s, want %s", stored.Digest, cmd.Digest)
	}
}

// Content is what identifies an artifact, so registering the same digest twice
// is the same artifact arriving twice rather than a conflict to report. A
// build that reruns should not have to know whether it is the first.
func TestRegisteringTheSameArtifactTwiceReturnsTheOneAlreadyRegistered(t *testing.T) {
	h := newHarness(t)
	cmd := h.registering()
	first := h.register(t, cmd)

	second := h.register(t, cmd)

	if second.ID != first.ID {
		t.Errorf("second registration = %q, want the %q already recorded", second.ID, first.ID)
	}
	all, err := h.store.Artifacts().List(context.Background(), h.project.ID)
	if err != nil {
		t.Fatalf("Artifacts.List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("%d artifacts stored, want 1", len(all))
	}
}

// A locator that is not pinned to the digest can hand a different image to
// production than the one that was verified, so it is refused here rather than
// discovered at pull time.
func TestAnArtifactWhoseLocatorIsNotPinnedIsRefused(t *testing.T) {
	h := newHarness(t)
	cmd := h.registering()
	cmd.Locator = "oci://registry.example/app:latest"

	_, err := h.service.RegisterArtifact(context.Background(), cmd)

	if !errors.Is(err, artifact.ErrUnpinnedLocator) {
		t.Fatalf("err = %v, want ErrUnpinnedLocator", err)
	}
	// Also marked as the caller's, so that a transport answers with the code
	// that sends them back to their request rather than one that has them
	// reporting an outage.
	if !errors.Is(err, app.ErrRejected) {
		t.Errorf("err = %v, want it marked ErrRejected", err)
	}
}

func TestRegisteringAnArtifactForAProjectNobodyCreatedIsNotFound(t *testing.T) {
	h := newHarness(t)
	cmd := h.registering()
	cmd.ProjectID = identity.ProjectID("prj_0199a0dd-0000-7000-8000-000000000009")

	_, err := h.service.RegisterArtifact(context.Background(), cmd)

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestARegisteredArtifactIsOnTheTimeline(t *testing.T) {
	h := newHarness(t)

	a := h.register(t, h.registering())

	e := find(t, h.timeline(t), event.ArtifactRegistered)
	if e.AggregateID != string(a.ID) {
		t.Errorf("aggregate = %q, want the artifact %q", e.AggregateID, a.ID)
	}
	if e.Principal.ID != actor().ID {
		t.Errorf("actor = %q, want %q", e.Principal.ID, actor().ID)
	}
}

// Whether an artifact arrived with anything vouching for it is the auditable
// fact, so the event counts the attestations rather than naming them. The
// locator is left out on purpose: it names a registry, and a private one's
// host is a detail the people who read a timeline should not need.
func TestTheArtifactEventCountsItsAttestationsAndNamesNoRegistry(t *testing.T) {
	h := newHarness(t)
	cmd := h.registering()
	cmd.Provenance = document(provenance.DocumentProvenance, "slsa")
	cmd.SBOMs = []provenance.DocumentRef{document(provenance.DocumentSBOM, "sbom")}
	cmd.Signatures = []provenance.DocumentRef{document(provenance.DocumentSignature, "sig")}
	h.register(t, cmd)

	e := find(t, h.timeline(t), event.ArtifactRegistered)
	body := string(e.Payload)
	if !strings.Contains(body, `"attestations":3`) {
		t.Errorf("payload %s does not count the three attestations", body)
	}
	if strings.Contains(body, "registry.example") {
		t.Errorf("payload %s names the registry the bytes came from", body)
	}
}

// document is an attestation good enough to be accepted. What it points at
// does not matter here; that it is pinned to a digest does.
func document(kind provenance.DocumentKind, name string) provenance.DocumentRef {
	d := digest.Of([]byte(name))
	return provenance.DocumentRef{
		Kind:    kind,
		Format:  "application/json",
		Locator: "https://attest.example/" + name,
		Digest:  d,
	}
}

// An artifact that was already registered is not news. Appending a second
// event would have an auditor counting two builds where there was one.
func TestRegisteringTheSameArtifactTwiceRecordsItOnce(t *testing.T) {
	h := newHarness(t)
	cmd := h.registering()
	h.register(t, cmd)
	h.register(t, cmd)

	var n int
	for _, e := range h.timeline(t) {
		if e.Type == event.ArtifactRegistered {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d artifact.registered events, want 1", n)
	}
}

// ── creating a release ───────────────────────────────────────────────────────

func TestCreatingAReleaseFixesTheArtifactSetItWillDeploy(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)

	got, err := h.service.Create(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no release id; nothing could be planned against it")
	}
	if got.Version != cmd.Version {
		t.Errorf("version = %q, want %q", got.Version, cmd.Version)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Role != "app" {
		t.Errorf("artifacts = %v", got.Artifacts)
	}

	stored, err := h.store.Releases().Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Releases.Get: %v", err)
	}
	if stored.Fingerprint() != got.Fingerprint() {
		t.Error("the stored release is not the one that was returned")
	}
}

// The claims are what policy is evaluated against, and they routinely carry a
// token. A release is persisted and rendered on every surface, so a credential
// that reached it could never be recalled (INV-012).
func TestTheAuthorOfAReleaseIsRecordedWithoutTheirCredentials(t *testing.T) {
	h := newHarness(t)

	got, err := h.service.Create(context.Background(), h.releasing(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.CreatedBy.ID != actor().ID {
		t.Errorf("author = %q, want %q", got.CreatedBy.ID, actor().ID)
	}
	// The key survives — that a principal presented a token is part of what
	// policy saw. What must not survive is the token itself.
	if got.CreatedBy.Claims["token"] == actor().Claims["token"] {
		t.Error("the author's token was stored on the release")
	}
}

// A version is how a release is asked for on every surface, so two releases
// answering to one version in a project would make the name ambiguous.
func TestAVersionAlreadyUsedInTheProjectIsRefused(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	if _, err := h.service.Create(context.Background(), cmd); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err := h.service.Create(context.Background(), cmd)

	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
}

// A release naming an artifact nobody registered would be undeployable at
// exactly the wrong moment — when somebody applies a plan built from it.
func TestAReleaseNamingAnArtifactNobodyRegisteredIsRefused(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	cmd.Artifacts = []release.Artifact{{
		ArtifactID: identity.ArtifactID("art_0199a0dd-0000-7000-8000-000000000009"),
		Role:       "app",
	}}

	_, err := h.service.Create(context.Background(), cmd)

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Projects are the isolation boundary. A release that could reach across one
// would let a deployment in one project run bytes built for another.
func TestAReleaseCannotNameAnArtifactFromAnotherProject(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	elsewhere := h.registering()
	elsewhere.ProjectID = h.sibling(t).ID
	elsewhere.Digest = digest.Of([]byte("billing-app"))
	elsewhere.Locator = "oci://registry.example/billing@" + elsewhere.Digest.String()
	foreign := h.register(t, elsewhere)
	cmd.Artifacts = []release.Artifact{{ArtifactID: foreign.ID, Role: "app"}}

	_, err := h.service.Create(context.Background(), cmd)

	if !errors.Is(err, app.ErrForeignArtifact) {
		t.Fatalf("err = %v, want ErrForeignArtifact", err)
	}
}

func TestCreatingAReleaseForAProjectNobodyCreatedIsNotFound(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	cmd.ProjectID = identity.ProjectID("prj_0199a0dd-0000-7000-8000-000000000009")

	_, err := h.service.Create(context.Background(), cmd)

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAReleaseWithNothingInItIsRefused(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	cmd.Artifacts = nil

	_, err := h.service.Create(context.Background(), cmd)

	if err == nil {
		t.Fatal("a release with no artifacts was accepted; there is nothing to deploy")
	}
	if !strings.Contains(err.Error(), "artifact") {
		t.Errorf("err = %v; it does not say what was missing", err)
	}
	if !errors.Is(err, app.ErrRejected) {
		t.Errorf("err = %v, want it marked ErrRejected", err)
	}
}

func TestACreatedReleaseIsOnTheTimeline(t *testing.T) {
	h := newHarness(t)

	r, err := h.service.Create(context.Background(), h.releasing(t))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	e := find(t, h.timeline(t), event.ReleaseCreated)
	if e.AggregateID != string(r.ID) {
		t.Errorf("aggregate = %q, want the release %q", e.AggregateID, r.ID)
	}
	if e.Principal.ID != actor().ID {
		t.Errorf("actor = %q, want %q", e.Principal.ID, actor().ID)
	}
}

// The fingerprint is how "have we already built this" is answered, and the
// repository recomputes it rather than trusting a stored copy. Putting it on
// the timeline is what lets that question be asked of the log too.
func TestTheReleaseEventCarriesTheFingerprintAndNotTheLabels(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	cmd.Labels = map[string]string{"channel": "beta"}

	r, err := h.service.Create(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	e := find(t, h.timeline(t), event.ReleaseCreated)
	body := string(e.Payload)
	if !strings.Contains(body, r.Fingerprint().String()) {
		t.Errorf("payload %s does not carry the fingerprint", body)
	}
	if strings.Contains(body, "beta") {
		t.Errorf("payload %s carries a label, which is outside what the release is", body)
	}
}

// A release that failed to store must leave nothing behind. Without the
// transaction the timeline would describe a release nothing can deploy.
func TestAReleaseThatCouldNotBeStoredIsNotOnTheTimeline(t *testing.T) {
	h := newHarness(t)
	cmd := h.releasing(t)
	if _, err := h.service.Create(context.Background(), cmd); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	before := len(h.timeline(t))

	if _, err := h.service.Create(context.Background(), cmd); err == nil {
		t.Fatal("the duplicate version was accepted")
	}

	if got := len(h.timeline(t)); got != before {
		t.Errorf("%d events after the refusal, want the %d from before", got, before)
	}
}

func TestAServiceMissingADependencyIsRefusedAtConstruction(t *testing.T) {
	store := memory.New()
	complete := app.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Artifacts:  store.Artifacts(),
		Releases:   store.Releases(),
		Events:     store.Events(),
		Clock:      fixedClock{now: start},
		IDs:        identity.NewSequenceGenerator(),
	}
	for _, tc := range []struct {
		name  string
		spoil func(*app.Config)
	}{
		{"transactor", func(c *app.Config) { c.Transactor = nil }},
		{"projects", func(c *app.Config) { c.Projects = nil }},
		{"artifacts", func(c *app.Config) { c.Artifacts = nil }},
		{"releases", func(c *app.Config) { c.Releases = nil }},
		{"events", func(c *app.Config) { c.Events = nil }},
		{"clock", func(c *app.Config) { c.Clock = nil }},
		{"ids", func(c *app.Config) { c.IDs = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := complete
			tc.spoil(&cfg)

			_, err := app.New(cfg)

			if !errors.Is(err, app.ErrIncomplete) {
				t.Fatalf("err = %v, want ErrIncomplete", err)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("err = %v; it does not name what was missing", err)
			}
		})
	}
}
