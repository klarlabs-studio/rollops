package identity

import (
	"errors"
	"fmt"
	"strings"
)

// PrincipalType distinguishes who or what performed an action. Agents are named
// separately from humans because the guardrails that apply to them differ, and
// an audit trail that cannot tell them apart cannot answer the question anyone
// actually asks after an incident.
type PrincipalType string

const (
	PrincipalHuman     PrincipalType = "human"
	PrincipalService   PrincipalType = "service"
	PrincipalAgent     PrincipalType = "agent"
	PrincipalGit       PrincipalType = "git"
	PrincipalScheduler PrincipalType = "scheduler"
	PrincipalSystem    PrincipalType = "system"
)

// Principal is the actor a mutation is attributed to (INV-005). Every command
// that changes state carries one; there is no anonymous path.
//
// It carries no authorization decision (spec §4.10). Who someone is and what
// they may do are answered by different layers, and merging them here would put
// the answer where nothing can re-evaluate it.
type Principal struct {
	ID          string
	Type        PrincipalType
	DisplayName string
	Claims      map[string]string
}

// redacted replaces any claim value that may carry credential material.
const redacted = "[redacted]"

// Claim keys whose values are treated as secret. Matching is on a lowercased
// substring, so "x-authorization" and "API_KEY" are both caught.
//
// This is a denylist and therefore incomplete: an identity provider that names
// its bearer token something unexpected will pass straight through. Treat it as
// the last line of defence, not the only one — claims that are known to be
// sensitive should not be copied onto a Principal in the first place.
var secretClaimFragments = []string{
	"apikey",
	"api_key",
	"authorization",
	"credential",
	"passwd",
	"password",
	"private_key",
	"secret",
	"token",
}

// ErrInvalidPrincipal reports an actor that cannot be attributed.
var ErrInvalidPrincipal = errors.New("identity: invalid principal")

// Validate reports whether the principal can be attributed. The zero value
// never can.
func (p Principal) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("%w: missing id", ErrInvalidPrincipal)
	}
	switch p.Type {
	case PrincipalHuman, PrincipalService, PrincipalAgent,
		PrincipalGit, PrincipalScheduler, PrincipalSystem:
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidPrincipal, p.Type)
	}
	return nil
}

// Redacted returns a copy safe to render in an audit entry, an API response or
// a log (INV-012). The receiver is untouched.
func (p Principal) Redacted() Principal {
	p.Claims = RedactSecrets(p.Claims)
	return p
}

// RedactSecrets returns a copy of m with the value of every secret-looking key
// replaced (INV-012). A nil map stays nil, so an absent map does not quietly
// become an empty one on the way through.
//
// It is exported because claims are not the only string map that travels with a
// mutation: an event envelope carries metadata of the same shape, and metadata
// is where a forwarded request header lands. One denylist guarding both beats
// two that drift apart.
func RedactSecrets(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if isSecretClaim(k) {
			out[k] = redacted
			continue
		}
		out[k] = v
	}
	return out
}

func isSecretClaim(key string) bool {
	k := strings.ToLower(key)
	for _, fragment := range secretClaimFragments {
		if strings.Contains(k, fragment) {
			return true
		}
	}
	return false
}
