package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	"go.klarlabs.de/rollops/internal/domain/release"
)

// Projects returns the project repository over this store.
func (s *Store) Projects() port.ProjectRepository { return projectRepo{s} }

// Environments returns the environment repository over this store.
func (s *Store) Environments() port.EnvironmentRepository { return environmentRepo{s} }

// Artifacts returns the artifact repository over this store.
func (s *Store) Artifacts() port.ArtifactRepository { return artifactRepo{s} }

// Releases returns the release repository over this store.
func (s *Store) Releases() port.ReleaseRepository { return releaseRepo{s} }

// Plans returns the deployment plan repository over this store.
func (s *Store) Plans() port.PlanRepository { return planRepo{s} }

// Deployments returns the deployment repository over this store.
func (s *Store) Deployments() port.DeploymentRepository { return deploymentRepo{s} }

// Approvals returns the approval repository over this store.
func (s *Store) Approvals() port.ApprovalRepository { return approvalRepo{s} }

// Uniqueness and existence are checked with a query rather than by reading the
// driver's constraint errors. The constraints stay in the schema as the
// integrity backstop, but they cannot say which rule was broken in a form the
// application can branch on, and parsing their messages would tie the ports'
// error vocabulary to one driver's wording.

type projectRepo struct{ s *Store }

func (r projectRepo) Create(ctx context.Context, p project.Project) error {
	return r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM projects WHERE id = ?`, p.ID,
			fmt.Sprintf("project %s", p.ID),
		); err != nil {
			return err
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM projects WHERE name = ?`, p.Name,
			fmt.Sprintf("project %q", p.Name),
		); err != nil {
			return err
		}
		labels, err := encodeStringMap(p.Labels)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx,
			`INSERT INTO projects (id, name, description, labels, created_at, updated_at, revision)
			 VALUES (?, ?, ?, ?, ?, ?, 1)`,
			p.ID, p.Name, p.Description, labels, encodeTime(p.CreatedAt), encodeTime(p.UpdatedAt),
		)
		return wrap("insert project", err)
	})
}

