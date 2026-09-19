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
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
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
	Plans        port.PlanRepository
	Deployments  port.DeploymentRepository
	Approvals    port.ApprovalRepository
	Events       port.EventLog
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
		"plans":        runPlans,
		"deployments":  runDeployments,
		"approvals":    runApprovals,
		"events":       runEvents,
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

// plan builds a plan that deploys next into e and would roll back to prev.
//
// It deliberately carries the three things a store is most likely to lose: a
// sensitive change, whose value must come back blank while its path survives; a
// dependency between operations, which a structural encoder drops without
// changing the shape of the record; and a rollback, which is optional and so is
// the field easiest to leave unread.
func (f *fixture) plan(t *testing.T, e environment.Environment, prev, next release.Release) plan.DeploymentPlan {
	t.Helper()
	return f.planWith(t, e, prev, next, deployment.StrategyCanary)
}

// planWith is plan with the rollout named, for the cases that have to vary it.
func (f *fixture) planWith(
	t *testing.T,
	e environment.Environment,
	prev, next release.Release,
	strategy deployment.Strategy,
) plan.DeploymentPlan {
	t.Helper()
	p, err := plan.New(f.gen, f.clock, identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
	}, time.Hour, plan.DeploymentPlan{
		ProjectID:     e.ProjectID,
		EnvironmentID: e.ID,
		ReleaseID:     next.ID,
		BaseRevision:  e.Revision,
		Strategy:      strategy,
		Operations: []plan.PlannedOperation{
			{
				ID: "op_1", Target: "primary", Kind: plan.OperationApply,
				Summary: "roll out the api",
				Diff: plan.Diff{Changes: []plan.Change{
					{Path: "spec.containers[0].image", From: "app:1", To: "app:2"},
					{Path: "spec.env.DATABASE_URL", From: "old", To: "new", Sensitive: true},
				}},
				Reversible: true,
			},
			{
				ID: "op_2", Target: "secondary", Kind: plan.OperationApply,
				Summary:      "roll out the worker",
				Dependencies: []plan.OperationID{"op_1"},
			},
		},
		Policy: policy.Decision{
			Allowed:      true,
			Requirements: []policy.Requirement{{Type: policy.RequireApproval, Role: "release-manager", Count: 2}},
			Reasons:      []policy.Reason{{Code: "production_gate", Message: "Production requires approval"}},
			Risk: policy.RiskAssessment{
				Level: policy.RiskMedium, Score: 0.48,
				Factors: []policy.RiskFactor{{Code: "production_environment", Message: "Targets production"}},
			},
		},
		Rollback: plan.RollbackPlan{
			FromRelease: next.ID,
			ToRelease:   prev.ID,
			Operations: []plan.PlannedOperation{
				{
					ID: "op_1", Target: "primary", Kind: plan.OperationRollback,
					Summary:    "restore the api",
					Reversible: true,
				},
			},
			Automatic: true,
		},
	})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return p
}

func (f *fixture) deployment(t *testing.T, p plan.DeploymentPlan) deployment.Deployment {
	t.Helper()
	d, err := deployment.New(f.gen, f.clock, identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
	}, deployment.Deployment{
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		PlanID:        p.ID,
		// The strategy comes from the plan rather than from the caller: the
		// rollout is part of what was reviewed, and a deployment that chose its
		// own would be running something other than the approved change.
		Strategy: p.Strategy,
		Trigger:  deployment.Trigger{Type: deployment.TriggerManual, Detail: "ticket OPS-14"},
	})
	if err != nil {
		t.Fatalf("build deployment: %v", err)
	}
	return d
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

// world is everything a plan or a deployment needs to exist against: the
// project, the environment it acts on, and the release it moves away from as
// well as the one it moves to.
type world struct {
	f    *fixture
	r    Repositories
	proj project.Project
	env  environment.Environment
	arts []artifact.Artifact
	prev release.Release
	next release.Release
}

