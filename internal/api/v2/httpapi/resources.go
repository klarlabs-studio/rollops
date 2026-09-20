// Projects, environments, artifacts and releases (§23.2).
//
// Creates answer 200 rather than 201. The service does not report whether a
// call created something or replayed an earlier one — idempotency is the point,
// and RegisterArtifact returns an artifact already registered as a matter of
// course (INV-002) — so a transport that answered 201 would be telling a caller
// something happened that may not have.
package httpapi

import (
	"errors"
	"net/http"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// --- projects ---------------------------------------------------------------

type project struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Revision    uint64            `json:"revision"`
}

func wireProject(p apiv2.Project) project {
	return project{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Labels:      p.Labels,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Revision:    p.Revision,
	}
}

type createProjectBody struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

func (h *api) createProject(w http.ResponseWriter, r *http.Request, who identity.Principal) error {
	var body createProjectBody
	if err := h.decode(w, r, &body); err != nil {
		return err
	}
	p, err := h.svc.CreateProject(r.Context(), apiv2.CreateProjectRequest{
		Name:           body.Name,
		Description:    body.Description,
		Labels:         body.Labels,
		Actor:          who,
		IdempotencyKey: idempotencyKey(r),
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireProject(p))
	return nil
}

func (h *api) listProjects(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	p, err := pageOf(r)
	if err != nil {
		return err
	}
	out, err := h.svc.ListProjects(r.Context(), apiv2.ListProjectsRequest{Page: p})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, struct {
		Projects []project `json:"projects"`
		Next     string    `json:"next_page_token,omitempty"`
	}{mapped(out.Projects, wireProject), out.Next})
	return nil
}

func (h *api) getProject(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	p, err := h.svc.GetProject(r.Context(), apiv2.GetProjectRequest{ID: r.PathValue("id")})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireProject(p))
	return nil
}

// --- environments -----------------------------------------------------------

// value is a configured value as a caller supplies it: either an inline
// literal or the name of a secret the provider holds. Setting both is refused
// by the service, because there would be no reading of it that was not a guess.
//
// There is deliberately no read counterpart. A read returns the keys an
// environment configures and never what they resolve to (INV-012), so the write
// shape and the read shape are different types rather than one pressed into
// both jobs — a read that could carry a literal through would be a read a
// caller could not tell from one carrying a resolved secret.
type value struct {
	Literal string `json:"literal,omitempty"`
	Secret  string `json:"secret,omitempty"`
}

type targetSpec struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver"`
	Config map[string]value  `json:"config,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

type targetBinding struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`

	// Config lists the keys the binding sets, sorted, and nothing else. It
	// answers the question a reader actually has — whether this environment
	// configures the thing they expected — without putting the estate's
	// credential layout on the wire.
	Config []string `json:"config"`

	Labels map[string]string `json:"labels,omitempty"`
}

type policyBinding struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// lifecycle carries the TTL as a duration string ("24h"), not as the
// nanosecond integer Go would marshal a time.Duration to. A caller writing
// configuration by hand has to be able to read what they wrote.
type lifecycle struct {
	TTL           string `json:"ttl,omitempty"`
	DeleteOnClose bool   `json:"delete_on_close,omitempty"`
}

type environment struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Targets   []targetBinding `json:"targets"`
	Policies  []policyBinding `json:"policies"`

	// Variables lists the variable names the environment sets, sorted. See
	// targetBinding.Config for why the values are not here.
	Variables []string `json:"variables"`

	Labels    map[string]string `json:"labels,omitempty"`
	Lifecycle lifecycle         `json:"lifecycle"`
	Revision  uint64            `json:"revision"`
}

func wireEnvironment(e apiv2.Environment) environment {
	ttl := ""
	if e.Lifecycle.TTL > 0 {
		ttl = e.Lifecycle.TTL.String()
	}
	return environment{
		ID:        e.ID,
		ProjectID: e.ProjectID,
		Name:      e.Name,
		Kind:      e.Kind,
		Targets: mapped(e.Targets, func(t apiv2.TargetBinding) targetBinding {
			return targetBinding{
				Name:   t.Name,
				Driver: t.Driver,
				Config: t.Config,
				Labels: t.Labels,
			}
		}),
		Policies:  mapped(e.Policies, wirePolicyBinding),
		Variables: e.Variables,
		Labels:    e.Labels,
		Lifecycle: lifecycle{TTL: ttl, DeleteOnClose: e.Lifecycle.DeleteOnClose},
		Revision:  e.Revision,
	}
}