func (r projectRepo) Update(ctx context.Context, p project.Project) (identity.Revision, error) {
	var committed identity.Revision
	err := r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		stored, err := scanProject(q.QueryRowContext(ctx, projectColumns+` WHERE id = ?`, p.ID))
		if err != nil {
			return notFoundAs(err, fmt.Sprintf("project %s", p.ID))
		}
		if !stored.Revision.Matches(p.Revision) {
			return fmt.Errorf("project %s: %w", p.ID, port.ErrRevisionConflict)
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM projects WHERE name = ? AND id <> ?`, []any{p.Name, p.ID},
			fmt.Sprintf("project %q", p.Name),
		); err != nil {
			return err
		}
		labels, err := encodeStringMap(p.Labels)
		if err != nil {
			return err
		}
		committed = stored.Revision.Next()
		_, err = q.ExecContext(ctx,
			`UPDATE projects SET name = ?, description = ?, labels = ?, updated_at = ?, revision = ?
			 WHERE id = ? AND revision = ?`,
			p.Name, p.Description, labels, encodeTime(p.UpdatedAt), committed, p.ID, stored.Revision,
		)
		return wrap("update project", err)
	})
	if err != nil {
		return 0, err
	}
	return committed, nil
}

func (r projectRepo) Get(ctx context.Context, id identity.ProjectID) (project.Project, error) {
	p, err := scanProject(r.s.conn(ctx).QueryRowContext(ctx, projectColumns+` WHERE id = ?`, id))
	if err != nil {
		return project.Project{}, notFoundAs(err, fmt.Sprintf("project %s", id))
	}
	return p, nil
}

func (r projectRepo) GetByName(ctx context.Context, name string) (project.Project, error) {
	p, err := scanProject(r.s.conn(ctx).QueryRowContext(ctx, projectColumns+` WHERE name = ?`, name))
	if err != nil {
		return project.Project{}, notFoundAs(err, fmt.Sprintf("project %q", name))
	}
	return p, nil
}

func (r projectRepo) List(ctx context.Context) ([]project.Project, error) {
	rows, err := r.s.conn(ctx).QueryContext(ctx, projectColumns+` ORDER BY id`)
	if err != nil {
		return nil, wrap("list projects", err)
	}
	return collect(rows, scanProject)
}

const projectColumns = `SELECT id, name, description, labels, created_at, updated_at, revision FROM projects`

func scanProject(sc scanner) (project.Project, error) {
	var (
		p                    project.Project
		labels               string
		createdAt, updatedAt string
	)
	if err := sc.Scan(&p.ID, &p.Name, &p.Description, &labels, &createdAt, &updatedAt, &p.Revision); err != nil {
		return project.Project{}, err
	}
	var err error
	if p.Labels, err = decodeStringMap(labels); err != nil {
		return project.Project{}, err
	}
	if p.CreatedAt, err = decodeTime(createdAt); err != nil {
		return project.Project{}, err
	}
	if p.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return project.Project{}, err
	}
	return p, nil
}

type environmentRepo struct{ s *Store }

func (r environmentRepo) Create(ctx context.Context, e environment.Environment) error {
	return r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		if err := mustExist(ctx, q,
			`SELECT 1 FROM projects WHERE id = ?`, e.ProjectID,
			fmt.Sprintf("project %s", e.ProjectID),
		); err != nil {
			return err
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM environments WHERE id = ?`, e.ID,
			fmt.Sprintf("environment %s", e.ID),
		); err != nil {
			return err
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM environments WHERE project_id = ? AND name = ?`, []any{e.ProjectID, e.Name},
			fmt.Sprintf("environment %q", e.Name),
		); err != nil {
			return err
		}
		policies, variables, labels, err := encodeEnvironment(e)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO environments
			   (id, project_id, name, kind, policies, variables, labels, ttl_seconds, delete_on_close, revision)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
			e.ID, e.ProjectID, e.Name, string(e.Kind), policies, variables, labels,
			int64(e.Lifecycle.TTL.Seconds()), e.Lifecycle.DeleteOnClose,
		); err != nil {
			return wrap("insert environment", err)
		}
		return insertTargets(ctx, q, e)
	})
}

func (r environmentRepo) Update(ctx context.Context, e environment.Environment) (identity.Revision, error) {
	var committed identity.Revision
	err := r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		stored, err := scanEnvironment(q.QueryRowContext(ctx, environmentColumns+` WHERE id = ?`, e.ID))
		if err != nil {
			return notFoundAs(err, fmt.Sprintf("environment %s", e.ID))
		}
		if !stored.Revision.Matches(e.Revision) {
			return fmt.Errorf("environment %s: %w", e.ID, port.ErrRevisionConflict)
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM environments WHERE project_id = ? AND name = ? AND id <> ?`,
			[]any{stored.ProjectID, e.Name, e.ID},
			fmt.Sprintf("environment %q", e.Name),
		); err != nil {
			return err
		}
		policies, variables, labels, err := encodeEnvironment(e)
		if err != nil {
			return err
		}
		committed = stored.Revision.Next()
		if _, err := q.ExecContext(ctx,
			`UPDATE environments
			    SET name = ?, kind = ?, policies = ?, variables = ?, labels = ?,
			        ttl_seconds = ?, delete_on_close = ?, revision = ?
			  WHERE id = ? AND revision = ?`,
			e.Name, string(e.Kind), policies, variables, labels,
			int64(e.Lifecycle.TTL.Seconds()), e.Lifecycle.DeleteOnClose, committed,
			e.ID, stored.Revision,
		); err != nil {
			return wrap("update environment", err)
		}
		// Bindings are replaced rather than merged: the environment handed in
		// is the whole intent, so a target it omits is one the caller removed.
		if _, err := q.ExecContext(ctx,
			`DELETE FROM target_bindings WHERE environment_id = ?`, e.ID,
		); err != nil {
			return wrap("clear target bindings", err)
		}
		return insertTargets(ctx, q, e)
	})
	if err != nil {
		return 0, err
	}
	return committed, nil
}

func (r environmentRepo) Get(ctx context.Context, id identity.EnvironmentID) (environment.Environment, error) {
	q := r.s.conn(ctx)
	e, err := scanEnvironment(q.QueryRowContext(ctx, environmentColumns+` WHERE id = ?`, id))
	if err != nil {
		return environment.Environment{}, notFoundAs(err, fmt.Sprintf("environment %s", id))
	}
	return withTargets(ctx, q, e)
}

func (r environmentRepo) GetByName(ctx context.Context, p identity.ProjectID, name string) (environment.Environment, error) {
	q := r.s.conn(ctx)
	e, err := scanEnvironment(q.QueryRowContext(ctx,
		environmentColumns+` WHERE project_id = ? AND name = ?`, p, name))
	if err != nil {
		return environment.Environment{}, notFoundAs(err, fmt.Sprintf("environment %q", name))
	}
	return withTargets(ctx, q, e)
}

func (r environmentRepo) List(ctx context.Context, p identity.ProjectID) ([]environment.Environment, error) {
	q := r.s.conn(ctx)
	rows, err := q.QueryContext(ctx, environmentColumns+` WHERE project_id = ? ORDER BY id`, p)
	if err != nil {
		return nil, wrap("list environments", err)
	}
	es, err := collect(rows, scanEnvironment)
	if err != nil {
		return nil, err
	}
	for i, e := range es {
		if es[i], err = withTargets(ctx, q, e); err != nil {
			return nil, err
		}
	}
	return es, nil
}

const environmentColumns = `SELECT id, project_id, name, kind, policies, variables, labels,
	ttl_seconds, delete_on_close, revision FROM environments`

