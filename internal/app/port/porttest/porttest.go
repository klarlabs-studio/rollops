// Package porttest is the conformance suite every repository implementation
// must pass unchanged.
//
// It exists because the in-memory store is what use-case tests run against and
// the SQLite store is what production runs against. A difference between them
// is a bug that only appears in production, so the contract is asserted once
// here rather than twice, differently, in each package.
package porttest

import (
	"context"
	"errors"
	"fmt"
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
)

// Repositories is one implementation's full set. A suite run gets a fresh,
// empty set so that no test can observe another's writes.
type Repositories struct {
	Projects     port.ProjectRepository
	Environments port.EnvironmentRepository
	Artifacts    port.ArtifactRepository
	Releases     port.ReleaseRepository
	Tx           port.Transactor
}

// Factory builds an empty set of repositories for one test.
type Factory func(t *testing.T) Repositories

// Run executes the whole suite against the implementation newRepos builds.
func Run(t *testing.T, newRepos Factory) {
	t.Helper()
	suites := map[string]func(*testing.T, Factory){
		"projects":     runProjects,
		"environments": runEnvironments,
		"artifacts":    runArtifacts,
		"releases":     runReleases,
		"transactions": runTransactions,
	}
	for name, run := range suites {
		t.Run(name, func(t *testing.T) { run(t, newRepos) })
	}
}

// fixture builds valid aggregates with deterministic identity, so that a
// failure names the aggregate rather than a random id.
type fixture struct {
	gen   identity.Generator
	clock identity.Clock
}

func newFixture() *fixture {
	return &fixture{
		gen:   identity.NewSequenceGenerator(),
		clock: identity.NewFixedClock(time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)),
	}
}