func seedWorld(t *testing.T, newRepos Factory) world {
	t.Helper()
	ctx := context.Background()
	f, r := newFixture(), newRepos(t)

	p := f.project(t, "checkout")
	if err := r.Projects.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	e := f.environment(t, p.ID, "production")
	if err := r.Environments.Create(ctx, e); err != nil {
		t.Fatalf("Create environment: %v", err)
	}
	// Create returns nothing, so the stored revision is read back: a plan's
	// base revision has to be the one the store actually holds, or every
	// applicability check in the suite would be testing the fixture's guess.
	e, err := r.Environments.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get environment: %v", err)
	}

	as := []artifact.Artifact{f.artifact(t, p.ID, "app-v1"), f.artifact(t, p.ID, "migration-v1")}
	for _, a := range as {
		if err := r.Artifacts.Create(ctx, a); err != nil {
			t.Fatalf("Create artifact: %v", err)
		}
	}
	prev, next := f.release(t, p.ID, "1.0.0", as), f.release(t, p.ID, "2.0.0", as)
	for _, rel := range []release.Release{prev, next} {
		if err := r.Releases.Create(ctx, rel); err != nil {
			t.Fatalf("Create release %s: %v", rel.Version, err)
		}
	}
	return world{f: f, r: r, proj: p, env: e, arts: as, prev: prev, next: next}
}

// storedPlan seeds a world and persists one plan in it, which is the starting
// point of nearly every deployment case.
func storedPlan(t *testing.T, newRepos Factory) (world, plan.DeploymentPlan) {
	t.Helper()
	w := seedWorld(t, newRepos)
	p := w.f.plan(t, w.env, w.prev, w.next)
	if err := w.r.Plans.Create(context.Background(), p); err != nil {
		t.Fatalf("Create plan: %v", err)
	}
	return w, p
}