func scanEnvironment(sc scanner) (environment.Environment, error) {
	var (
		e                           environment.Environment
		kind                        string
		policies, variables, labels string
		ttlSeconds                  int64
	)
	if err := sc.Scan(&e.ID, &e.ProjectID, &e.Name, &kind, &policies, &variables, &labels,
		&ttlSeconds, &e.Lifecycle.DeleteOnClose, &e.Revision); err != nil {
		return environment.Environment{}, err
	}
	e.Kind = environment.Kind(kind)
	e.Lifecycle.TTL = time.Duration(ttlSeconds) * time.Second

	var err error
	if e.Policies, err = decodePolicyBindings(policies); err != nil {
		return environment.Environment{}, err
	}
	if e.Variables, err = decodeRefMap(variables); err != nil {
		return environment.Environment{}, err
	}
	if e.Labels, err = decodeStringMap(labels); err != nil {
		return environment.Environment{}, err
	}
	return e, nil
}

func encodeEnvironment(e environment.Environment) (policies, variables, labels string, err error) {
	if policies, err = encodePolicyBindings(e.Policies); err != nil {
		return "", "", "", err
	}
	if variables, err = encodeRefMap(e.Variables); err != nil {
		return "", "", "", err
	}
	if labels, err = encodeStringMap(e.Labels); err != nil {
		return "", "", "", err
	}
	return policies, variables, labels, nil
}

func insertTargets(ctx context.Context, q querier, e environment.Environment) error {
	for i, t := range e.Targets {
		config, err := encodeRefMap(t.Config)
		if err != nil {
			return err
		}
		labels, err := encodeStringMap(t.Labels)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO target_bindings (environment_id, name, driver, config, labels, position)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			e.ID, t.Name, t.Driver, config, labels, i,
		); err != nil {
			return wrap("insert target binding", err)
		}
	}
	return nil
}

// withTargets loads the bindings in the order they were declared in, which is
// what position is for: reading an environment back yields the list it was
// written with rather than one sorted by name.
func withTargets(ctx context.Context, q querier, e environment.Environment) (environment.Environment, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name, driver, config, labels FROM target_bindings
		 WHERE environment_id = ? ORDER BY position`, e.ID)
	if err != nil {
		return environment.Environment{}, wrap("load target bindings", err)
	}
	e.Targets, err = collect(rows, func(sc scanner) (environment.TargetBinding, error) {
		var (
			t              environment.TargetBinding
			config, labels string
		)
		if err := sc.Scan(&t.Name, &t.Driver, &config, &labels); err != nil {
			return environment.TargetBinding{}, err
		}
		if t.Config, err = decodeRefMap(config); err != nil {
			return environment.TargetBinding{}, err
		}
		if t.Labels, err = decodeStringMap(labels); err != nil {
			return environment.TargetBinding{}, err
		}
		return t, nil
	})
	if err != nil {
		return environment.Environment{}, err
	}
	return e, nil
}

type artifactRepo struct{ s *Store }

func (r artifactRepo) Create(ctx context.Context, a artifact.Artifact) error {
	return r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		if err := mustExist(ctx, q,
			`SELECT 1 FROM projects WHERE id = ?`, a.ProjectID,
			fmt.Sprintf("project %s", a.ProjectID),
		); err != nil {
			return err
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM artifacts WHERE id = ?`, a.ID,
			fmt.Sprintf("artifact %s", a.ID),
		); err != nil {
			return err
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM artifacts WHERE project_id = ? AND kind = ? AND digest = ?`,
			[]any{a.ProjectID, string(a.Kind), a.Digest.String()},
			fmt.Sprintf("artifact %s", a.Digest),
		); err != nil {
			return err
		}
		metadata, err := encodeStringMap(a.Metadata)
		if err != nil {
			return err
		}
		prov, err := encodeDocumentRef(a.Provenance)
		if err != nil {
			return err
		}
		sboms, err := encodeDocumentRefs(a.SBOMs)
		if err != nil {
			return err
		}
		signatures, err := encodeDocumentRefs(a.Signatures)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx,
			`INSERT INTO artifacts
			   (id, project_id, kind, digest, locator, size, media_type,
			    metadata, provenance, sboms, signatures, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.ProjectID, string(a.Kind), a.Digest.String(), a.Locator, a.Size, a.MediaType,
			metadata, prov, sboms, signatures, encodeTime(a.CreatedAt),
		)
		return wrap("insert artifact", err)
	})
}

func (r artifactRepo) Get(ctx context.Context, id identity.ArtifactID) (artifact.Artifact, error) {
	a, err := scanArtifact(r.s.conn(ctx).QueryRowContext(ctx, artifactColumns+` WHERE id = ?`, id))
	if err != nil {
		return artifact.Artifact{}, notFoundAs(err, fmt.Sprintf("artifact %s", id))
	}
	return a, nil
}

