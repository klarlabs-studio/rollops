package cliapi

import (
	"context"
	"errors"
	"io"
	"strings"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
)

// The nouns of spec 26.1. Each offers the list, get and create the service has
// and nothing else — there is no update or delete here because there is none
// behind it, and a command that could only fail is worse than a command that
// is missing.

func (a *App) project(ctx context.Context, args []string) error {
	op, rest, err := sub("project", args, "list", "get", "create")
	if err != nil {
		return err
	}
	switch op {
	case "list":
		return a.projectList(ctx, rest)
	case "get":
		return a.projectGet(ctx, rest)
	default:
		return a.projectCreate(ctx, rest)
	}
}

// ProjectsView is a page of projects.
type ProjectsView struct {
	Projects      []ProjectView `json:"projects"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

func (a *App) projectList(ctx context.Context, args []string) error {
	fs := flags("project list")
	out := outputFlag(fs)
	pg := pageFlags(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	got, err := a.svc.ListProjects(ctx, apiv2.ListProjectsRequest{Page: *pg})
	if err != nil {
		return err
	}
	view := ProjectsView{Projects: mapped(got.Projects, project), NextPageToken: got.Next}
	return a.render(f, view, func(w io.Writer) {
		for _, p := range view.Projects {
			printf(w, "%s\t%s\n", p.ProjectID, p.Name)
		}
		nextPage(w, view.NextPageToken)
	})
}

func (a *App) projectGet(ctx context.Context, args []string) error {
	fs := flags("project get")
	out := outputFlag(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "a project id")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	p, err := a.svc.GetProject(ctx, apiv2.GetProjectRequest{ID: id})
	if err != nil {
		return err
	}
	return a.render(f, project(p), func(w io.Writer) { describeProject(w, project(p)) })
}

func describeProject(w io.Writer, p ProjectView) {
	printf(w, "%s\t%s\trevision %d\n", p.ProjectID, p.Name, p.Revision)
	if p.Description != "" {
		printf(w, "  %s\n", p.Description)
	}
}

func (a *App) projectCreate(ctx context.Context, args []string) error {
	fs := flags("project create")
	out := outputFlag(fs)
	name := fs.String("name", "", "the project's name; unique")
	description := fs.String("description", "", "what it is for")
	key := fs.String("idempotency-key", "", "repeat on a retry to get the project the first call created")
	var labels kvFlag
	fs.Var(&labels, "label", "key=value, repeatable")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	p, err := a.svc.CreateProject(ctx, apiv2.CreateProjectRequest{
		Name:           *name,
		Description:    *description,
		Labels:         labels,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	return a.render(f, project(p), func(w io.Writer) { describeProject(w, project(p)) })
}

func (a *App) environment(ctx context.Context, args []string) error {
	op, rest, err := sub("environment", args, "list", "get", "create")
	if err != nil {
		return err
	}
	switch op {
	case "list":
		return a.environmentList(ctx, rest)
	case "get":
		return a.environmentGet(ctx, rest)
	default:
		return a.environmentCreate(ctx, rest)
	}
}

// EnvironmentsView is a page of environments.
type EnvironmentsView struct {
	Environments  []EnvironmentView `json:"environments"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}

func (a *App) environmentList(ctx context.Context, args []string) error {
	fs := flags("environment list")
	out := outputFlag(fs)
	pg := pageFlags(fs)
	// The project is required because the service requires it, and for the
	// reason it gives: environment names are unique within a project, so a
	// list spanning projects would show several rows called "production".
	projectID := fs.String("project", "", "the project whose environments to list")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	got, err := a.svc.ListEnvironments(ctx, apiv2.ListEnvironmentsRequest{
		ProjectID: *projectID,
		Page:      *pg,
	})
	if err != nil {
		return err
	}
	view := EnvironmentsView{Environments: mapped(got.Environments, environment), NextPageToken: got.Next}
	return a.render(f, view, func(w io.Writer) {
		for _, e := range view.Environments {
			printf(w, "%s\t%s\t%s\n", e.EnvironmentID, e.Name, e.Kind)
		}
		nextPage(w, view.NextPageToken)
	})
}

func (a *App) environmentGet(ctx context.Context, args []string) error {
	fs := flags("environment get")
	out := outputFlag(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "an environment id")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	e, err := a.svc.GetEnvironment(ctx, apiv2.GetEnvironmentRequest{ID: id})
	if err != nil {
		return err
	}
	return a.render(f, environment(e), func(w io.Writer) { describeEnvironment(w, environment(e)) })
}

func describeEnvironment(w io.Writer, e EnvironmentView) {
	printf(w, "%s\t%s\t%s\trevision %d\n", e.EnvironmentID, e.Name, e.Kind, e.Revision)
	for _, t := range e.Targets {
		printf(w, "  target %s\t%s\n", t.Name, t.Driver)
	}
	for _, p := range e.Policies {
		printf(w, "  policy %s\t%s\n", p.Name, p.Mode)
	}
}

