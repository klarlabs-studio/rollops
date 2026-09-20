// Deployments, plans and the commands against them (§23.2).
//
// A command is POST to the resource with the verb in the same path segment as
// the id — `/v2/deployments/dpl_1:cancel`. The verb is not a sub-resource
// because it is not a thing: there is no `/cancel` to fetch, and modelling one
// would invite a GET that means nothing. Splitting the segment is this file's
// job, and a verb nobody serves must not be read as an id that ends in a colon.
//
// Every command returns the deployment rather than an acknowledgement. §23.3
// asks a mutation whose execution is asynchronous to return identity
// immediately, and the deployment is the identity plus the one thing that has
// changed so far, its status.
package httpapi

import (
	"net/http"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

func (h *api) getDeployment(
	w http.ResponseWriter, r *http.Request, _ identity.Principal,
) error {
	id, verb := splitRef(r.PathValue("ref"))
	if verb != "" {
		// A command is a POST. Serving it here would let a GET change something.
		return noSuchRoute()
	}
	d, err := h.svc.GetDeployment(r.Context(), apiv2.GetDeploymentRequest{ID: id})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireDeployment(d))
	return nil
}

// reasoned is the body every command that records why shares.
type reasoned struct {
	Reason string `json:"reason,omitempty"`
}

type approveBody struct {
	PlanHash string `json:"plan_hash"`
	Granted  bool   `json:"granted"`
	Reason   string `json:"reason,omitempty"`
}

func (h *api) deploymentCommand(
	w http.ResponseWriter, r *http.Request, who identity.Principal,
) error {
	id, verb := splitRef(r.PathValue("ref"))
	key := idempotencyKey(r)

	// Verifying is the one command that does not answer with a deployment: the
	// verdict and where it left the deployment are both wanted, and a caller
	// that had to fetch the deployment separately would read it after whatever
	// happened next.
	if verb == "verify" {
		// It asks a question rather than overriding an answer, so there is
		// nothing for a caller to justify and no body to read.
		out, err := h.svc.VerifyDeployment(r.Context(), apiv2.VerifyDeploymentRequest{
			DeploymentID:   id,
			Actor:          who,
			IdempotencyKey: key,
		})
		if err != nil {
			return err
		}
		respond(w, http.StatusOK, struct {
			Deployment deployment      `json:"deployment"`
			Run        verificationRun `json:"run"`
		}{wireDeployment(out.Deployment), wireVerificationRun(out.Run)})
		return nil
	}

	var (
		d   apiv2.Deployment
		err error
	)
	switch verb {
	case "cancel":
		var body reasoned
		if err := h.decode(w, r, &body); err != nil {
			return err
		}
		d, err = h.svc.CancelDeployment(r.Context(), apiv2.CancelDeploymentRequest{
			DeploymentID:   id,
			Reason:         body.Reason,
			Actor:          who,
			IdempotencyKey: key,
		})

	case "promote":
		var body reasoned
		if err := h.decode(w, r, &body); err != nil {
			return err
		}
		d, err = h.svc.PromoteDeployment(r.Context(), apiv2.PromoteDeploymentRequest{
			DeploymentID:   id,
			Reason:         body.Reason,
			Actor:          who,
			IdempotencyKey: key,
		})

	case "rollback":
		var body reasoned
		if err := h.decode(w, r, &body); err != nil {
			return err
		}
		d, err = h.svc.RollbackDeployment(r.Context(), apiv2.RollbackDeploymentRequest{
			DeploymentID:   id,
			Reason:         body.Reason,
			Actor:          who,
			IdempotencyKey: key,
		})

	case "approve":
		var body approveBody
		if err := h.decode(w, r, &body); err != nil {
			return err
		}
		d, err = h.svc.ApproveDeployment(r.Context(), apiv2.ApproveDeploymentRequest{
			DeploymentID:   id,
			PlanHash:       body.PlanHash,
			Granted:        body.Granted,
			Reason:         body.Reason,
			Actor:          who,
			IdempotencyKey: key,
		})

	default:
		return noSuchRoute()
	}
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wireDeployment(d))
	return nil
}

func (h *api) getPlan(w http.ResponseWriter, r *http.Request, _ identity.Principal) error {
	id, verb := splitRef(r.PathValue("ref"))
	if verb != "" {
		return noSuchRoute()
	}
	p, err := h.svc.GetPlan(r.Context(), apiv2.GetPlanRequest{ID: id})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, wirePlan(p))
	return nil
}

type applyBody struct {
	// Detail is free text for whoever reads the timeline later. The trigger
	// type is not the caller's to choose: this endpoint is the API, and a
	// request that could claim "git" would write a cause into the record that
	// nothing else corroborates.
	Detail string `json:"detail,omitempty"`
}

func (h *api) planCommand(w http.ResponseWriter, r *http.Request, who identity.Principal) error {
	id, verb := splitRef(r.PathValue("ref"))
	if verb != "apply" {
		return noSuchRoute()
	}
	var body applyBody
	if err := h.decode(w, r, &body); err != nil {
		return err
	}
	// 202 rather than 200: the deployment is recorded and nothing has been
	// applied yet, which is precisely what Accepted means.
	d, err := h.svc.ApplyPlan(r.Context(), apiv2.ApplyPlanRequest{
		PlanID:         id,
		Detail:         body.Detail,
		Actor:          who,
		IdempotencyKey: idempotencyKey(r),
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusAccepted, wireDeployment(d))
	return nil
}

func (h *api) deploymentEvents(
	w http.ResponseWriter, r *http.Request, _ identity.Principal,
) error {
	p, err := pageOf(r)
	if err != nil {
		return err
	}
	out, err := h.svc.DeploymentEvents(r.Context(), apiv2.DeploymentEventsRequest{
		DeploymentID: r.PathValue("id"),
		Page:         p,
	})
	if err != nil {
		return err
	}
	respond(w, http.StatusOK, struct {
		Events []event `json:"events"`
		Next   string  `json:"next_page_token,omitempty"`
	}{mapped(out.Events, wireEvent), out.Next})
	return nil
}