func (r artifactRepo) GetByDigest(ctx context.Context, p identity.ProjectID, k artifact.Kind, d string) (artifact.Artifact, error) {
	a, err := scanArtifact(r.s.conn(ctx).QueryRowContext(ctx,
		artifactColumns+` WHERE project_id = ? AND kind = ? AND digest = ?`, p, string(k), d))
	if err != nil {
		return artifact.Artifact{}, notFoundAs(err, fmt.Sprintf("artifact %s", d))
	}
	return a, nil
}

func (r artifactRepo) List(ctx context.Context, p identity.ProjectID) ([]artifact.Artifact, error) {
	rows, err := r.s.conn(ctx).QueryContext(ctx,
		artifactColumns+` WHERE project_id = ? ORDER BY id`, p)
	if err != nil {
		return nil, wrap("list artifacts", err)
	}
	return collect(rows, scanArtifact)
}

const artifactColumns = `SELECT id, project_id, kind, digest, locator, size, media_type,
	metadata, provenance, sboms, signatures, created_at FROM artifacts`

func scanArtifact(sc scanner) (artifact.Artifact, error) {
	var (
		a                                 artifact.Artifact
		kind, dig, createdAt              string
		metadata, prov, sboms, signatures string
	)
	if err := sc.Scan(&a.ID, &a.ProjectID, &kind, &dig, &a.Locator, &a.Size, &a.MediaType,
		&metadata, &prov, &sboms, &signatures, &createdAt); err != nil {
		return artifact.Artifact{}, err
	}
	a.Kind = artifact.Kind(kind)

	var err error
	if a.Digest, err = digest.Parse(dig); err != nil {
		return artifact.Artifact{}, fmt.Errorf("sqlite: artifact %s: %w", a.ID, err)
	}
	if a.Metadata, err = decodeStringMap(metadata); err != nil {
		return artifact.Artifact{}, err
	}
	if a.Provenance, err = decodeDocumentRef(prov); err != nil {
		return artifact.Artifact{}, err
	}
	if a.SBOMs, err = decodeDocumentRefs(sboms); err != nil {
		return artifact.Artifact{}, err
	}
	if a.Signatures, err = decodeDocumentRefs(signatures); err != nil {
		return artifact.Artifact{}, err
	}
	if a.CreatedAt, err = decodeTime(createdAt); err != nil {
		return artifact.Artifact{}, err
	}
	return a, nil
}

type releaseRepo struct{ s *Store }