func runPlans(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	t.Run("a stored plan reads back whole", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		got, err := w.r.Plans.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertPlan(t, got, p)
	})

	t.Run("the value of a sensitive change is never stored", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		got, err := w.r.Plans.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}

		// The path has to survive: knowing that a database URL changed is the
		// point of a diff, and only the value is the secret (INV-012).
		var found bool
		for _, o := range got.Operations {
			for _, c := range o.Diff.Changes {
				if !c.Sensitive {
					continue
				}
				found = true
				if c.Path == "" {
					t.Error("a sensitive change came back with no path")
				}
				if c.From != "" || c.To != "" {
					t.Errorf("a sensitive value reached storage: from %q to %q", c.From, c.To)
				}
			}
		}
		if !found {
			t.Fatal("the stored plan has no sensitive change; the case proves nothing")
		}

		// Redaction and the hash have to agree. A plan whose stored form no
		// longer verified would be indistinguishable from a tampered one at
		// exactly the moment an operator needed to tell them apart.
		if err := got.VerifyHash(); err != nil {
			t.Errorf("the redacted plan no longer verifies: %v", err)
		}
	})

	t.Run("the rollout strategy survives the round trip", func(t *testing.T) {
		w := seedWorld(t, newRepos)
		// A plan reviewed as a canary must not read back as a recreate: the
		// operations would be identical and the blast radius would not. Every
		// strategy is stored rather than one, because a store that returned a
		// constant would agree with a fixture that only ever used that one.
		for _, s := range []deployment.Strategy{
			deployment.StrategyRolling, deployment.StrategyCanary,
			deployment.StrategyBlueGreen, deployment.StrategyRecreate,
		} {
			p := w.f.planWith(t, w.env, w.prev, w.next, s)
			if err := w.r.Plans.Create(ctx, p); err != nil {
				t.Fatalf("Create %s: %v", s, err)
			}
			got, err := w.r.Plans.Get(ctx, p.ID)
			if err != nil {
				t.Fatalf("Get %s: %v", s, err)
			}
			if got.Strategy != s {
				t.Errorf("Strategy = %q, want %q", got.Strategy, s)
			}
			if err := got.VerifyHash(); err != nil {
				t.Errorf("the stored %s plan does not verify: %v", s, err)
			}
		}
	})

	t.Run("a stored plan still applies against the world it was planned for", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		got, err := w.r.Plans.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if err := got.CheckApplicable(p.CreatedAt, w.env.Revision); err != nil {
			t.Errorf("CheckApplicable: %v", err)
		}
	})

	t.Run("what a read returns is a copy, not the stored plan", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		got, err := w.r.Plans.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}

		// Snapshotted by value first. An implementation that shares its slices
		// would otherwise have the expectation tampered with alongside the
		// record, and the comparison would pass by aliasing both sides of it.
		wantPath := got.Operations[0].Diff.Changes[0].Path
		wantCount := got.Policy.Requirements[0].Count
		wantFactor := got.Policy.Risk.Factors[0].Code

		// Editing a value that was merely read must not reach storage. A store
		// handing out its own slices would let a caller rewrite an approved plan
		// by accident — and since the hash covers all of this, the next read
		// would report tampering that nobody knowingly did.
		got.Operations[0].Diff.Changes[0].Path = "tampered"
		got.Rollback.Operations[0].Summary = "tampered"
		got.Policy.Requirements[0].Count = 99
		got.Policy.Reasons[0].Code = "tampered"
		got.Policy.Risk.Factors[0].Code = "tampered"

		again, err := w.r.Plans.Get(ctx, p.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if again.Operations[0].Diff.Changes[0].Path != wantPath {
			t.Errorf("operation path = %q, want %q",
				again.Operations[0].Diff.Changes[0].Path, wantPath)
		}
		if again.Policy.Requirements[0].Count != wantCount {
			t.Errorf("approval count = %d, want %d",
				again.Policy.Requirements[0].Count, wantCount)
		}
		if again.Policy.Risk.Factors[0].Code != wantFactor {
			t.Errorf("risk factor = %q, want %q",
				again.Policy.Risk.Factors[0].Code, wantFactor)
		}
		// The hash covers every field edited above, so it is the one check that
		// cannot be satisfied by a store that shared any of them.
		if err := again.VerifyHash(); err != nil {
			t.Errorf("the stored plan no longer verifies: %v", err)
		}
	})

	t.Run("the same plan cannot be created twice", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		if err := w.r.Plans.Create(ctx, p); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("a plan needs the things it names to exist", func(t *testing.T) {
		w := seedWorld(t, newRepos)
		absentEnv := w.f.environment(t, w.proj.ID, "gone")
		absentRel := w.f.release(t, w.proj.ID, "9.9.9", w.arts)

		strayEnv := w.f.plan(t, absentEnv, w.prev, w.next)
		if err := w.r.Plans.Create(ctx, strayEnv); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create with an unknown environment = %v, want ErrNotFound", err)
		}
		strayRel := w.f.plan(t, w.env, w.prev, absentRel)
		if err := w.r.Plans.Create(ctx, strayRel); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create with an unknown release = %v, want ErrNotFound", err)
		}
	})

	t.Run("missing plans are reported as missing", func(t *testing.T) {
		w := seedWorld(t, newRepos)
		absent := w.f.plan(t, w.env, w.prev, w.next)
		if _, err := w.r.Plans.Get(ctx, absent.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
	})
}

