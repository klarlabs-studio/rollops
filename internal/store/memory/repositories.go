package memory

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
)

// Aggregates are copied on the way in and on the way out. A store that handed
// back the map it holds would let a caller edit persisted state by writing to a
// value it merely read — a bug the SQLite store cannot have, so the in-memory
// one must not either.

type projects struct{ s *Store }

func (r projects) Create(ctx context.Context, p project.Project) error {
	return r.s.write(ctx, func(st *state) error {
		if _, taken := st.projects[p.ID]; taken {
			return fmt.Errorf("project %s: %w", p.ID, port.ErrAlreadyExists)
		}
		if _, taken := findProjectByName(st, p.Name); taken {
			return fmt.Errorf("project %q: %w", p.Name, port.ErrAlreadyExists)
		}
		p.Revision = 1
		st.projects[p.ID] = copyProject(p)
		return nil
	})
}

func (r projects) Update(ctx context.Context, p project.Project) (identity.Revision, error) {
	var committed identity.Revision
	err := r.s.write(ctx, func(st *state) error {
		stored, ok := st.projects[p.ID]
		if !ok {
			return fmt.Errorf("project %s: %w", p.ID, port.ErrNotFound)
		}
		if !stored.Revision.Matches(p.Revision) {
			return fmt.Errorf("project %s: %w", p.ID, port.ErrRevisionConflict)
		}
		if other, taken := findProjectByName(st, p.Name); taken && other != p.ID {
			return fmt.Errorf("project %q: %w", p.Name, port.ErrAlreadyExists)
		}
		committed = stored.Revision.Next()
		p.Revision = committed
		st.projects[p.ID] = copyProject(p)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return committed, nil
}

func (r projects) Get(ctx context.Context, id identity.ProjectID) (project.Project, error) {
	var out project.Project
	err := r.s.read(ctx, func(st *state) error {
		p, ok := st.projects[id]
		if !ok {
			return fmt.Errorf("project %s: %w", id, port.ErrNotFound)
		}
		out = copyProject(p)
		return nil
	})
	return out, err
}

func (r projects) GetByName(ctx context.Context, name string) (project.Project, error) {
	var out project.Project
	err := r.s.read(ctx, func(st *state) error {
		id, ok := findProjectByName(st, name)
		if !ok {
			return fmt.Errorf("project %q: %w", name, port.ErrNotFound)
		}
		out = copyProject(st.projects[id])
		return nil
	})
	return out, err
}

func (r projects) List(ctx context.Context) ([]project.Project, error) {
	var out []project.Project
	err := r.s.read(ctx, func(st *state) error {
		for _, p := range sortedByID(st.projects, nil) {
			out = append(out, copyProject(p))
		}
		return nil
	})
	return out, err
}

func findProjectByName(st *state, name string) (identity.ProjectID, bool) {
	for id, p := range st.projects {
		if p.Name == name {
			return id, true
		}
	}
	return "", false
}

type environments struct{ s *Store }

func (r environments) Create(ctx context.Context, e environment.Environment) error {
	return r.s.write(ctx, func(st *state) error {
		if _, ok := st.projects[e.ProjectID]; !ok {
			return fmt.Errorf("project %s: %w", e.ProjectID, port.ErrNotFound)
		}
		if _, taken := st.environments[e.ID]; taken {
			return fmt.Errorf("environment %s: %w", e.ID, port.ErrAlreadyExists)
		}
		if _, taken := findEnvironmentByName(st, e.ProjectID, e.Name); taken {
			return fmt.Errorf("environment %q: %w", e.Name, port.ErrAlreadyExists)
		}
		e.Revision = 1
		st.environments[e.ID] = copyEnvironment(e)
		return nil
	})
}

func (r environments) Update(ctx context.Context, e environment.Environment) (identity.Revision, error) {
	var committed identity.Revision
	err := r.s.write(ctx, func(st *state) error {
		stored, ok := st.environments[e.ID]
		if !ok {
			return fmt.Errorf("environment %s: %w", e.ID, port.ErrNotFound)
		}
		if !stored.Revision.Matches(e.Revision) {
			return fmt.Errorf("environment %s: %w", e.ID, port.ErrRevisionConflict)
		}
		if other, taken := findEnvironmentByName(st, stored.ProjectID, e.Name); taken && other != e.ID {
			return fmt.Errorf("environment %q: %w", e.Name, port.ErrAlreadyExists)
		}
		committed = stored.Revision.Next()
		e.Revision = committed
		st.environments[e.ID] = copyEnvironment(e)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return committed, nil
}

func (r environments) Get(ctx context.Context, id identity.EnvironmentID) (environment.Environment, error) {
	var out environment.Environment
	err := r.s.read(ctx, func(st *state) error {
		e, ok := st.environments[id]
		if !ok {
			return fmt.Errorf("environment %s: %w", id, port.ErrNotFound)
		}
		out = copyEnvironment(e)
		return nil
	})
	return out, err
}

func (r environments) GetByName(ctx context.Context, p identity.ProjectID, name string) (environment.Environment, error) {
	var out environment.Environment
	err := r.s.read(ctx, func(st *state) error {
		id, ok := findEnvironmentByName(st, p, name)
		if !ok {
			return fmt.Errorf("environment %q: %w", name, port.ErrNotFound)
		}
		out = copyEnvironment(st.environments[id])
		return nil
	})
	return out, err
}

func (r environments) List(ctx context.Context, p identity.ProjectID) ([]environment.Environment, error) {
	var out []environment.Environment
	err := r.s.read(ctx, func(st *state) error {
		in := func(e environment.Environment) bool { return e.ProjectID == p }
		for _, e := range sortedByID(st.environments, in) {
			out = append(out, copyEnvironment(e))
		}
		return nil
	})
	return out, err
}

func findEnvironmentByName(st *state, p identity.ProjectID, name string) (identity.EnvironmentID, bool) {
	for id, e := range st.environments {
		if e.ProjectID == p && e.Name == name {
			return id, true
		}
	}
	return "", false
}

type artifacts struct{ s *Store }

func (r artifacts) Create(ctx context.Context, a artifact.Artifact) error {
	return r.s.write(ctx, func(st *state) error {
		if _, ok := st.projects[a.ProjectID]; !ok {
			return fmt.Errorf("project %s: %w", a.ProjectID, port.ErrNotFound)
		}
		if _, taken := st.artifacts[a.ID]; taken {
			return fmt.Errorf("artifact %s: %w", a.ID, port.ErrAlreadyExists)
		}
		if _, taken := findArtifactByDigest(st, a.ProjectID, a.Kind, a.Digest.String()); taken {
			return fmt.Errorf("artifact %s: %w", a.Digest, port.ErrAlreadyExists)
		}
		st.artifacts[a.ID] = copyArtifact(a)
		return nil
	})
}

func (r artifacts) Get(ctx context.Context, id identity.ArtifactID) (artifact.Artifact, error) {
	var out artifact.Artifact
	err := r.s.read(ctx, func(st *state) error {
		a, ok := st.artifacts[id]
		if !ok {
			return fmt.Errorf("artifact %s: %w", id, port.ErrNotFound)
		}
		out = copyArtifact(a)
		return nil
	})
	return out, err
}

func (r artifacts) GetByDigest(ctx context.Context, p identity.ProjectID, k artifact.Kind, d string) (artifact.Artifact, error) {
	var out artifact.Artifact
	err := r.s.read(ctx, func(st *state) error {
		id, ok := findArtifactByDigest(st, p, k, d)
		if !ok {
			return fmt.Errorf("artifact %s: %w", d, port.ErrNotFound)
		}
		out = copyArtifact(st.artifacts[id])
		return nil
	})
	return out, err
}

func (r artifacts) List(ctx context.Context, p identity.ProjectID) ([]artifact.Artifact, error) {
	var out []artifact.Artifact
	err := r.s.read(ctx, func(st *state) error {
		in := func(a artifact.Artifact) bool { return a.ProjectID == p }
		for _, a := range sortedByID(st.artifacts, in) {
			out = append(out, copyArtifact(a))
		}
		return nil
	})
	return out, err
}

func findArtifactByDigest(st *state, p identity.ProjectID, k artifact.Kind, d string) (identity.ArtifactID, bool) {
	for id, a := range st.artifacts {
		if a.ProjectID == p && a.Kind == k && a.Digest.String() == d {
			return id, true
		}
	}
	return "", false
}

type releases struct{ s *Store }

func (r releases) Create(ctx context.Context, rel release.Release) error {
	return r.s.write(ctx, func(st *state) error {
		if _, ok := st.projects[rel.ProjectID]; !ok {
			return fmt.Errorf("project %s: %w", rel.ProjectID, port.ErrNotFound)
		}
		for _, a := range rel.Artifacts {
			if _, ok := st.artifacts[a.ArtifactID]; !ok {
				return fmt.Errorf("artifact %s: %w", a.ArtifactID, port.ErrNotFound)
			}
		}
		if _, taken := st.releases[rel.ID]; taken {
			return fmt.Errorf("release %s: %w", rel.ID, port.ErrAlreadyExists)
		}
		if _, taken := findReleaseByVersion(st, rel.ProjectID, rel.Version); taken {
			return fmt.Errorf("release %q: %w", rel.Version, port.ErrAlreadyExists)
		}
		st.releases[rel.ID] = copyRelease(rel)
		return nil
	})
}

func (r releases) Get(ctx context.Context, id identity.ReleaseID) (release.Release, error) {
	var out release.Release
	err := r.s.read(ctx, func(st *state) error {
		rel, ok := st.releases[id]
		if !ok {
			return fmt.Errorf("release %s: %w", id, port.ErrNotFound)
		}
		out = copyRelease(rel)
		return nil
	})
	return out, err
}

func (r releases) GetByVersion(ctx context.Context, p identity.ProjectID, version string) (release.Release, error) {
	var out release.Release
	err := r.s.read(ctx, func(st *state) error {
		id, ok := findReleaseByVersion(st, p, version)
		if !ok {
			return fmt.Errorf("release %q: %w", version, port.ErrNotFound)
		}
		out = copyRelease(st.releases[id])
		return nil
	})
	return out, err
}

func (r releases) List(ctx context.Context, p identity.ProjectID) ([]release.Release, error) {
	var out []release.Release
	err := r.s.read(ctx, func(st *state) error {
		in := func(rel release.Release) bool { return rel.ProjectID == p }
		for _, rel := range sortedByID(st.releases, in) {
			out = append(out, copyRelease(rel))
		}
		return nil
	})
	return out, err
}

// FindByFingerprint recomputes rather than reads a stored value: the
// fingerprint is derived from the release's content, so deriving it is the only
// way an answer can be wrong in the same direction as the question.
func (r releases) FindByFingerprint(ctx context.Context, p identity.ProjectID, f string) ([]release.Release, error) {
	var out []release.Release
	err := r.s.read(ctx, func(st *state) error {
		in := func(rel release.Release) bool {
			return rel.ProjectID == p && rel.Fingerprint().String() == f
		}
		for _, rel := range sortedByID(st.releases, in) {
			out = append(out, copyRelease(rel))
		}
		return nil
	})
	return out, err
}

func findReleaseByVersion(st *state, p identity.ProjectID, version string) (identity.ReleaseID, bool) {
	for id, rel := range st.releases {
		if rel.ProjectID == p && rel.Version == version {
			return id, true
		}
	}
	return "", false
}

func copyProject(p project.Project) project.Project {
	p.Labels = maps.Clone(p.Labels)
	return p
}

func copyEnvironment(e environment.Environment) environment.Environment {
	e.Targets = slices.Clone(e.Targets)
	for i := range e.Targets {
		e.Targets[i].Config = maps.Clone(e.Targets[i].Config)
		e.Targets[i].Labels = maps.Clone(e.Targets[i].Labels)
	}
	e.Policies = slices.Clone(e.Policies)
	e.Variables = maps.Clone(e.Variables)
	e.Labels = maps.Clone(e.Labels)
	return e
}

func copyArtifact(a artifact.Artifact) artifact.Artifact {
	a.Metadata = maps.Clone(a.Metadata)
	a.SBOMs = slices.Clone(a.SBOMs)
	a.Signatures = slices.Clone(a.Signatures)
	return a
}

func copyRelease(r release.Release) release.Release {
	r.Artifacts = slices.Clone(r.Artifacts)
	r.Labels = maps.Clone(r.Labels)
	r.Annotations = maps.Clone(r.Annotations)
	return r
}