func (r releaseRepo) Create(ctx context.Context, rel release.Release) error {
	return r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		if err := mustExist(ctx, q,
			`SELECT 1 FROM projects WHERE id = ?`, rel.ProjectID,
			fmt.Sprintf("project %s", rel.ProjectID),
		); err != nil {
			return err
		}
		for _, a := range rel.Artifacts {
			if err := mustExist(ctx, q,
				`SELECT 1 FROM artifacts WHERE id = ?`, a.ArtifactID,
				fmt.Sprintf("artifact %s", a.ArtifactID),
			); err != nil {
				return err
			}
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM releases WHERE id = ?`, rel.ID,
			fmt.Sprintf("release %s", rel.ID),
		); err != nil {
			return err
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM releases WHERE project_id = ? AND version = ?`,
			[]any{rel.ProjectID, rel.Version},
			fmt.Sprintf("release %q", rel.Version),
		); err != nil {
			return err
		}
		source, err := encodeSourceRevision(rel.Source)
		if err != nil {
			return err
		}
		prov, err := encodeDocumentRef(rel.Provenance)
		if err != nil {
			return err
		}
		createdBy, err := encodePrincipal(rel.CreatedBy)
		if err != nil {
			return err
		}
		labels, err := encodeStringMap(rel.Labels)
		if err != nil {
			return err
		}
		annotations, err := encodeStringMap(rel.Annotations)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			`INSERT INTO releases
			   (id, project_id, version, fingerprint, source, provenance,
			    created_by, created_at, labels, annotations)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rel.ID, rel.ProjectID, rel.Version, rel.Fingerprint().String(), source, prov,
			createdBy, encodeTime(rel.CreatedAt), labels, annotations,
		); err != nil {
			return wrap("insert release", err)
		}
		for _, a := range rel.Artifacts {
			if _, err := q.ExecContext(ctx,
				`INSERT INTO release_artifacts (release_id, role, artifact_id) VALUES (?, ?, ?)`,
				rel.ID, a.Role, a.ArtifactID,
			); err != nil {
				return wrap("insert release artifact", err)
			}
		}
		return nil
	})
}

func (r releaseRepo) Get(ctx context.Context, id identity.ReleaseID) (release.Release, error) {
	q := r.s.conn(ctx)
	rel, err := scanRelease(q.QueryRowContext(ctx, releaseColumns+` WHERE id = ?`, id))
	if err != nil {
		return release.Release{}, notFoundAs(err, fmt.Sprintf("release %s", id))
	}
	return withReleaseArtifacts(ctx, q, rel)
}

func (r releaseRepo) GetByVersion(ctx context.Context, p identity.ProjectID, version string) (release.Release, error) {
	q := r.s.conn(ctx)
	rel, err := scanRelease(q.QueryRowContext(ctx,
		releaseColumns+` WHERE project_id = ? AND version = ?`, p, version))
	if err != nil {
		return release.Release{}, notFoundAs(err, fmt.Sprintf("release %q", version))
	}
	return withReleaseArtifacts(ctx, q, rel)
}

func (r releaseRepo) List(ctx context.Context, p identity.ProjectID) ([]release.Release, error) {
	return r.listWhere(ctx, ` WHERE project_id = ? ORDER BY id`, p)
}

// FindByFingerprint narrows with the indexed column and then recomputes, so the
// index is a way of finding candidates rather than the answer. A row whose
// recorded fingerprint disagrees with its content is not returned.
func (r releaseRepo) FindByFingerprint(ctx context.Context, p identity.ProjectID, f string) ([]release.Release, error) {
	candidates, err := r.listWhere(ctx, ` WHERE project_id = ? AND fingerprint = ? ORDER BY id`, p, f)
	if err != nil {
		return nil, err
	}
	var out []release.Release
	for _, rel := range candidates {
		if rel.Fingerprint().String() == f {
			out = append(out, rel)
		}
	}
	return out, nil
}

func (r releaseRepo) listWhere(ctx context.Context, where string, args ...any) ([]release.Release, error) {
	q := r.s.conn(ctx)
	rows, err := q.QueryContext(ctx, releaseColumns+where, args...)
	if err != nil {
		return nil, wrap("list releases", err)
	}
	rels, err := collect(rows, scanRelease)
	if err != nil {
		return nil, err
	}
	for i, rel := range rels {
		if rels[i], err = withReleaseArtifacts(ctx, q, rel); err != nil {
			return nil, err
		}
	}
	return rels, nil
}

const releaseColumns = `SELECT id, project_id, version, source, provenance,
	created_by, created_at, labels, annotations FROM releases`

func scanRelease(sc scanner) (release.Release, error) {
	var (
		rel                                release.Release
		source, prov, createdBy, createdAt string
		labels, annotations                string
	)
	if err := sc.Scan(&rel.ID, &rel.ProjectID, &rel.Version, &source, &prov,
		&createdBy, &createdAt, &labels, &annotations); err != nil {
		return release.Release{}, err
	}
	var err error
	if rel.Source, err = decodeSourceRevision(source); err != nil {
		return release.Release{}, err
	}
	if rel.Provenance, err = decodeDocumentRef(prov); err != nil {
		return release.Release{}, err
	}
	if rel.CreatedBy, err = decodePrincipal(createdBy); err != nil {
		return release.Release{}, err
	}
	if rel.CreatedAt, err = decodeTime(createdAt); err != nil {
		return release.Release{}, err
	}
	if rel.Labels, err = decodeStringMap(labels); err != nil {
		return release.Release{}, err
	}
	if rel.Annotations, err = decodeStringMap(annotations); err != nil {
		return release.Release{}, err
	}
	return rel, nil
}

func withReleaseArtifacts(ctx context.Context, q querier, rel release.Release) (release.Release, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT role, artifact_id FROM release_artifacts WHERE release_id = ? ORDER BY role`, rel.ID)
	if err != nil {
		return release.Release{}, wrap("load release artifacts", err)
	}
	rel.Artifacts, err = collect(rows, func(sc scanner) (release.Artifact, error) {
		var a release.Artifact
		err := sc.Scan(&a.Role, &a.ArtifactID)
		return a, err
	})
	if err != nil {
		return release.Release{}, err
	}
	return rel, nil
}

type planRepo struct{ s *Store }

func (r planRepo) Create(ctx context.Context, p plan.DeploymentPlan) error {
	return r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		for _, ref := range []struct{ query, id, what string }{
			{`SELECT 1 FROM projects WHERE id = ?`, string(p.ProjectID), "project"},
			{`SELECT 1 FROM environments WHERE id = ?`, string(p.EnvironmentID), "environment"},
			{`SELECT 1 FROM releases WHERE id = ?`, string(p.ReleaseID), "release"},
		} {
			if err := mustExist(ctx, q, ref.query, ref.id,
				fmt.Sprintf("%s %s", ref.what, ref.id)); err != nil {
				return err
			}
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM plans WHERE id = ?`, p.ID,
			fmt.Sprintf("plan %s", p.ID),
		); err != nil {
			return err
		}
		operations, err := encodeOperations(p.Operations)
		if err != nil {
			return err
		}
		decision, err := encodeDecision(p.Policy)
		if err != nil {
			return err
		}
		rollback, err := encodeRollback(p.Rollback)
		if err != nil {
			return err
		}
		createdBy, err := encodePrincipal(p.CreatedBy)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx,
			`INSERT INTO plans
			   (id, project_id, environment_id, release_id, base_revision, strategy,
			    operations, policy, rollback_plan, created_by, created_at, expires_at, hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.ProjectID, p.EnvironmentID, p.ReleaseID, p.BaseRevision, string(p.Strategy),
			operations, decision, rollback, createdBy,
			encodeTime(p.CreatedAt), encodeTime(p.ExpiresAt), p.Hash.String(),
		)
		return wrap("insert plan", err)
	})
}