func (f *fixture) project(t *testing.T, name string) project.Project {
	t.Helper()
	p, err := project.New(f.gen, f.clock, project.Project{
		Name:        name,
		Description: "a project",
		Labels:      map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	return p
}

func (f *fixture) environment(t *testing.T, p identity.ProjectID, name string) environment.Environment {
	t.Helper()
	e, err := environment.New(f.gen, environment.Environment{
		ProjectID: p,
		Name:      name,
		Kind:      environment.KindProduction,
		Targets: []environment.TargetBinding{
			{Name: "primary", Driver: "kubernetes", Config: map[string]value.Ref{
				"namespace": value.Literal("default"),
			}},
			{Name: "secondary", Driver: "kubernetes"},
		},
		Policies:  []environment.PolicyBinding{{Name: "no-friday", Ref: "policies/no-friday.cel", Mode: environment.PolicyEnforce}},
		Variables: map[string]value.Ref{"region": value.Literal("eu-central-1")},
		Labels:    map[string]string{"tier": "gold"},
		Lifecycle: environment.Lifecycle{TTL: 72 * time.Hour, DeleteOnClose: true},
	})
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	return e
}

func (f *fixture) artifact(t *testing.T, p identity.ProjectID, content string) artifact.Artifact {
	t.Helper()
	d := digest.Of([]byte(content))
	a, err := artifact.New(f.gen, f.clock, artifact.Artifact{
		ProjectID: p,
		Kind:      artifact.KindOCIImage,
		Digest:    d,
		Locator:   fmt.Sprintf("oci://registry.example/app@%s", d),
		Size:      4096,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		Metadata:  map[string]string{"builder": "ci"},
		// Provenance is populated because a document reference carries a digest,
		// and a digest is the one field in the model whose contents are
		// unreachable from outside its package. A store that serialises it
		// structurally loses it silently and still returns a plausible record.
		Provenance: f.document(provenance.DocumentProvenance, "slsa", content+"/provenance"),
		SBOMs: []provenance.DocumentRef{
			f.document(provenance.DocumentSBOM, "spdx-json", content+"/sbom"),
		},
		Signatures: []provenance.DocumentRef{
			f.document(provenance.DocumentSignature, "cosign", content+"/sig"),
		},
	})
	if err != nil {
		t.Fatalf("build artifact: %v", err)
	}
	return a
}

// document builds a reference whose digest is derived from seed, so that two
// references in one fixture differ in the field most likely to be dropped.
func (f *fixture) document(k provenance.DocumentKind, format, seed string) provenance.DocumentRef {
	return provenance.DocumentRef{
		Kind:    k,
		Format:  format,
		Locator: fmt.Sprintf("https://artifacts.example/%s/%s", k, format),
		Digest:  digest.Of([]byte(seed)),
	}
}

func (f *fixture) release(t *testing.T, p identity.ProjectID, version string, as []artifact.Artifact) release.Release {
	t.Helper()
	bound := make([]release.Artifact, len(as))
	for i, a := range as {
		bound[i] = release.Artifact{ArtifactID: a.ID, Role: fmt.Sprintf("role-%d", i)}
	}
	r, err := release.New(f.gen, f.clock, identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
	}, release.Release{
		ProjectID: p,
		Version:   version,
		Artifacts: bound,
		Source: provenance.SourceRevision{
			Provider:   provenance.ProviderGit,
			Repository: "klarlabs/rollops",
			Revision:   "5f2a1c9e7b3d4a6f8e0c2b4d6a8f0c2e4b6d8a0f",
			Ref:        "refs/heads/main",
		},
		Labels:     map[string]string{"channel": "stable"},
		Provenance: f.document(provenance.DocumentProvenance, "slsa", version),
	})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	return r
}

func runProjects(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	t.Run("a stored project reads back whole", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := r.Projects.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertProject(t, got, p)

		// A stored project has been persisted once, so it is at revision one —
		// not at zero, which means "never written" and refuses every update.
		if got.Revision != 1 {
			t.Errorf("Revision = %d, want 1", got.Revision)
		}
	})

	t.Run("a project is addressable by name", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Projects.GetByName(ctx, "checkout")
		if err != nil {
			t.Fatalf("GetByName: %v", err)
		}
		if got.ID != p.ID {
			t.Errorf("GetByName returned %s, want %s", got.ID, p.ID)
		}
	})

	t.Run("a name belongs to one project", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		if err := r.Projects.Create(ctx, f.project(t, "checkout")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		err := r.Projects.Create(ctx, f.project(t, "checkout"))
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("the same project cannot be created twice", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := r.Projects.Create(ctx, p); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("a project cannot be renamed onto a name in use", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		for _, n := range []string{"checkout", "billing"} {
			if err := r.Projects.Create(ctx, f.project(t, n)); err != nil {
				t.Fatalf("Create %s: %v", n, err)
			}
		}
		billing, err := r.Projects.GetByName(ctx, "billing")
		if err != nil {
			t.Fatalf("GetByName: %v", err)
		}
		clash, err := billing.Rename(f.clock, "checkout")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if _, err := r.Projects.Update(ctx, clash); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Update = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("missing projects are reported as missing", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		absent := f.project(t, "gone")
		if _, err := r.Projects.Get(ctx, absent.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
		if _, err := r.Projects.GetByName(ctx, "gone"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("GetByName = %v, want ErrNotFound", err)
		}
	})

	t.Run("an update advances the revision", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		stored, err := r.Projects.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		renamed, err := stored.Rename(f.clock, "basket")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}
		next, err := r.Projects.Update(ctx, renamed)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if next != stored.Revision.Next() {
			t.Errorf("Update committed at %d, want %d", next, stored.Revision.Next())
		}

		got, err := r.Projects.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "basket" {
			t.Errorf("Name = %q, want %q", got.Name, "basket")
		}
		if got.Revision != next {
			t.Errorf("Revision = %d, want %d", got.Revision, next)
		}
		// The old name is free again, or renaming would leak a reservation.
		if _, err := r.Projects.GetByName(ctx, "checkout"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("GetByName(old) = %v, want ErrNotFound", err)
		}
	})

	t.Run("a second writer loses", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		stale, err := r.Projects.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		winner, err := stale.Rename(f.clock, "basket")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if _, err := r.Projects.Update(ctx, winner); err != nil {
			t.Fatalf("Update: %v", err)
		}

		loser, err := stale.Rename(f.clock, "cart")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}
		if _, err := r.Projects.Update(ctx, loser); !errors.Is(err, port.ErrRevisionConflict) {
			t.Errorf("Update = %v, want ErrRevisionConflict", err)
		}
	})

	t.Run("a blind write is refused", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create: %v", err)
		}
		// p is the value Create was handed, so its revision is still zero:
		// the caller never read the project back.
		if _, err := r.Projects.Update(ctx, p); !errors.Is(err, port.ErrRevisionConflict) {
			t.Errorf("Update = %v, want ErrRevisionConflict", err)
		}
	})

	t.Run("updating a project that does not exist", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		p.Revision = 1
		if _, err := r.Projects.Update(ctx, p); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Update = %v, want ErrNotFound", err)
		}
	})

	t.Run("listing", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		empty, err := r.Projects.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(empty) != 0 {
			t.Errorf("List = %v, want none", empty)
		}
		for _, n := range []string{"checkout", "billing", "search"} {
			if err := r.Projects.Create(ctx, f.project(t, n)); err != nil {
				t.Fatalf("Create %s: %v", n, err)
			}
		}
		all, err := r.Projects.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 3 {
			t.Fatalf("List returned %d projects, want 3", len(all))
		}
		for _, p := range all {
			if p.Revision == 0 {
				t.Errorf("%s came back at revision zero, which no stored project is", p.Name)
			}
		}
	})
}