func (a *App) environmentCreate(ctx context.Context, args []string) error {
	fs := flags("environment create")
	out := outputFlag(fs)
	projectID := fs.String("project", "", "the project the environment belongs to")
	name := fs.String("name", "", "the environment's name; unique within the project")
	kind := fs.String("kind", "", "what sort of environment it is")
	ttl := fs.Duration("ttl", 0, "how long it is meant to last; zero for indefinitely")
	deleteOnClose := fs.Bool("delete-on-close", false, "remove it when whatever it was made for ends")
	key := fs.String("idempotency-key", "", "repeat on a retry to get the environment the first call made")

	var targets, policies listFlag
	var labels kvFlag
	var configs, secrets, vars, varSecrets kvFlag
	fs.Var(&targets, "target", "name=NAME,driver=DRIVER; repeatable")
	fs.Var(&configs, "target-config", "TARGET.KEY=VALUE; repeatable")
	fs.Var(&secrets, "target-secret", "TARGET.KEY=SECRET_NAME; repeatable")
	fs.Var(&policies, "policy", "name=NAME[,ref=REF][,mode=MODE]; repeatable")
	fs.Var(&vars, "var", "KEY=VALUE; repeatable")
	fs.Var(&varSecrets, "var-secret", "KEY=SECRET_NAME; repeatable")
	fs.Var(&labels, "label", "key=value, repeatable")

	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	specs, err := targetSpecs(targets, configs, secrets)
	if err != nil {
		return err
	}
	bindings, err := policyBindings(policies)
	if err != nil {
		return err
	}
	variables, err := values(vars, varSecrets)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	e, err := a.svc.CreateEnvironment(ctx, apiv2.CreateEnvironmentRequest{
		ProjectID: *projectID,
		Name:      *name,
		Kind:      *kind,
		Targets:   specs,
		Policies:  bindings,
		Variables: variables,
		Labels:    labels,
		Lifecycle: apiv2.Lifecycle{TTL: *ttl, DeleteOnClose: *deleteOnClose},
		Actor:     who,

		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	return a.render(f, environment(e), func(w io.Writer) { describeEnvironment(w, environment(e)) })
}

// values turns the two ways of giving a setting into the one type the service
// takes. A literal and a secret under the same key is refused here rather than
// later, so the message names the key.
func values(literals, secrets kvFlag) (map[string]apiv2.Value, error) {
	if len(literals) == 0 && len(secrets) == 0 {
		return nil, nil
	}
	out := make(map[string]apiv2.Value, len(literals)+len(secrets))
	for k, v := range literals {
		out[k] = apiv2.Value{Literal: v}
	}
	for k, name := range secrets {
		if _, both := out[k]; both {
			return nil, invalid("%q is given as both a literal and a secret", k)
		}
		out[k] = apiv2.Value{Secret: name}
	}
	return out, nil
}

// targetSpecs assembles the declared targets and the settings addressed to
// them. A setting naming a target nobody declared is refused: silently dropping
// it would create an environment missing what the operator thought they gave it.
func targetSpecs(declared listFlag, configs, secrets kvFlag) ([]apiv2.TargetSpec, error) {
	if len(declared) == 0 {
		if len(configs) > 0 || len(secrets) > 0 {
			return nil, invalid("--target-config and --target-secret need a --target to address")
		}
		return nil, nil
	}
	order := make([]string, 0, len(declared))
	specs := make(map[string]*apiv2.TargetSpec, len(declared))
	for _, spec := range declared {
		got, err := fields(spec)
		if err != nil {
			return nil, invalid("--target %q: %w", spec, err)
		}
		if err := only(got, "name", "driver"); err != nil {
			return nil, invalid("--target %q: %w", spec, err)
		}
		name := got["name"]
		if name == "" {
			return nil, invalid("--target %q: name is required", spec)
		}
		if _, twice := specs[name]; twice {
			return nil, invalid("--target %q: declared twice", name)
		}
		order = append(order, name)
		specs[name] = &apiv2.TargetSpec{Name: name, Driver: got["driver"], Config: map[string]apiv2.Value{}}
	}
	assign := func(flagName string, given kvFlag, build func(string) apiv2.Value) error {
		for dotted, v := range given {
			target, key, err := splitTargetKey(dotted)
			if err != nil {
				return invalid("--%s %q: %w", flagName, dotted, err)
			}
			spec, declared := specs[target]
			if !declared {
				return invalid("--%s %q: no --target named %q", flagName, dotted, target)
			}
			if _, both := spec.Config[key]; both {
				return invalid("%q on target %q is given as both a literal and a secret", key, target)
			}
			spec.Config[key] = build(v)
		}
		return nil
	}
	if err := assign("target-config", configs, func(v string) apiv2.Value {
		return apiv2.Value{Literal: v}
	}); err != nil {
		return nil, err
	}
	if err := assign("target-secret", secrets, func(v string) apiv2.Value {
		return apiv2.Value{Secret: v}
	}); err != nil {
		return nil, err
	}
	out := make([]apiv2.TargetSpec, 0, len(order))
	for _, name := range order {
		spec := specs[name]
		if len(spec.Config) == 0 {
			spec.Config = nil
		}
		out = append(out, *spec)
	}
	return out, nil
}

// splitTargetKey reads TARGET.KEY. It cuts at the first dot because a
// configuration key may well contain one and a target name may not.
func splitTargetKey(s string) (target, key string, err error) {
	target, key, ok := strings.Cut(s, ".")
	if !ok || target == "" || key == "" {
		return "", "", errors.New("want TARGET.KEY")
	}
	return target, key, nil
}

func policyBindings(declared listFlag) ([]apiv2.PolicyBinding, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	out := make([]apiv2.PolicyBinding, 0, len(declared))
	for _, spec := range declared {
		got, err := fields(spec)
		if err != nil {
			return nil, invalid("--policy %q: %w", spec, err)
		}
		if err := only(got, "name", "ref", "mode"); err != nil {
			return nil, invalid("--policy %q: %w", spec, err)
		}
		if got["name"] == "" {
			return nil, invalid("--policy %q: name is required", spec)
		}
		out = append(out, apiv2.PolicyBinding{Name: got["name"], Ref: got["ref"], Mode: got["mode"]})
	}
	return out, nil
}

func (a *App) release(ctx context.Context, args []string) error {
	op, rest, err := sub("release", args, "list", "get", "create", "artifact")
	if err != nil {
		return err
	}
	switch op {
	case "list":
		return a.releaseList(ctx, rest)
	case "get":
		return a.releaseGet(ctx, rest)
	case "create":
		return a.releaseCreate(ctx, rest)
	default:
		return a.artifact(ctx, rest)
	}
}

// ReleasesView is a page of releases.
type ReleasesView struct {
	Releases      []ReleaseView `json:"releases"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

func (a *App) releaseList(ctx context.Context, args []string) error {
	fs := flags("release list")
	out := outputFlag(fs)
	pg := pageFlags(fs)
	projectID := fs.String("project", "", "the project whose releases to list")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	got, err := a.svc.ListReleases(ctx, apiv2.ListReleasesRequest{ProjectID: *projectID, Page: *pg})
	if err != nil {
		return err
	}
	view := ReleasesView{Releases: mapped(got.Releases, release), NextPageToken: got.Next}
	return a.render(f, view, func(w io.Writer) {
		for _, r := range view.Releases {
			printf(w, "%s\t%s\t%s\n", r.ReleaseID, r.Version, r.CreatedAt.Format(rfc3339))
		}
		nextPage(w, view.NextPageToken)
	})
}

func (a *App) releaseGet(ctx context.Context, args []string) error {
	fs := flags("release get")
	out := outputFlag(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "a release id")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	r, err := a.svc.GetRelease(ctx, apiv2.GetReleaseRequest{ID: id})
	if err != nil {
		return err
	}
	return a.render(f, release(r), func(w io.Writer) { describeRelease(w, release(r)) })
}

func describeRelease(w io.Writer, r ReleaseView) {
	printf(w, "%s\t%s\n", r.ReleaseID, r.Version)
	if r.Source.Revision != "" {
		printf(w, "  source %s@%s\n", r.Source.Repository, r.Source.Revision)
	}
	for _, art := range r.Artifacts {
		printf(w, "  %s\t%s\n", art.Role, art.ArtifactID)
	}
}

func (a *App) releaseCreate(ctx context.Context, args []string) error {
	fs := flags("release create")
	out := outputFlag(fs)
	projectID := fs.String("project", "", "the project the release belongs to")
	version := fs.String("version", "", "how the release is asked for; unique within the project")
	key := fs.String("idempotency-key", "", "repeat on a retry to get the release the first call created")

	provider := fs.String("source-provider", "", "where the code came from, e.g. github")
	repository := fs.String("source-repository", "", "")
	revision := fs.String("source-revision", "", "the commit the release was built from")
	ref := fs.String("source-ref", "", "the branch or tag the commit was on")

	var artifacts listFlag
	var labels, annotations kvFlag
	fs.Var(&artifacts, "artifact", "role=ROLE,id=ARTIFACT_ID; repeatable")
	fs.Var(&labels, "label", "key=value, repeatable")
	fs.Var(&annotations, "annotation", "key=value, repeatable")

	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	bound, err := releaseArtifacts(artifacts)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	r, err := a.svc.CreateRelease(ctx, apiv2.CreateReleaseRequest{
		ProjectID: *projectID,
		Version:   *version,
		Artifacts: bound,
		Source: apiv2.Source{
			Provider:   *provider,
			Repository: *repository,
			Revision:   *revision,
			Ref:        *ref,
		},
		Labels:         labels,
		Annotations:    annotations,
		Actor:          who,
		IdempotencyKey: *key,
	})
	if err != nil {
		return err
	}
	return a.render(f, release(r), func(w io.Writer) { describeRelease(w, release(r)) })
}

func releaseArtifacts(declared listFlag) ([]apiv2.ReleaseArtifact, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	out := make([]apiv2.ReleaseArtifact, 0, len(declared))
	for _, spec := range declared {
		got, err := fields(spec)
		if err != nil {
			return nil, invalid("--artifact %q: %w", spec, err)
		}
		if err := only(got, "role", "id"); err != nil {
			return nil, invalid("--artifact %q: %w", spec, err)
		}
		out = append(out, apiv2.ReleaseArtifact{Role: got["role"], ArtifactID: got["id"]})
	}
	return out, nil
}

// artifact sits under release rather than beside it. Spec 26.1 names no noun
// for artifacts, but a release cannot be created without one already
// registered, so a surface that left them out would have a create command that
// could never succeed. Nesting them keeps the top-level nouns the ones the spec
// lists.
func (a *App) artifact(ctx context.Context, args []string) error {
	op, rest, err := sub("release artifact", args, "list", "get", "register")
	if err != nil {
		return err
	}
	switch op {
	case "list":
		return a.artifactList(ctx, rest)
	case "get":
		return a.artifactGet(ctx, rest)
	default:
		return a.artifactRegister(ctx, rest)
	}
}

// ArtifactsView is a page of artifacts.
type ArtifactsView struct {
	Artifacts     []ArtifactView `json:"artifacts"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

func (a *App) artifactList(ctx context.Context, args []string) error {
	fs := flags("release artifact list")
	out := outputFlag(fs)
	pg := pageFlags(fs)
	projectID := fs.String("project", "", "the project whose artifacts to list")
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	got, err := a.svc.ListArtifacts(ctx, apiv2.ListArtifactsRequest{ProjectID: *projectID, Page: *pg})
	if err != nil {
		return err
	}
	view := ArtifactsView{Artifacts: mapped(got.Artifacts, artifact), NextPageToken: got.Next}
	return a.render(f, view, func(w io.Writer) {
		for _, art := range view.Artifacts {
			printf(w, "%s\t%s\t%s\n", art.ArtifactID, art.Kind, art.Digest)
		}
		nextPage(w, view.NextPageToken)
	})
}

func (a *App) artifactGet(ctx context.Context, args []string) error {
	fs := flags("release artifact get")
	out := outputFlag(fs)
	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	id, err := one(fs.Name(), positional, "an artifact id")
	if err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	if err := a.reader(ctx); err != nil {
		return err
	}
	got, err := a.svc.GetArtifact(ctx, apiv2.GetArtifactRequest{ID: id})
	if err != nil {
		return err
	}
	return a.render(f, artifact(got), func(w io.Writer) { describeArtifact(w, artifact(got)) })
}

func describeArtifact(w io.Writer, art ArtifactView) {
	printf(w, "%s\t%s\t%s\n", art.ArtifactID, art.Kind, art.Digest)
	if art.Locator != "" {
		printf(w, "  at %s\n", art.Locator)
	}
}

func (a *App) artifactRegister(ctx context.Context, args []string) error {
	fs := flags("release artifact register")
	out := outputFlag(fs)
	projectID := fs.String("project", "", "the project the artifact belongs to")
	kind := fs.String("kind", "", "what sort of thing it is, e.g. container_image")
	digest := fs.String("digest", "", "what the content must hash to")
	locator := fs.String("locator", "", "where it is, pinned to the digest")
	size := fs.Int64("size", 0, "its size in bytes")
	mediaType := fs.String("media-type", "", "")
	var metadata kvFlag
	fs.Var(&metadata, "metadata", "key=value, repeatable")

	positional, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := none(fs.Name(), positional); err != nil {
		return err
	}
	f, err := format(*out)
	if err != nil {
		return err
	}
	who, err := a.caller(ctx)
	if err != nil {
		return err
	}
	got, err := a.svc.RegisterArtifact(ctx, apiv2.RegisterArtifactRequest{
		ProjectID: *projectID,
		Kind:      *kind,
		Digest:    *digest,
		Locator:   *locator,
		Size:      *size,
		MediaType: *mediaType,
		Metadata:  metadata,
		Actor:     who,
	})
	if err != nil {
		return err
	}
	return a.render(f, artifact(got), func(w io.Writer) { describeArtifact(w, artifact(got)) })
}
