package mcp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	mcpserver "go.klarlabs.de/mcp"
	"go.klarlabs.de/mcp/transport"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/rollout"
)

// ErrUnauthenticated is returned by a tool handler when the request context
// carries no authenticated caller, and by the transport authorize hook when a
// bearer token does not resolve. The MCP surface is fail-closed: with no
// resolvable identity nothing runs.
var ErrUnauthenticated = errors.New("mcp: no authenticated caller")

// identityCtxKey is the private context key under which the transport auth hook
// stashes the resolved caller identity. A struct{} key cannot collide with keys
// from other packages.
type identityCtxKey struct{}

// WithIdentity returns a child context carrying the authenticated caller
// identity. The transport hook uses it per request; tool handlers read it back
// via the unexported accessor.
func WithIdentity(ctx context.Context, id rollout.Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// identityFrom returns the caller identity the transport auth hook injected, and
// ok=false when none is present (an unauthenticated call).
func identityFrom(ctx context.Context) (rollout.Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(rollout.Identity)
	return id, ok
}

// caller resolves the authenticated identity for a tool call, failing closed
// with ErrUnauthenticated when the context carries none. Every handler calls
// this before doing any work, so a request that reached a handler without a
// resolved identity performs no engine operation and authorizes as nobody.
func (t *Tools) caller(ctx context.Context) (rollout.Identity, error) {
	if id, ok := identityFrom(ctx); ok {
		return id, nil
	}
	return rollout.Identity{}, ErrUnauthenticated
}

// AuthServeOptions returns the mcp-go HTTP transport options that authenticate
// every MCP request by bearer token, reusing the same Authenticator model as the
// HTTP and gRPC surfaces so there is a single token format across the daemon.
//
// It installs two hooks, both fail-closed:
//
//   - WithAuthorize runs before any tool handler on every request path (POST /mcp
//     and the SSE stream). A request whose "Authorization: Bearer <token>" does
//     not resolve to an identity is rejected there and then, so the handler is
//     never reached — no token, no fallback identity, no work.
//   - WithRequestContextFn runs once per HTTP request and injects the resolved
//     rollout.Identity into the context that propagates to the tool handlers, so
//     RBAC authorizes each caller as itself.
//
// mcp-go ships no auth of its own; these are the library's documented seams for
// deriving a request-scoped caller from transport details.
func AuthServeOptions(auth api.Authenticator) []mcpserver.HTTPOption {
	return []mcpserver.HTTPOption{
		transport.WithAuthorize(authorize(auth)),
		transport.WithRequestContextFn(injectIdentity(auth)),
	}
}

// authorize is the fail-closed gate wired into WithAuthorize: it resolves the
// request's bearer token and returns ErrUnauthenticated (rejecting before any
// handler runs) when the token is absent or does not map to an identity.
func authorize(auth api.Authenticator) func(*http.Request) error {
	return func(r *http.Request) error {
		tok := bearer(r)
		if _, ok := auth.Identify(tok); tok == "" || !ok {
			return ErrUnauthenticated
		}
		return nil
	}
}

// injectIdentity is wired into WithRequestContextFn: it resolves the request's
// bearer token and, when it maps to an identity, returns a context carrying that
// caller for the tool handlers. An unresolved token yields the context unchanged
// (the handler then fails closed via caller); in practice authorize has already
// rejected that request, so this is defense in depth.
func injectIdentity(auth api.Authenticator) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		tok := bearer(r)
		if id, ok := auth.Identify(tok); tok != "" && ok {
			return WithIdentity(ctx, id)
		}
		return ctx
	}
}

// bearer extracts the token from an "Authorization: Bearer <token>" header,
// mirroring the HTTP API's parser. Empty when absent or not a bearer scheme.
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}
