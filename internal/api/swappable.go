package api

import (
	"sync/atomic"

	"go.klarlabs.de/rollops/internal/rollout"
)

// SwappableTokenAuth is an Authenticator whose token map can be replaced while
// the server is running, so credentials can be rotated without a restart. It
// mirrors what security.Policy.ReplaceWith does for RBAC.
//
// Reads are lock-free (atomic load of an immutable map); Replace swaps in a new
// map wholesale rather than mutating the live one, so an in-flight Identify
// never observes a half-applied rotation.
type SwappableTokenAuth struct {
	current atomic.Pointer[TokenAuth]
}

// NewSwappableTokenAuth returns an Authenticator serving auth until replaced.
func NewSwappableTokenAuth(auth TokenAuth) *SwappableTokenAuth {
	s := &SwappableTokenAuth{}
	s.Replace(auth)
	return s
}

// Replace atomically swaps the token map. The caller must not mutate auth
// afterwards — ownership passes to the SwappableTokenAuth.
func (s *SwappableTokenAuth) Replace(auth TokenAuth) {
	if auth == nil {
		auth = TokenAuth{}
	}
	s.current.Store(&auth)
}

// Identify implements Authenticator against the current token map. An empty map
// resolves nothing, which is the fail-closed direction: every call is rejected
// until tokens are configured.
func (s *SwappableTokenAuth) Identify(token string) (rollout.Identity, bool) {
	cur := s.current.Load()
	if cur == nil {
		return rollout.Identity{}, false
	}
	return cur.Identify(token)
}

// Len reports how many tokens are currently configured, for startup and reload
// log lines.
func (s *SwappableTokenAuth) Len() int {
	cur := s.current.Load()
	if cur == nil {
		return 0
	}
	return len(*cur)
}