func wirePolicyBinding(p apiv2.PolicyBinding) policyBinding {
	return policyBinding{Name: p.Name, Ref: p.Ref, Mode: p.Mode}
}

type createEnvironmentBody struct {
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Targets   []targetSpec      `json:"targets,omitempty"`
	Policies  []policyBinding   `json:"policies,omitempty"`
	Variables map[string]value  `json:"variables,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Lifecycle lifecycle         `json:"lifecycle,omitzero"`
}

func (h *api) createEnvironment(w http.ResponseWriter, r *http.Request, who identity.Principal) error {
	var body createEnvironmentBody
	if err := h.decode(w, r, &body); err != nil {
		return err
	}
	life, err := parseLifecycle(body.Lifecycle)
	if err != nil {
		return err
	}
	e, err := h.svc.CreateEnvironment(r.Context(), apiv2.CreateEnvironmentRequest{
		ProjectID: r.PathValue("id"),
		Name:      body.Name,
		Kind:      body.Kind,
		Targets: mapped(body.Targets, func(t targetSpec) apiv2.TargetSpec {
			return apiv2.TargetSpec{
				Name:   t.Name,
				Driver: t.Driver,
				Config: parseValues(t.Config),
				Labels: t.Labels,
			}
		}),
		Policies:       mapped(body.Policies, parsePolicyBinding),
		Variables:      parseValues(body.Variables),
		Labels:         body.Labels,
		Lifecycle:      life,
		Actor:          who,
		IdempotencyKey: idempotencyKey(r),
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireEnvironment(e))
	return nil
}

func parseLifecycle(l lifecycle) (apiv2.Lifecycle, error) {
	out := apiv2.Lifecycle{DeleteOnClose: l.DeleteOnClose}
	if l.TTL == "" {
		return out, nil
	}
	ttl, err := time.ParseDuration(l.TTL)
	if err != nil {
		return apiv2.Lifecycle{}, badRequest(errors.New("lifecycle.ttl is not a duration"))
	}
	out.TTL = ttl
	return out, nil
}

func parseValues(in map[string]value) map[string]apiv2.Value {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]apiv2.Value, len(in))
	for k, v := range in {
		out[k] = apiv2.Value{Literal: v.Literal, Secret: v.Secret}
	}
	return out
}

func parsePolicyBinding(p policyBinding) apiv2.PolicyBinding {
	return apiv2.PolicyBinding{Name: p.Name, Ref: p.Ref, Mode: p.Mode}
}

func (h *api) listEnvironments(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	p, err := pageOf(r)
	if err != nil {
		return err
	}
	out, err := h.svc.ListEnvironments(r.Context(), apiv2.ListEnvironmentsRequest{
		ProjectID: r.PathValue("id"),
		Page:      p,
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, struct {
		Environments []environment `json:"environments"`
		Next         string        `json:"next_page_token,omitempty"`
	}{mapped(out.Environments, wireEnvironment), out.Next})
	return nil
}

func (h *api) getEnvironment(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	e, err := h.svc.GetEnvironment(r.Context(), apiv2.GetEnvironmentRequest{ID: r.PathValue("id")})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireEnvironment(e))
	return nil
}

// --- artifacts and releases -------------------------------------------------

type documentRef struct {
	Kind    string `json:"kind,omitempty"`
	Format  string `json:"format,omitempty"`
	Locator string `json:"locator,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

func wireDocumentRef(d apiv2.DocumentRef) documentRef {
	return documentRef{Kind: d.Kind, Format: d.Format, Locator: d.Locator, Digest: d.Digest}
}

func parseDocumentRef(d documentRef) apiv2.DocumentRef {
	return apiv2.DocumentRef{Kind: d.Kind, Format: d.Format, Locator: d.Locator, Digest: d.Digest}
}

type artifact struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Kind      string `json:"kind"`
	Digest    string `json:"digest"`

	// Locator is pinned to the digest. A tag resolved again at pull time can
	// hand a different image to production than the one that was verified.
	Locator string `json:"locator"`

	Size      int64             `json:"size,omitempty"`
	MediaType string            `json:"media_type,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`

	Provenance documentRef   `json:"provenance,omitzero"`
	SBOMs      []documentRef `json:"sboms"`
	Signatures []documentRef `json:"signatures"`
	CreatedAt  time.Time     `json:"created_at"`
}