func (r planRepo) Get(ctx context.Context, id identity.PlanID) (plan.DeploymentPlan, error) {
	p, err := scanPlan(r.s.conn(ctx).QueryRowContext(ctx, planColumns+` WHERE id = ?`, id))
	if err != nil {
		return plan.DeploymentPlan{}, notFoundAs(err, fmt.Sprintf("plan %s", id))
	}
	return p, nil
}

const planColumns = `SELECT id, project_id, environment_id, release_id, base_revision, strategy,
	operations, policy, rollback_plan, created_by, created_at, expires_at, hash FROM plans`

func scanPlan(sc scanner) (plan.DeploymentPlan, error) {
	var (
		p                               plan.DeploymentPlan
		strategy                        string
		operations, decision, rollback  string
		createdBy, createdAt, expiresAt string
		hash                            string
	)
	if err := sc.Scan(&p.ID, &p.ProjectID, &p.EnvironmentID, &p.ReleaseID, &p.BaseRevision,
		&strategy, &operations, &decision, &rollback, &createdBy, &createdAt, &expiresAt,
		&hash); err != nil {
		return plan.DeploymentPlan{}, err
	}
	p.Strategy = deployment.Strategy(strategy)

	var err error
	if p.Operations, err = decodeOperations(operations); err != nil {
		return plan.DeploymentPlan{}, err
	}
	if p.Policy, err = decodeDecision(decision); err != nil {
		return plan.DeploymentPlan{}, err
	}
	if p.Rollback, err = decodeRollback(rollback); err != nil {
		return plan.DeploymentPlan{}, err
	}
	if p.CreatedBy, err = decodePrincipal(createdBy); err != nil {
		return plan.DeploymentPlan{}, err
	}
	if p.CreatedAt, err = decodeTime(createdAt); err != nil {
		return plan.DeploymentPlan{}, err
	}
	if p.ExpiresAt, err = decodeTime(expiresAt); err != nil {
		return plan.DeploymentPlan{}, err
	}
	if p.Hash, err = digest.Parse(hash); err != nil {
		return plan.DeploymentPlan{}, fmt.Errorf("sqlite: plan %s: %w", p.ID, err)
	}
	return p, nil
}

type deploymentRepo struct{ s *Store }

func (r deploymentRepo) Create(ctx context.Context, d deployment.Deployment) (identity.Revision, error) {
	const first identity.Revision = 1
	err := r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		for _, ref := range []struct{ query, id, what string }{
			{`SELECT 1 FROM projects WHERE id = ?`, string(d.ProjectID), "project"},
			{`SELECT 1 FROM environments WHERE id = ?`, string(d.EnvironmentID), "environment"},
			{`SELECT 1 FROM releases WHERE id = ?`, string(d.ReleaseID), "release"},
			{`SELECT 1 FROM plans WHERE id = ?`, string(d.PlanID), "plan"},
		} {
			if err := mustExist(ctx, q, ref.query, ref.id,
				fmt.Sprintf("%s %s", ref.what, ref.id)); err != nil {
				return err
			}
		}
		// The deployment this one replaces has to be one we recorded. A
		// dangling reference would make the rollback target unresolvable at
		// exactly the moment it is needed.
		if d.Previous != nil {
			if err := mustExist(ctx, q,
				`SELECT 1 FROM deployments WHERE id = ?`, string(*d.Previous),
				fmt.Sprintf("deployment %s", *d.Previous),
			); err != nil {
				return err
			}
		}
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM deployments WHERE id = ?`, d.ID,
			fmt.Sprintf("deployment %s", d.ID),
		); err != nil {
			return err
		}
		actor, err := encodePrincipal(d.Actor)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx,
			`INSERT INTO deployments
			   (id, project_id, environment_id, release_id, plan_id, strategy, status,
			    trigger_type, trigger_detail, actor, created_at, started_at, finished_at,
			    previous_id, revision)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.ProjectID, d.EnvironmentID, d.ReleaseID, d.PlanID,
			string(d.Strategy), string(d.Status), string(d.Trigger.Type), d.Trigger.Detail,
			actor, encodeTime(d.CreatedAt), encodeTimePtr(d.StartedAt), encodeTimePtr(d.FinishedAt),
			deploymentRef(d.Previous), uint64(first),
		)
		return wrap("insert deployment", err)
	})
	if err != nil {
		return 0, err
	}
	return first, nil
}