func runEnvironments(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	// seed returns a repository set holding one project, since an environment
	// cannot exist without one.
	seed := func(t *testing.T) (*fixture, Repositories, project.Project) {
		t.Helper()
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create project: %v", err)
		}
		return f, r, p
	}

	t.Run("a stored environment reads back whole", func(t *testing.T) {
		f, r, p := seed(t)
		e := f.environment(t, p.ID, "production")
		if err := r.Environments.Create(ctx, e); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Environments.Get(ctx, e.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertEnvironment(t, got, e)
		if got.Revision != 1 {
			t.Errorf("Revision = %d, want 1", got.Revision)
		}
	})

	t.Run("target bindings keep the order they were declared in", func(t *testing.T) {
		f, r, p := seed(t)
		e := f.environment(t, p.ID, "production")
		if err := r.Environments.Create(ctx, e); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Environments.Get(ctx, e.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Targets) != len(e.Targets) {
			t.Fatalf("got %d targets, want %d", len(got.Targets), len(e.Targets))
		}
		for i := range e.Targets {
			if got.Targets[i].Name != e.Targets[i].Name {
				t.Errorf("target %d = %q, want %q", i, got.Targets[i].Name, e.Targets[i].Name)
			}
		}
	})

	t.Run("a name is unique within a project, not across them", func(t *testing.T) {
		f, r, p := seed(t)
		if err := r.Environments.Create(ctx, f.environment(t, p.ID, "production")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		err := r.Environments.Create(ctx, f.environment(t, p.ID, "production"))
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}

		other := f.project(t, "billing")
		if err := r.Projects.Create(ctx, other); err != nil {
			t.Fatalf("Create project: %v", err)
		}
		if err := r.Environments.Create(ctx, f.environment(t, other.ID, "production")); err != nil {
			t.Errorf("a second project may also have a production: %v", err)
		}
	})

	t.Run("the same environment cannot be created twice", func(t *testing.T) {
		f, r, p := seed(t)
		e := f.environment(t, p.ID, "production")
		if err := r.Environments.Create(ctx, e); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := r.Environments.Create(ctx, e); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("an environment cannot be renamed onto a name in use", func(t *testing.T) {
		f, r, p := seed(t)
		for _, n := range []string{"production", "staging"} {
			if err := r.Environments.Create(ctx, f.environment(t, p.ID, n)); err != nil {
				t.Fatalf("Create %s: %v", n, err)
			}
		}
		staging, err := r.Environments.GetByName(ctx, p.ID, "staging")
		if err != nil {
			t.Fatalf("GetByName: %v", err)
		}
		staging.Name = "production"
		if _, err := r.Environments.Update(ctx, staging); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Update = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("an environment needs a project that exists", func(t *testing.T) {
		f, r, _ := seed(t)
		absent := f.project(t, "gone")
		err := r.Environments.Create(ctx, f.environment(t, absent.ID, "production"))
		if !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create = %v, want ErrNotFound", err)
		}
	})

	t.Run("an update replaces the bindings rather than adding to them", func(t *testing.T) {
		f, r, p := seed(t)
		e := f.environment(t, p.ID, "production")
		if err := r.Environments.Create(ctx, e); err != nil {
			t.Fatalf("Create: %v", err)
		}
		stored, err := r.Environments.Get(ctx, e.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		stored.Targets = []environment.TargetBinding{{Name: "only", Driver: "ssh"}}
		stored.Policies = nil
		if _, err := r.Environments.Update(ctx, stored); err != nil {
			t.Fatalf("Update: %v", err)
		}
		got, err := r.Environments.Get(ctx, e.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Targets) != 1 || got.Targets[0].Name != "only" {
			t.Errorf("targets = %v, want just \"only\"", got.Targets)
		}
		if len(got.Policies) != 0 {
			t.Errorf("policies = %v, want none", got.Policies)
		}
	})

	t.Run("a second writer loses", func(t *testing.T) {
		f, r, p := seed(t)
		e := f.environment(t, p.ID, "production")
		if err := r.Environments.Create(ctx, e); err != nil {
			t.Fatalf("Create: %v", err)
		}
		stale, err := r.Environments.Get(ctx, e.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if _, err := r.Environments.Update(ctx, stale); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if _, err := r.Environments.Update(ctx, stale); !errors.Is(err, port.ErrRevisionConflict) {
			t.Errorf("Update = %v, want ErrRevisionConflict", err)
		}
	})

	t.Run("lookup by name and listing are scoped to the project", func(t *testing.T) {
		f, r, p := seed(t)
		other := f.project(t, "billing")
		if err := r.Projects.Create(ctx, other); err != nil {
			t.Fatalf("Create project: %v", err)
		}
		if err := r.Environments.Create(ctx, f.environment(t, p.ID, "production")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := r.Environments.Create(ctx, f.environment(t, p.ID, "staging")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := r.Environments.Create(ctx, f.environment(t, other.ID, "production")); err != nil {
			t.Fatalf("Create: %v", err)
		}

		mine, err := r.Environments.List(ctx, p.ID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(mine) != 2 {
			t.Errorf("List returned %d environments, want 2", len(mine))
		}
		got, err := r.Environments.GetByName(ctx, other.ID, "production")
		if err != nil {
			t.Fatalf("GetByName: %v", err)
		}
		if got.ProjectID != other.ID {
			t.Errorf("GetByName crossed into project %s", got.ProjectID)
		}
		if _, err := r.Environments.GetByName(ctx, other.ID, "staging"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("GetByName = %v, want ErrNotFound", err)
		}
	})

	t.Run("missing environments are reported as missing", func(t *testing.T) {
		f, r, p := seed(t)
		absent := f.environment(t, p.ID, "production")
		if _, err := r.Environments.Get(ctx, absent.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
		absent.Revision = 1
		if _, err := r.Environments.Update(ctx, absent); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Update = %v, want ErrNotFound", err)
		}
	})
}

func runArtifacts(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	seed := func(t *testing.T) (*fixture, Repositories, project.Project) {
		t.Helper()
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create project: %v", err)
		}
		return f, r, p
	}

	t.Run("a stored artifact reads back whole", func(t *testing.T) {
		f, r, p := seed(t)
		a := f.artifact(t, p.ID, "app-v1")
		if err := r.Artifacts.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Artifacts.Get(ctx, a.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertArtifact(t, got, a)
	})

	t.Run("registering one digest twice is a conflict, not a second row", func(t *testing.T) {
		f, r, p := seed(t)
		if err := r.Artifacts.Create(ctx, f.artifact(t, p.ID, "app-v1")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		err := r.Artifacts.Create(ctx, f.artifact(t, p.ID, "app-v1"))
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("an artifact is addressable by content", func(t *testing.T) {
		f, r, p := seed(t)
		a := f.artifact(t, p.ID, "app-v1")
		if err := r.Artifacts.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Artifacts.GetByDigest(ctx, p.ID, a.Kind, a.Digest.String())
		if err != nil {
			t.Fatalf("GetByDigest: %v", err)
		}
		if got.ID != a.ID {
			t.Errorf("GetByDigest returned %s, want %s", got.ID, a.ID)
		}
		other := f.artifact(t, p.ID, "app-v2")
		if _, err := r.Artifacts.GetByDigest(ctx, p.ID, other.Kind, other.Digest.String()); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("GetByDigest = %v, want ErrNotFound", err)
		}
	})

	t.Run("an artifact needs a project that exists", func(t *testing.T) {
		f, r, _ := seed(t)
		absent := f.project(t, "gone")
		if err := r.Artifacts.Create(ctx, f.artifact(t, absent.ID, "app-v1")); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create = %v, want ErrNotFound", err)
		}
	})

	t.Run("the same artifact cannot be created twice", func(t *testing.T) {
		f, r, p := seed(t)
		a := f.artifact(t, p.ID, "app-v1")
		if err := r.Artifacts.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := r.Artifacts.Create(ctx, a); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("listing is scoped to the project", func(t *testing.T) {
		f, r, p := seed(t)
		other := f.project(t, "billing")
		if err := r.Projects.Create(ctx, other); err != nil {
			t.Fatalf("Create project: %v", err)
		}
		for _, c := range []string{"app-v1", "app-v2"} {
			if err := r.Artifacts.Create(ctx, f.artifact(t, p.ID, c)); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		if err := r.Artifacts.Create(ctx, f.artifact(t, other.ID, "app-v3")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Artifacts.List(ctx, p.ID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("List returned %d artifacts, want 2", len(got))
		}
	})

	t.Run("missing artifacts are reported as missing", func(t *testing.T) {
		f, r, p := seed(t)
		absent := f.artifact(t, p.ID, "never-registered")
		if _, err := r.Artifacts.Get(ctx, absent.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
	})
}

func runReleases(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	seed := func(t *testing.T) (*fixture, Repositories, project.Project, []artifact.Artifact) {
		t.Helper()
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		if err := r.Projects.Create(ctx, p); err != nil {
			t.Fatalf("Create project: %v", err)
		}
		as := []artifact.Artifact{f.artifact(t, p.ID, "app-v1"), f.artifact(t, p.ID, "migration-v1")}
		for _, a := range as {
			if err := r.Artifacts.Create(ctx, a); err != nil {
				t.Fatalf("Create artifact: %v", err)
			}
		}
		return f, r, p, as
	}

	t.Run("a stored release reads back whole", func(t *testing.T) {
		f, r, p, as := seed(t)
		rel := f.release(t, p.ID, "1.0.0", as)
		if err := r.Releases.Create(ctx, rel); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Releases.Get(ctx, rel.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertRelease(t, got, rel)
	})

	t.Run("the fingerprint survives the round trip", func(t *testing.T) {
		f, r, p, as := seed(t)
		rel := f.release(t, p.ID, "1.0.0", as)
		if err := r.Releases.Create(ctx, rel); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Releases.Get(ctx, rel.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Recomputed from what came back, not read from a column: a stored
		// fingerprint that disagreed with the content would be the bug.
		if got.Fingerprint() != rel.Fingerprint() {
			t.Errorf("fingerprint = %s, want %s", got.Fingerprint(), rel.Fingerprint())
		}
		found, err := r.Releases.FindByFingerprint(ctx, p.ID, rel.Fingerprint().String())
		if err != nil {
			t.Fatalf("FindByFingerprint: %v", err)
		}
		if len(found) != 1 || found[0].ID != rel.ID {
			t.Errorf("FindByFingerprint = %v, want just %s", found, rel.ID)
		}
	})

	t.Run("a version belongs to one release within a project", func(t *testing.T) {
		f, r, p, as := seed(t)
		if err := r.Releases.Create(ctx, f.release(t, p.ID, "1.0.0", as)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		err := r.Releases.Create(ctx, f.release(t, p.ID, "1.0.0", as))
		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("a release may not name an artifact that is not registered", func(t *testing.T) {
		f, r, p, _ := seed(t)
		ghost := f.artifact(t, p.ID, "never-registered")
		err := r.Releases.Create(ctx, f.release(t, p.ID, "1.0.0", []artifact.Artifact{ghost}))
		if !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create = %v, want ErrNotFound", err)
		}
	})

	t.Run("the same release cannot be created twice", func(t *testing.T) {
		f, r, p, as := seed(t)
		rel := f.release(t, p.ID, "1.0.0", as)
		if err := r.Releases.Create(ctx, rel); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := r.Releases.Create(ctx, rel); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("a release needs a project that exists", func(t *testing.T) {
		f, r, _, as := seed(t)
		absent := f.project(t, "gone")
		if err := r.Releases.Create(ctx, f.release(t, absent.ID, "1.0.0", as)); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create = %v, want ErrNotFound", err)
		}
	})

	t.Run("a release is addressable by version", func(t *testing.T) {
		f, r, p, as := seed(t)
		rel := f.release(t, p.ID, "1.0.0", as)
		if err := r.Releases.Create(ctx, rel); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.Releases.GetByVersion(ctx, p.ID, "1.0.0")
		if err != nil {
			t.Fatalf("GetByVersion: %v", err)
		}
		if got.ID != rel.ID {
			t.Errorf("GetByVersion returned %s, want %s", got.ID, rel.ID)
		}
		if _, err := r.Releases.GetByVersion(ctx, p.ID, "2.0.0"); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("GetByVersion = %v, want ErrNotFound", err)
		}
	})

	t.Run("listing is scoped to the project", func(t *testing.T) {
		f, r, p, as := seed(t)
		for _, v := range []string{"1.0.0", "1.1.0"} {
			if err := r.Releases.Create(ctx, f.release(t, p.ID, v, as)); err != nil {
				t.Fatalf("Create %s: %v", v, err)
			}
		}
		got, err := r.Releases.List(ctx, p.ID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("List returned %d releases, want 2", len(got))
		}
	})

	t.Run("missing releases are reported as missing", func(t *testing.T) {
		f, r, p, as := seed(t)
		absent := f.release(t, p.ID, "9.9.9", as)
		if _, err := r.Releases.Get(ctx, absent.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
		none, err := r.Releases.FindByFingerprint(ctx, p.ID, absent.Fingerprint().String())
		if err != nil {
			t.Fatalf("FindByFingerprint: %v", err)
		}
		if len(none) != 0 {
			t.Errorf("FindByFingerprint = %v, want none", none)
		}
	})
}

func runTransactions(t *testing.T, newRepos Factory) {
	ctx := context.Background()
	sentinel := errors.New("rolled back on purpose")

	t.Run("a committed unit is visible", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		e := f.environment(t, p.ID, "production")
		err := r.Tx.WithinTransaction(ctx, func(ctx context.Context) error {
			if err := r.Projects.Create(ctx, p); err != nil {
				return err
			}
			return r.Environments.Create(ctx, e)
		})
		if err != nil {
			t.Fatalf("WithinTransaction: %v", err)
		}
		if _, err := r.Projects.Get(ctx, p.ID); err != nil {
			t.Errorf("project did not survive the commit: %v", err)
		}
		if _, err := r.Environments.Get(ctx, e.ID); err != nil {
			t.Errorf("environment did not survive the commit: %v", err)
		}
	})

	t.Run("a failed unit leaves nothing behind", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		err := r.Tx.WithinTransaction(ctx, func(ctx context.Context) error {
			if err := r.Projects.Create(ctx, p); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithinTransaction = %v, want the callback's error", err)
		}
		if _, err := r.Projects.Get(ctx, p.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound — the write was rolled back", err)
		}
	})

	t.Run("a write inside the unit is visible to a read inside it", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		err := r.Tx.WithinTransaction(ctx, func(ctx context.Context) error {
			if err := r.Projects.Create(ctx, p); err != nil {
				return err
			}
			_, err := r.Projects.Get(ctx, p.ID)
			return err
		})
		if err != nil {
			t.Errorf("a transaction could not read its own write: %v", err)
		}
	})

	t.Run("nesting reuses the open transaction", func(t *testing.T) {
		f, r := newFixture(), newRepos(t)
		p := f.project(t, "checkout")
		err := r.Tx.WithinTransaction(ctx, func(ctx context.Context) error {
			return r.Tx.WithinTransaction(ctx, func(ctx context.Context) error {
				if err := r.Projects.Create(ctx, p); err != nil {
					return err
				}
				return sentinel
			})
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithinTransaction = %v, want the callback's error", err)
		}
		// The inner failure rolls the whole unit back rather than a savepoint:
		// a nested call is not a partial rollback point (ADR-0003).
		if _, err := r.Projects.Get(ctx, p.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
	})
}
