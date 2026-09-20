// Package httpapi is the HTTP/JSON projection of the v2 service (§23.2).
//
// It carries request values in and response values out and decides nothing
// else. Every rule about what a call means lives in internal/api/v2, because a
// rule written here is a rule the gRPC server and the MCP tools do not have.
// What is genuinely this package's own is narrow: which path names which call,
// how a failure code becomes a status, and what the JSON looks like.
//
// The wire shape is declared here rather than inherited. The view types in
// api/v2 carry no json tags on purpose — tagging them would make every field
// rename a breaking API change decided in the wrong package, and a field added
// there would appear on the wire before anybody chose to publish it. The same
// reasoning the SQLite codec uses for its row structs.
//
// Authentication is injected. This package never learns what a credential looks
// like: it asks an Identifier who is calling and reports what it is told, so a
// deployment using bearer tokens and one using mTLS differ by a constructor
// argument rather than by a fork of the transport.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// Identifier says who is calling. It returns an error rather than a bool so
// that an implementation which has already worked out what kind of refusal
// this is can say so with an *apierr.Error; anything else is UNAUTHORIZED.
type Identifier interface {
	Identify(r *http.Request) (identity.Principal, error)
}

// IdentifierFunc adapts a function to Identifier.
type IdentifierFunc func(*http.Request) (identity.Principal, error)

// Identify implements Identifier.
func (f IdentifierFunc) Identify(r *http.Request) (identity.Principal, error) { return f(r) }

// DefaultMaxBodyBytes is the cap on a request body when Config names none.
// Every body this API takes is a handful of identifiers and some prose, so the
// limit is generous by two orders of magnitude and still small enough that a
// caller cannot make the server allocate whatever it likes.
const DefaultMaxBodyBytes int64 = 1 << 20

// Config names what the handler serves and who it trusts to say who is calling.
type Config struct {
	// Service answers every call. Required.
	Service *apiv2.Service

	// Identity resolves the caller. Required, and there is deliberately no
	// default: an API that authenticates when configured to would serve
	// anonymously when somebody forgot to.
	Identity Identifier

	// MaxBodyBytes caps a request body. Zero means DefaultMaxBodyBytes.
	MaxBodyBytes int64
}

type api struct {
	svc     *apiv2.Service
	who     Identifier
	maxBody int64
}

// New returns the routed handler, naming the first dependency it was not given.
func New(cfg Config) (http.Handler, error) {
	switch {
	case cfg.Service == nil:
		return nil, errors.New("httpapi: no service")
	case cfg.Identity == nil:
		return nil, errors.New("httpapi: no identifier")
	}
	h := &api{svc: cfg.Service, who: cfg.Identity, maxBody: cfg.MaxBodyBytes}
	if h.maxBody <= 0 {
		h.maxBody = DefaultMaxBodyBytes
	}
	return h.routes(), nil
}

// handler is what a route does. It returns an error rather than writing one,
// so that there is exactly one place a failure is rendered and no handler can
// invent a second envelope.
type handler func(http.ResponseWriter, *http.Request, identity.Principal) error

// methods is a route's handlers by HTTP method.
//
// Patterns are registered without a method and dispatched here rather than
// through ServeMux's "POST /path" form, for one reason: ServeMux answers a
// method it has no pattern for with its own plain-text 405, which is a second
// error shape for a caller to parse. §23.4 has no code for a method, and a
// method this API does not serve on a path is a route it does not serve, so it
// is answered as one.
type methods map[string]handler

func (h *api) routes() http.Handler {
	mux := http.NewServeMux()
	register := func(pattern string, m methods) {
		mux.Handle(pattern, h.serve(func(
			w http.ResponseWriter, r *http.Request, who identity.Principal,
		) error {
			fn, ok := m[r.Method]
			if !ok {
				return noSuchRoute()
			}
			return fn(w, r, who)
		}))
	}

	register("/v2/projects", methods{
		http.MethodGet:  h.listProjects,
		http.MethodPost: h.createProject,
	})
	register("/v2/projects/{id}", methods{http.MethodGet: h.getProject})
	register("/v2/projects/{id}/environments", methods{
		http.MethodGet:  h.listEnvironments,
		http.MethodPost: h.createEnvironment,
	})
	register("/v2/projects/{id}/artifacts", methods{
		http.MethodGet:  h.listArtifacts,
		http.MethodPost: h.registerArtifact,
	})
	register("/v2/projects/{id}/releases", methods{
		http.MethodGet:  h.listReleases,
		http.MethodPost: h.createRelease,
	})

	register("/v2/environments/{id}", methods{http.MethodGet: h.getEnvironment})
	register("/v2/environments/{id}/{ref}", methods{
		http.MethodGet:  h.environmentDeployments,
		http.MethodPost: h.environmentDeployments,
	})

	register("/v2/artifacts/{id}", methods{http.MethodGet: h.getArtifact})
	register("/v2/releases/{id}", methods{http.MethodGet: h.getRelease})

	// One pattern for a deployment rather than two, because the id and the verb
	// share a path segment: ServeMux cannot express "{id}:verify", so what it
	// matches is the whole segment and splitting it is this package's job.
	register("/v2/deployments/{ref}", methods{
		http.MethodGet:  h.getDeployment,
		http.MethodPost: h.deploymentCommand,
	})
	register("/v2/deployments/{id}/events", methods{
		http.MethodGet: h.deploymentEvents,
	})
	register("/v2/deployment-plans/{ref}", methods{
		http.MethodGet:  h.getPlan,
		http.MethodPost: h.planCommand,
	})

	// A route nobody registered is still a route this handler answers, so that
	// a 404 arrives in the same envelope as everything else — and so that it
	// arrives after authentication rather than before.
	register("/", methods{})

	return mux
}

// deploymentRef splits the path segment that carries both the deployment id and
// an optional verb. The id may not contain a colon, so the first one separates
// them; a verb nobody serves must not be read as an id that ends in one.
func splitRef(ref string) (id, verb string) {
	id, verb, _ = strings.Cut(ref, ":")
	return id, verb
}