func (r deploymentRepo) Update(ctx context.Context, d deployment.Deployment) (identity.Revision, error) {
	var committed identity.Revision
	err := r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		stored, err := scanDeployment(q.QueryRowContext(ctx, deploymentColumns+` WHERE id = ?`, d.ID))
		if err != nil {
			return notFoundAs(err, fmt.Sprintf("deployment %s", d.ID))
		}
		if !stored.Revision.Matches(d.Revision) {
			return fmt.Errorf("deployment %s: %w", d.ID, port.ErrRevisionConflict)
		}
		actor, err := encodePrincipal(d.Actor)
		if err != nil {
			return err
		}
		committed = stored.Revision.Next()
		_, err = q.ExecContext(ctx,
			`UPDATE deployments
			    SET strategy = ?, status = ?, trigger_type = ?, trigger_detail = ?, actor = ?,
			        started_at = ?, finished_at = ?, previous_id = ?, revision = ?
			  WHERE id = ? AND revision = ?`,
			string(d.Strategy), string(d.Status), string(d.Trigger.Type), d.Trigger.Detail, actor,
			encodeTimePtr(d.StartedAt), encodeTimePtr(d.FinishedAt), deploymentRef(d.Previous),
			committed, d.ID, stored.Revision,
		)
		return wrap("update deployment", err)
	})
	if err != nil {
		return 0, err
	}
	return committed, nil
}

func (r deploymentRepo) Get(ctx context.Context, id identity.DeploymentID) (deployment.Deployment, error) {
	d, err := scanDeployment(r.s.conn(ctx).QueryRowContext(ctx, deploymentColumns+` WHERE id = ?`, id))
	if err != nil {
		return deployment.Deployment{}, notFoundAs(err, fmt.Sprintf("deployment %s", id))
	}
	return d, nil
}

// ListForEnvironment orders by creation time descending, with the id breaking a
// tie so that two deployments created in the same instant still have one order
// rather than the driver's. Ordering is part of the contract: the first result
// is how a caller asks what is currently deployed.
func (r deploymentRepo) ListForEnvironment(ctx context.Context, e identity.EnvironmentID) ([]deployment.Deployment, error) {
	rows, err := r.s.conn(ctx).QueryContext(ctx,
		deploymentColumns+` WHERE environment_id = ? ORDER BY created_at DESC, id DESC`, e)
	if err != nil {
		return nil, wrap("list deployments", err)
	}
	return collect(rows, scanDeployment)
}

// FindActive asks for the deployment that has not finished. Which statuses
// count as finished comes from the domain rather than from the schema, so a new
// status is added in one place; the query only has to know how to exclude them.
func (r deploymentRepo) FindActive(ctx context.Context, e identity.EnvironmentID) (deployment.Deployment, error) {
	terminal := deployment.TerminalStatuses()
	marks := make([]string, len(terminal))
	args := make([]any, 0, len(terminal)+1)
	args = append(args, e)
	for i, s := range terminal {
		marks[i] = "?"
		args = append(args, string(s))
	}
	query := deploymentColumns + ` WHERE environment_id = ? AND status NOT IN (` +
		strings.Join(marks, ", ") + `) ORDER BY created_at DESC, id DESC LIMIT 1`

	d, err := scanDeployment(r.s.conn(ctx).QueryRowContext(ctx, query, args...))
	if err != nil {
		return deployment.Deployment{}, notFoundAs(err,
			fmt.Sprintf("active deployment in environment %s", e))
	}
	return d, nil
}

const deploymentColumns = `SELECT id, project_id, environment_id, release_id, plan_id,
	strategy, status, trigger_type, trigger_detail, actor, created_at, started_at,
	finished_at, previous_id, revision FROM deployments`

func scanDeployment(sc scanner) (deployment.Deployment, error) {
	var (
		d                                 deployment.Deployment
		strategy, status, triggerType     string
		actor, createdAt                  string
		startedAt, finishedAt, previousID sql.NullString
	)
	if err := sc.Scan(&d.ID, &d.ProjectID, &d.EnvironmentID, &d.ReleaseID, &d.PlanID,
		&strategy, &status, &triggerType, &d.Trigger.Detail, &actor, &createdAt,
		&startedAt, &finishedAt, &previousID, &d.Revision); err != nil {
		return deployment.Deployment{}, err
	}
	d.Strategy = deployment.Strategy(strategy)
	d.Status = deployment.Status(status)
	d.Trigger.Type = deployment.TriggerType(triggerType)

	var err error
	if d.Actor, err = decodePrincipal(actor); err != nil {
		return deployment.Deployment{}, err
	}
	if d.CreatedAt, err = decodeTime(createdAt); err != nil {
		return deployment.Deployment{}, err
	}
	if d.StartedAt, err = decodeTimePtr(startedAt); err != nil {
		return deployment.Deployment{}, err
	}
	if d.FinishedAt, err = decodeTimePtr(finishedAt); err != nil {
		return deployment.Deployment{}, err
	}
	if previousID.Valid {
		id := identity.DeploymentID(previousID.String)
		d.Previous = &id
	}
	return d, nil
}