func wireArtifact(a apiv2.Artifact) artifact {
	return artifact{
		ID:         a.ID,
		ProjectID:  a.ProjectID,
		Kind:       a.Kind,
		Digest:     a.Digest,
		Locator:    a.Locator,
		Size:       a.Size,
		MediaType:  a.MediaType,
		Metadata:   a.Metadata,
		Provenance: wireDocumentRef(a.Provenance),
		SBOMs:      mapped(a.SBOMs, wireDocumentRef),
		Signatures: mapped(a.Signatures, wireDocumentRef),
		CreatedAt:  a.CreatedAt,
	}
}

type registerArtifactBody struct {
	Kind      string `json:"kind"`
	Digest    string `json:"digest"`
	Locator   string `json:"locator"`
	Size      int64  `json:"size,omitempty"`
	MediaType string `json:"media_type,omitempty"`

	Metadata   map[string]string `json:"metadata,omitempty"`
	Provenance documentRef       `json:"provenance,omitzero"`
	SBOMs      []documentRef     `json:"sboms,omitempty"`
	Signatures []documentRef     `json:"signatures,omitempty"`
}

// registerArtifact takes no idempotency key, and that is not an omission:
// content identifies an artifact (INV-002), so the digest already is one.
func (h *api) registerArtifact(w http.ResponseWriter, r *http.Request, who identity.Principal) error {
	var body registerArtifactBody
	if err := h.decode(w, r, &body); err != nil {
		return err
	}
	a, err := h.svc.RegisterArtifact(r.Context(), apiv2.RegisterArtifactRequest{
		ProjectID:  r.PathValue("id"),
		Kind:       body.Kind,
		Digest:     body.Digest,
		Locator:    body.Locator,
		Size:       body.Size,
		MediaType:  body.MediaType,
		Metadata:   body.Metadata,
		Provenance: parseDocumentRef(body.Provenance),
		SBOMs:      mapped(body.SBOMs, parseDocumentRef),
		Signatures: mapped(body.Signatures, parseDocumentRef),
		Actor:      who,
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireArtifact(a))
	return nil
}

func (h *api) listArtifacts(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	p, err := pageOf(r)
	if err != nil {
		return err
	}
	out, err := h.svc.ListArtifacts(r.Context(), apiv2.ListArtifactsRequest{
		ProjectID: r.PathValue("id"),
		Page:      p,
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, struct {
		Artifacts []artifact `json:"artifacts"`
		Next      string     `json:"next_page_token,omitempty"`
	}{mapped(out.Artifacts, wireArtifact), out.Next})
	return nil
}

func (h *api) getArtifact(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	a, err := h.svc.GetArtifact(r.Context(), apiv2.GetArtifactRequest{ID: r.PathValue("id")})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireArtifact(a))
	return nil
}

type source struct {
	Provider   string `json:"provider,omitempty"`
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Ref        string `json:"ref,omitempty"`
	TreeDigest string `json:"tree_digest,omitempty"`
	URL        string `json:"url,omitempty"`
}

type releaseArtifact struct {
	Role       string `json:"role"`
	ArtifactID string `json:"artifact_id"`
}

type release struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	Version     string            `json:"version"`
	Artifacts   []releaseArtifact `json:"artifacts"`
	Source      source            `json:"source,omitzero"`
	Provenance  documentRef       `json:"provenance,omitzero"`
	CreatedBy   principal         `json:"created_by"`
	CreatedAt   time.Time         `json:"created_at"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func wireRelease(r apiv2.Release) release {
	return release{
		ID:        r.ID,
		ProjectID: r.ProjectID,
		Version:   r.Version,
		Artifacts: mapped(r.Artifacts, func(a apiv2.ReleaseArtifact) releaseArtifact {
			return releaseArtifact{Role: a.Role, ArtifactID: a.ArtifactID}
		}),
		Source:      source(r.Source),
		Provenance:  wireDocumentRef(r.Provenance),
		CreatedBy:   wirePrincipal(r.CreatedBy),
		CreatedAt:   r.CreatedAt,
		Labels:      r.Labels,
		Annotations: r.Annotations,
	}
}

type createReleaseBody struct {
	Version   string            `json:"version"`
	Artifacts []releaseArtifact `json:"artifacts"`
	Source    source            `json:"source,omitzero"`

	Provenance  documentRef       `json:"provenance,omitzero"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func (h *api) createRelease(w http.ResponseWriter, r *http.Request, who identity.Principal) error {
	var body createReleaseBody
	if err := h.decode(w, r, &body); err != nil {
		return err
	}
	rel, err := h.svc.CreateRelease(r.Context(), apiv2.CreateReleaseRequest{
		ProjectID: r.PathValue("id"),
		Version:   body.Version,
		Artifacts: mapped(body.Artifacts, func(a releaseArtifact) apiv2.ReleaseArtifact {
			return apiv2.ReleaseArtifact{Role: a.Role, ArtifactID: a.ArtifactID}
		}),
		Source:         apiv2.Source(body.Source),
		Provenance:     parseDocumentRef(body.Provenance),
		Labels:         body.Labels,
		Annotations:    body.Annotations,
		Actor:          who,
		IdempotencyKey: idempotencyKey(r),
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireRelease(rel))
	return nil
}

func (h *api) listReleases(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	p, err := pageOf(r)
	if err != nil {
		return err
	}
	out, err := h.svc.ListReleases(r.Context(), apiv2.ListReleasesRequest{
		ProjectID: r.PathValue("id"),
		Page:      p,
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, struct {
		Releases []release `json:"releases"`
		Next     string    `json:"next_page_token,omitempty"`
	}{mapped(out.Releases, wireRelease), out.Next})
	return nil
}

func (h *api) getRelease(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	rel, err := h.svc.GetRelease(r.Context(), apiv2.GetReleaseRequest{ID: r.PathValue("id")})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireRelease(rel))
	return nil
}

// --- an environment's deployments -------------------------------------------

type planBody struct {
	ReleaseID string `json:"release_id"`

	// Strategy is what the plan's operations are built for. Empty is refused by
	// the service rather than defaulted: guessing it would have an approver
	// review a rollout nobody asked for.
	Strategy string `json:"strategy"`
}

// environmentDeployments serves both the list and the plan command, because
// they share a path segment: §23.2 puts the verb on the collection
// (`/deployments:plan`) rather than on a deployment that does not exist yet.
func (h *api) environmentDeployments(
	w http.ResponseWriter, r *http.Request, who identity.Principal,
) error {
	collection, verb := splitRef(r.PathValue("ref"))
	if collection != "deployments" {
		return noSuchRoute()
	}
	switch {
	case r.Method == http.MethodGet && verb == "":
		p, err := pageOf(r)
		if err != nil {
			return err
		}
		out, err := h.svc.ListDeployments(r.Context(), apiv2.ListDeploymentsRequest{
			EnvironmentID: r.PathValue("id"),
			Page:          p,
		})
		if err != nil {
			return err
		}
		respond(w, http.StatusOK, struct {
			Deployments []deployment `json:"deployments"`
			Next        string       `json:"next_page_token,omitempty"`
		}{mapped(out.Deployments, wireDeployment), out.Next})
		return nil

	case r.Method == http.MethodPost && verb == "plan":
		var body planBody
		if err := h.decode(w, r, &body); err != nil {
			return err
		}
		p, err := h.svc.CreatePlan(r.Context(), apiv2.CreatePlanRequest{
			EnvironmentID:  r.PathValue("id"),
			ReleaseID:      body.ReleaseID,
			Strategy:       body.Strategy,
			Actor:          who,
			IdempotencyKey: idempotencyKey(r),
		})
		if err != nil {
			return err
		}
		respond(w, http.StatusOK, wirePlan(p))
		return nil
	}
	return noSuchRoute()
}