func runDeployments(t *testing.T, newRepos Factory) {
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)

	// stored seeds a world, a plan and one deployment created from it.
	stored := func(t *testing.T) (world, deployment.Deployment) {
		t.Helper()
		w, p := storedPlan(t, newRepos)
		d := w.f.deployment(t, p)
		rev, err := w.r.Deployments.Create(ctx, d)
		if err != nil {
			t.Fatalf("Create deployment: %v", err)
		}
		// The caller's copy is the one that goes on to be transitioned, so it
		// takes the revision the store committed at rather than assuming one.
		d.Revision = rev
		return w, d
	}

	t.Run("a stored deployment reads back whole", func(t *testing.T) {
		w, d := stored(t)
		got, err := w.r.Deployments.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertDeployment(t, got, d)

		// A stored deployment has been written once, so it is at revision one —
		// not at zero, which means "never written" and refuses every update.
		if got.Revision != 1 {
			t.Errorf("Revision = %d, want 1", got.Revision)
		}
	})

	t.Run("a deployment that has not started has no start time", func(t *testing.T) {
		w, d := stored(t)
		got, err := w.r.Deployments.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Absent, not zero. An operator reading a timeline has to be able to
		// tell "has not started" from "started at the epoch".
		if got.StartedAt != nil {
			t.Errorf("StartedAt = %s, want absent", got.StartedAt)
		}
		if got.FinishedAt != nil {
			t.Errorf("FinishedAt = %s, want absent", got.FinishedAt)
		}
	})

	t.Run("the start and finish times survive the round trip", func(t *testing.T) {
		w, d := stored(t)
		for _, next := range []deployment.Status{
			deployment.StatusQueued, deployment.StatusApplying,
			deployment.StatusVerifying, deployment.StatusSucceeded,
		} {
			moved, err := d.TransitionTo(next, at)
			if err != nil {
				t.Fatalf("TransitionTo %s: %v", next, err)
			}
			rev, err := w.r.Deployments.Update(ctx, moved)
			if err != nil {
				t.Fatalf("Update to %s: %v", next, err)
			}
			moved.Revision = rev
			d = moved
		}
		got, err := w.r.Deployments.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		assertDeployment(t, got, d)
	})

	t.Run("what a read returns is a copy, not the stored deployment", func(t *testing.T) {
		w, d := stored(t)
		for _, next := range []deployment.Status{
			deployment.StatusQueued, deployment.StatusApplying,
			deployment.StatusVerifying, deployment.StatusSucceeded,
		} {
			moved, err := d.TransitionTo(next, at)
			if err != nil {
				t.Fatalf("TransitionTo %s: %v", next, err)
			}
			rev, err := w.r.Deployments.Update(ctx, moved)
			if err != nil {
				t.Fatalf("Update to %s: %v", next, err)
			}
			moved.Revision = rev
			d = moved
		}

		got, err := w.r.Deployments.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.StartedAt == nil || got.FinishedAt == nil {
			t.Fatalf("a finished deployment came back with started %v, finished %v",
				got.StartedAt, got.FinishedAt)
		}
		// Snapshotted by value, because a store that shared the pointer would
		// otherwise have the expectation moved along with the record.
		wantStart, wantFinish := *got.StartedAt, *got.FinishedAt

		// Writing through a pointer that was merely read must not reach storage.
		// One implementation sharing its pointers and the other copying is the
		// divergence this suite exists to catch: it never shows up in
		// production, only in whichever tests run against the fake.
		*got.StartedAt = at.Add(time.Hour)
		*got.FinishedAt = at.Add(2 * time.Hour)

		again, err := w.r.Deployments.Get(ctx, d.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if again.StartedAt == nil || !again.StartedAt.Equal(wantStart) {
			t.Errorf("StartedAt = %v after a read copy was edited, want %s", again.StartedAt, wantStart)
		}
		if again.FinishedAt == nil || !again.FinishedAt.Equal(wantFinish) {
			t.Errorf("FinishedAt = %v after a read copy was edited, want %s", again.FinishedAt, wantFinish)
		}
	})

	t.Run("an update built on a stale read is refused", func(t *testing.T) {
		w, d := stored(t)
		moved, err := d.TransitionTo(deployment.StatusQueued, at)
		if err != nil {
			t.Fatalf("TransitionTo: %v", err)
		}
		rev, err := w.r.Deployments.Update(ctx, moved)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if rev != 2 {
			t.Errorf("Update committed at revision %d, want 2", rev)
		}
		// moved still carries revision one, which is the read the second writer
		// would be working from.
		if _, err := w.r.Deployments.Update(ctx, moved); !errors.Is(err, port.ErrRevisionConflict) {
			t.Errorf("Update = %v, want ErrRevisionConflict", err)
		}
	})

	t.Run("listing an environment gives the most recent first", func(t *testing.T) {
		w, first := stored(t)

		// The fixture clock does not move, so second shares first's instant and
		// only the identifier separates them. Two deployments started within one
		// clock tick still have to have an order, or "what is deployed now"
		// would be answered differently on each read.
		p, err := w.r.Plans.Get(ctx, first.PlanID)
		if err != nil {
			t.Fatalf("Get plan: %v", err)
		}
		second := w.f.deployment(t, p)
		third := w.f.deployment(t, p)
		third.CreatedAt = third.CreatedAt.Add(time.Minute)

		for _, d := range []deployment.Deployment{second, third} {
			if _, err := w.r.Deployments.Create(ctx, d); err != nil {
				t.Fatalf("Create deployment: %v", err)
			}
		}

		got, err := w.r.Deployments.ListForEnvironment(ctx, w.env.ID)
		if err != nil {
			t.Fatalf("ListForEnvironment: %v", err)
		}
		want := []identity.DeploymentID{third.ID, second.ID, first.ID}
		if len(got) != len(want) {
			t.Fatalf("got %d deployments, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i] {
				t.Errorf("position %d = %s, want %s", i, got[i].ID, want[i])
			}
		}
	})

	t.Run("listing is scoped to the environment", func(t *testing.T) {
		w, _ := stored(t)
		other := w.f.environment(t, w.proj.ID, "staging")
		if err := w.r.Environments.Create(ctx, other); err != nil {
			t.Fatalf("Create environment: %v", err)
		}
		got, err := w.r.Deployments.ListForEnvironment(ctx, other.ID)
		if err != nil {
			t.Fatalf("ListForEnvironment: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d deployments in an untouched environment, want none", len(got))
		}
	})

	t.Run("the active deployment is the one that has not finished", func(t *testing.T) {
		w, d := stored(t)
		got, err := w.r.Deployments.FindActive(ctx, w.env.ID)
		if err != nil {
			t.Fatalf("FindActive: %v", err)
		}
		if got.ID != d.ID {
			t.Errorf("FindActive returned %s, want %s", got.ID, d.ID)
		}

		done, err := d.TransitionTo(deployment.StatusCancelled, at)
		if err != nil {
			t.Fatalf("TransitionTo: %v", err)
		}
		if _, err := w.r.Deployments.Update(ctx, done); err != nil {
			t.Fatalf("Update: %v", err)
		}
		// An idle environment is an answer, not a failure: the caller branches
		// on it to decide whether a new deployment may start.
		if _, err := w.r.Deployments.FindActive(ctx, w.env.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("FindActive = %v, want ErrNotFound", err)
		}
	})

	t.Run("a deployment may name the one it replaces", func(t *testing.T) {
		w, first := stored(t)
		p := w.f.plan(t, w.env, w.next, w.prev)
		if err := w.r.Plans.Create(ctx, p); err != nil {
			t.Fatalf("Create plan: %v", err)
		}
		second := w.f.deployment(t, p)
		// A copy, not the address of first.ID: a store that shared the pointer
		// would otherwise edit the expectation along with the record.
		prev := first.ID
		second.Previous = &prev
		if _, err := w.r.Deployments.Create(ctx, second); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := w.r.Deployments.Get(ctx, second.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Previous == nil || *got.Previous != first.ID {
			t.Fatalf("Previous = %v, want %s", got.Previous, first.ID)
		}

		// The predecessor is what makes a rollback target derivable, so writing
		// through a copy of it must not reach the stored record either.
		*got.Previous = "tampered"
		again, err := w.r.Deployments.Get(ctx, second.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if again.Previous == nil || *again.Previous != first.ID {
			t.Errorf("Previous = %v after a read copy was edited, want %s", again.Previous, first.ID)
		}
	})

	t.Run("a deployment needs the things it names to exist", func(t *testing.T) {
		w, d := stored(t)

		unplanned := w.f.deployment(t, w.f.plan(t, w.env, w.prev, w.next))
		if _, err := w.r.Deployments.Create(ctx, unplanned); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create with an unknown plan = %v, want ErrNotFound", err)
		}

		// A dangling previous would make the rollback target unresolvable at
		// exactly the moment it is needed.
		p := w.f.plan(t, w.env, w.next, w.prev)
		if err := w.r.Plans.Create(ctx, p); err != nil {
			t.Fatalf("Create plan: %v", err)
		}
		ghost := w.f.deployment(t, p)
		gone := d.ID + "-gone"
		ghost.Previous = &gone
		if _, err := w.r.Deployments.Create(ctx, ghost); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Create with an unknown predecessor = %v, want ErrNotFound", err)
		}
	})

	t.Run("the same deployment cannot be created twice", func(t *testing.T) {
		w, d := stored(t)
		if _, err := w.r.Deployments.Create(ctx, d); !errors.Is(err, port.ErrAlreadyExists) {
			t.Errorf("Create = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("missing deployments are reported as missing", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		absent := w.f.deployment(t, p)
		if _, err := w.r.Deployments.Get(ctx, absent.ID); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
		absent.Revision = 1
		if _, err := w.r.Deployments.Update(ctx, absent); !errors.Is(err, port.ErrNotFound) {
			t.Errorf("Update = %v, want ErrNotFound", err)
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

// approval builds one principal's answer about a subject. Claims carry both a
// role, which a requirement matches on and which must survive, and a token,
// which must not (INV-012).
func (f *fixture) approval(
	t *testing.T, subject policy.SubjectRef, by string, d policy.ApprovalDecision,
) policy.Approval {
	t.Helper()
	id, err := identity.NewApprovalID(f.gen)
	if err != nil {
		t.Fatalf("build approval id: %v", err)
	}
	reason := ""
	if d == policy.ApprovalDenied {
		reason = "the migration has not been rehearsed"
	}
	return policy.Approval{
		ID:      id,
		Subject: subject,
		Principal: identity.Principal{
			ID: by, Type: identity.PrincipalHuman, DisplayName: by,
			Claims: map[string]string{"role": "release-manager", "token": "s3cr3t"},
		},
		Decision:  d,
		Reason:    reason,
		CreatedAt: f.clock.Now(),
	}
}

// subjectOf names a plan the way the deploy service does, so that the suite
// exercises the reference shape the application actually writes.
func subjectOf(p plan.DeploymentPlan) policy.SubjectRef {
	return policy.SubjectRef{Kind: "plan", ID: string(p.ID), Revision: p.Hash.String()}
}

func runApprovals(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	t.Run("a stored approval reads back whole", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		a := w.f.approval(t, subjectOf(p), "ana", policy.ApprovalGranted)
		a.ExpiresAt = a.CreatedAt.Add(24 * time.Hour)
		if err := w.r.Approvals.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.Approvals.ListForSubject(ctx, a.Subject.Kind, a.Subject.ID)
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("read back %d approvals, want 1", len(got))
		}
		assertApproval(t, got[0], a)
	})

	// The revision is the load-bearing field: an approval that came back
	// without it would discharge nothing, and one that came back with the
	// wrong one would discharge a plan its approver never read (§13).
	t.Run("the revision an approval is bound to survives", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		a := w.f.approval(t, subjectOf(p), "ana", policy.ApprovalGranted)
		if err := w.r.Approvals.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.Approvals.ListForSubject(ctx, "plan", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if err := p.Policy.SatisfiedBy(subjectOf(p), got, w.f.clock.Now()); err == nil {
			t.Fatal("one approval satisfied a two-approval requirement")
		}

		second := w.f.approval(t, subjectOf(p), "ben", policy.ApprovalGranted)
		if err := w.r.Approvals.Create(ctx, second); err != nil {
			t.Fatalf("Create second: %v", err)
		}
		got, err = w.r.Approvals.ListForSubject(ctx, "plan", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if err := p.Policy.SatisfiedBy(subjectOf(p), got, w.f.clock.Now()); err != nil {
			t.Fatalf("two stored approvals did not satisfy the plan's own policy: %v", err)
		}
	})

	t.Run("a denial keeps the reason it gave", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		a := w.f.approval(t, subjectOf(p), "ana", policy.ApprovalDenied)
		if err := w.r.Approvals.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.Approvals.ListForSubject(ctx, "plan", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if len(got) != 1 || got[0].Reason != a.Reason {
			t.Fatalf("stored reason = %q, want %q", got[0].Reason, a.Reason)
		}
		// Whoever is stopped has to be told what stopped them, so a stored
		// denial that lost its reason is one that no longer validates.
		if err := got[0].Validate(); err != nil {
			t.Errorf("the stored denial is no longer a complete record: %v", err)
		}
	})

	t.Run("approvals come back in the order they were given", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		want := []string{"ana", "ben", "cai"}
		for _, by := range want {
			if err := w.r.Approvals.Create(ctx, w.f.approval(t, subjectOf(p), by, policy.ApprovalGranted)); err != nil {
				t.Fatalf("Create %s: %v", by, err)
			}
		}

		got, err := w.r.Approvals.ListForSubject(ctx, "plan", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("read back %d approvals, want %d", len(got), len(want))
		}
		for i, by := range want {
			if got[i].Principal.ID != by {
				t.Errorf("approval %d is %q, want %q — who answered first is part of the record",
					i, got[i].Principal.ID, by)
			}
		}
	})

	t.Run("approvals of another subject are not returned", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		other := w.f.plan(t, w.env, w.prev, w.next)
		if err := w.r.Plans.Create(ctx, other); err != nil {
			t.Fatalf("Create other plan: %v", err)
		}
		if err := w.r.Approvals.Create(ctx, w.f.approval(t, subjectOf(p), "ana", policy.ApprovalGranted)); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.Approvals.ListForSubject(ctx, "plan", string(other.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("another plan's approvals leaked in: %+v", got)
		}

		// A different kind with the same id is a different thing entirely.
		got, err = w.r.Approvals.ListForSubject(ctx, "release", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("a release's approvals matched a plan id: %+v", got)
		}
	})

	t.Run("a subject nobody has answered has no approvals, not an error", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		got, err := w.r.Approvals.ListForSubject(ctx, "plan", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("an unanswered plan has %d approvals", len(got))
		}
	})

	t.Run("the same approval twice is refused", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		a := w.f.approval(t, subjectOf(p), "ana", policy.ApprovalGranted)
		if err := w.r.Approvals.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := w.r.Approvals.Create(ctx, a); !errors.Is(err, port.ErrAlreadyExists) {
			t.Fatalf("err = %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("an incomplete approval is refused", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		a := w.f.approval(t, subjectOf(p), "ana", policy.ApprovalGranted)
		a.Subject.Revision = ""
		if err := w.r.Approvals.Create(ctx, a); err == nil {
			t.Fatal("an approval bound to no revision was stored")
		}
	})

	// INV-012. An approval carries the approver's principal, and an identity
	// provider's claims are where a bearer token arrives.
	t.Run("a secret in the approver's claims is never stored", func(t *testing.T) {
		w, p := storedPlan(t, newRepos)
		a := w.f.approval(t, subjectOf(p), "ana", policy.ApprovalGranted)
		if err := w.r.Approvals.Create(ctx, a); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := w.r.Approvals.ListForSubject(ctx, "plan", string(p.ID))
		if err != nil {
			t.Fatalf("ListForSubject: %v", err)
		}
		if got[0].Principal.Claims["token"] == "s3cr3t" {
			t.Error("the approver's token reached storage")
		}
		// The role has to survive: it is what a role requirement matches on,
		// and redacting it would silently stop approvals from counting.
		if got[0].Principal.Claims["role"] != "release-manager" {
			t.Errorf("role claim = %q, want release-manager", got[0].Principal.Claims["role"])
		}
	})
}

func assertApproval(t *testing.T, got, want policy.Approval) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Subject != want.Subject {
		t.Errorf("Subject = %v, want %v", got.Subject, want.Subject)
	}
	if got.Principal.ID != want.Principal.ID || got.Principal.Type != want.Principal.Type {
		t.Errorf("Principal = %+v, want %+v", got.Principal, want.Principal)
	}
	if got.Decision != want.Decision {
		t.Errorf("Decision = %q, want %q", got.Decision, want.Decision)
	}
	if got.Reason != want.Reason {
		t.Errorf("Reason = %q, want %q", got.Reason, want.Reason)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %s, want %s", got.CreatedAt, want.CreatedAt)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %s, want %s — an approval that lost its expiry stands forever",
			got.ExpiresAt, want.ExpiresAt)
	}
}