func deploymentRef(id *identity.DeploymentID) any {
	if id == nil {
		return nil
	}
	return string(*id)
}

// scanner is what *sql.Row and *sql.Rows have in common, so one scan function
// serves a single-row lookup and a listing.
type scanner interface{ Scan(dest ...any) error }

func collect[T any](rows *sql.Rows, scan func(scanner) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, wrap("scan", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("scan", err)
	}
	return out, nil
}

// mustExist and mustNotExist take args as a single value or a slice, so a
// one-column check reads as one.
func exists(ctx context.Context, q querier, query string, args any) (bool, error) {
	list, ok := args.([]any)
	if !ok {
		list = []any{args}
	}
	var one int
	err := q.QueryRowContext(ctx, query, list...).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, wrap("check", err)
	}
	return true, nil
}

func mustExist(ctx context.Context, q querier, query string, args any, what string) error {
	found, err := exists(ctx, q, query, args)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: %w", what, port.ErrNotFound)
	}
	return nil
}

func mustNotExist(ctx context.Context, q querier, query string, args any, what string) error {
	found, err := exists(ctx, q, query, args)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("%s: %w", what, port.ErrAlreadyExists)
	}
	return nil
}

// notFoundAs translates the driver's "no rows" into the ports' vocabulary and
// leaves every other failure alone: a closed database is not a missing row.
func notFoundAs(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, port.ErrNotFound)
	}
	return wrap("load "+what, err)
}

func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlite: %s: %w", what, err)
}

type approvalRepo struct{ s *Store }

func (r approvalRepo) Create(ctx context.Context, a policy.Approval) error {
	// Validated before the write rather than trusted from the caller: this is
	// the last place an approval that cannot say what it approved could still
	// be refused, and once stored it is a record nothing may edit.
	if err := a.Validate(); err != nil {
		return err
	}
	return r.s.WithinTransaction(ctx, func(ctx context.Context) error {
		q := r.s.conn(ctx)
		if err := mustNotExist(ctx, q,
			`SELECT 1 FROM approvals WHERE id = ?`, string(a.ID),
			fmt.Sprintf("approval %s", a.ID),
		); err != nil {
			return err
		}
		principal, err := encodePrincipal(a.Principal)
		if err != nil {
			return err
		}
		var expires any
		if !a.ExpiresAt.IsZero() {
			expires = encodeTime(a.ExpiresAt)
		}
		_, err = q.ExecContext(ctx,
			`INSERT INTO approvals
			   (id, subject_kind, subject_id, subject_revision,
			    principal, decision, reason, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(a.ID), a.Subject.Kind, a.Subject.ID, a.Subject.Revision,
			principal, string(a.Decision), a.Reason, encodeTime(a.CreatedAt), expires,
		)
		return wrap("insert approval", err)
	})
}

func (r approvalRepo) ListForSubject(ctx context.Context, kind, id string) ([]policy.Approval, error) {
	rows, err := r.s.conn(ctx).QueryContext(ctx,
		approvalColumns+` WHERE subject_kind = ? AND subject_id = ? ORDER BY seq`, kind, id)
	if err != nil {
		return nil, wrap("list approvals", err)
	}
	return collect(rows, scanApproval)
}

const approvalColumns = `SELECT id, subject_kind, subject_id, subject_revision,
	principal, decision, reason, created_at, expires_at FROM approvals`

func scanApproval(row scanner) (policy.Approval, error) {
	var (
		a       policy.Approval
		id      string
		who     string
		d       string
		created string
		expires sql.NullString
	)
	if err := row.Scan(
		&id, &a.Subject.Kind, &a.Subject.ID, &a.Subject.Revision,
		&who, &d, &a.Reason, &created, &expires,
	); err != nil {
		return policy.Approval{}, err
	}
	a.ID = identity.ApprovalID(id)
	a.Decision = policy.ApprovalDecision(d)
	principal, err := decodePrincipal(who)
	if err != nil {
		return policy.Approval{}, err
	}
	a.Principal = principal
	if a.CreatedAt, err = decodeTime(created); err != nil {
		return policy.Approval{}, err
	}
	if expires.Valid {
		if a.ExpiresAt, err = decodeTime(expires.String); err != nil {
			return policy.Approval{}, err
		}
	}
	return a, nil
}
